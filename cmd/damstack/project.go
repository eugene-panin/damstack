package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/engine"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/release"
	"github.com/eugene-panin/damstack/internal/setup"
	"github.com/eugene-panin/damstack/internal/toolbox"
)

var errNoProject = errors.New("this is not a damstack project; cd into one, such as ~/damstack/<name>")

func currentProject() (*project.Project, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	p, err := project.Find(wd)
	if err == nil && p == nil {
		err = errNoProject
	}
	return p, err
}

func statusCommand(s *streams) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the project you are in: its stack, and how each step went last",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			p, err := currentProject()
			if err != nil {
				return err
			}
			ref := p.Meta.Stack
			fmt.Fprintf(s.out, "%s, in %s\n", p.Meta.Name, p.Dir)
			fmt.Fprintf(s.out, "stack %s %s %.12s from %s\n", ref.Name, ref.Tag, ref.Commit, ref.URL)
			fmt.Fprintf(s.out, "set up %s\n\n", p.Meta.Created.Local().Format(time.DateTime))
			entries, err := p.History()
			if err != nil {
				return err
			}
			last := map[string]project.Entry{}
			var order []string
			for _, e := range entries {
				if e.Command != "deploy" {
					continue
				}
				if _, ok := last[e.Step]; !ok {
					order = append(order, e.Step)
				}
				last[e.Step] = e
			}
			if m, _ := cachedStack(p); m != nil {
				order = nil
				for _, step := range m.Steps {
					order = append(order, step.Name)
				}
			}
			w := tabwriter.NewWriter(s.out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "STEP\tLAST RUN\tRESULT\tSTACK")
			for _, name := range order {
				e, ok := last[name]
				if !ok {
					fmt.Fprintf(w, "%s\t-\tnot run yet\t\n", name)
					continue
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, e.Time.Local().Format(time.DateTime), e.Result, e.Stack)
			}
			return w.Flush()
		},
	}
}

func historyCommand(s *streams) *cobra.Command {
	return &cobra.Command{
		Use:   "history",
		Short: "List everything damstack ran on the project you are in",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			p, err := currentProject()
			if err != nil {
				return err
			}
			entries, err := p.History()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(s.out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "TIME\tCOMMAND\tSTEP\tRESULT\tSECONDS\tSTACK\tDAMSTACK")
			for _, e := range entries {
				step := e.Step
				if step == "" {
					step = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%.1f\t%s\t%s\n", e.Time.Local().Format(time.DateTime), e.Command, step,
					e.Result, e.Seconds, e.Stack, e.Damstack)
				if e.Error != "" {
					fmt.Fprintf(w, "\t\t\t%s\t\t\t\n", firstLine(e.Error))
				}
			}
			return w.Flush()
		},
	}
}

// stackCommands are the commands of the stack of the project in wd, when the
// stack is on this machine.
func stackCommands(s *streams, taken func(string) bool) []*cobra.Command {
	p, err := currentProject()
	if err != nil {
		return nil
	}
	m, dir := cachedStack(p)
	if m == nil {
		return nil
	}
	var cmds []*cobra.Command
	for _, name := range slices.Sorted(maps.Keys(m.Commands)) {
		if taken(name) {
			continue
		}
		step := m.Commands[name]
		cmds = append(cmds, &cobra.Command{
			Use:                name + " [args]",
			Short:              "A command of the stack " + m.Name,
			DisableFlagParsing: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				e, _, err := newEngine(cmd.Context(), s, p, m, dir)
				if err != nil {
					return err
				}
				start := time.Now()
				err = e.Run(cmd.Context(), step, args)
				if rerr := record(p, name, "", start, err); rerr != nil && err == nil {
					err = rerr
				}
				return err
			},
		})
	}
	return cmds
}

// checkStack proves a stack without a server, on a project set up in the
// cache from the stack's test answers.
func checkStack(ctx context.Context, s *streams, dir string) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	m, err := manifest.Load(dir, release.Version)
	if err != nil {
		return err
	}
	given := map[string]any{}
	data, err := os.ReadFile(filepath.Join(dir, m.Check.Answers))
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(data, &given); err != nil {
		return fmt.Errorf("%s: %w", m.Check.Answers, err)
	}
	delete(given, "project")

	cache, err := config.CacheDir()
	if err != nil {
		return err
	}
	base := filepath.Join(cache, "check")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(base, m.Name+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	b := make([]byte, 32)
	rand.Read(b)
	password := base64.RawURLEncoding.EncodeToString(b)
	passwordPath := filepath.Join(tmp, "vault", "vault-pass")
	if err := os.Mkdir(filepath.Dir(passwordPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(passwordPath, []byte(password+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "== a project from %s\n", m.Check.Answers)
	p, _, err := setup.Create(setup.Options{
		Manifest: m, Stack: dir, Name: "check", Dir: filepath.Join(tmp, "project"),
		Ref:   project.StackRef{Name: m.Name, URL: "file://" + dir, Tag: devTag},
		Given: given, Placeholders: true, Password: password,
	})
	if err != nil {
		return err
	}
	image := m.Image
	if image == "" {
		image = release.ImageRef()
	}
	runner := &toolbox.Runner{
		Image: image, Stack: dir, Project: p.Dir, Password: passwordPath, PublicKey: "ssh-ed25519 AAAA damstack-check",
		UID: os.Getuid(), GID: os.Getgid(), Stdin: nil, Stdout: s.out, Stderr: s.err,
	}
	e := &engine.Engine{Manifest: m, Stack: dir, Project: p, Runner: runner, Password: password, Out: s.out}
	if err := e.Check(ctx); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "\n%s: all checks passed\n", m.Name)
	return nil
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}
