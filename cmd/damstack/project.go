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

var errNoProject = errors.New("there is no project yet: damstack deploy sets one up")

func statusCommand(s *streams) *cobra.Command {
	var live, asJSON bool
	cmd := &cobra.Command{
		Use:   "status [project]",
		Short: "Show a project: its stack and how each step went last; --live checks the server now",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if live && asJSON {
				return asUsage(cmd, errors.New("invalid argument: --json shows the history, --live checks the server; pass one"))
			}
			p, err := pickProject(s, firstArg(args))
			if err != nil {
				return err
			}
			if live {
				m, dir, err := projectStack(cmd.Context(), p, "")
				if err != nil {
					return err
				}
				return liveCheck(cmd.Context(), s, p, m, dir)
			}
			steps, err := lastRuns(p)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(s.out, newStatusJSON(p, steps))
			}
			ref := p.Meta.Stack
			fmt.Fprintf(s.out, "%s, in %s\n", p.Meta.Name, p.Dir)
			fmt.Fprintf(s.out, "stack %s %s %.12s from %s\n", ref.Name, ref.Tag, ref.Commit, ref.URL)
			fmt.Fprintf(s.out, "set up %s\n\n", p.Meta.Created.Local().Format(time.DateTime))
			fmt.Fprintln(s.out, "The last run of each step, from the history; damstack status --live checks the server now.")
			fmt.Fprintln(s.out)
			w := tabwriter.NewWriter(s.out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "STEP\tLAST RUN\tTHAT RUN\tSTACK THEN")
			for _, st := range steps {
				if st.last == nil {
					fmt.Fprintf(w, "%s\t-\tnot run yet\t\n", st.name)
					continue
				}
				e := st.last
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", st.name, e.Time.Local().Format(time.DateTime), e.Result, e.Stack)
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&live, "live", false, "check the server now: playbooks in check mode and OpenTofu plans, changing nothing")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the project and the last run of each step as JSON")
	return cmd
}

// stepRun is a step of a project and its last deploy, nil when it never ran.
type stepRun struct {
	name string
	last *project.Entry
}

// lastRuns are the steps of p in the order they run, with the last deploy of each.
func lastRuns(p *project.Project) ([]stepRun, error) {
	entries, err := p.History()
	if err != nil {
		return nil, err
	}
	last := map[string]project.Entry{}
	var order []string
	for _, e := range entries {
		if e.Command != "deploy" && e.Command != "import" {
			continue
		}
		if _, ok := last[e.Step]; !ok {
			order = append(order, e.Step)
		}
		last[e.Step] = e
	}
	if m, _ := cachedStack(p); m != nil {
		order = stepOrder(p, m)
	}
	steps := make([]stepRun, 0, len(order))
	for _, name := range order {
		st := stepRun{name: name}
		if e, ok := last[name]; ok {
			st.last = &e
		}
		steps = append(steps, st)
	}
	return steps, nil
}

func historyCommand(s *streams) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "history [project]",
		Short: "List everything damstack ran on a project",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			p, err := pickProject(s, firstArg(args))
			if err != nil {
				return err
			}
			entries, err := p.History()
			if err != nil {
				return err
			}
			if asJSON {
				runs := make([]runJSON, 0, len(entries))
				for _, e := range entries {
					runs = append(runs, newRunJSON(e))
				}
				return writeJSON(s.out, runs)
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
	cmd.Flags().BoolVar(&asJSON, "json", false, "print every run as JSON")
	return cmd
}

// stepOrder names the steps of a deploy in the order they run, those of an
// app as app/step.
func stepOrder(p *project.Project, m *manifest.Manifest) []string {
	var before, after []string
	for _, step := range m.Steps {
		if step.AfterApps {
			after = append(after, step.Name)
		} else {
			before = append(before, step.Name)
		}
	}
	for _, ref := range p.Meta.Apps {
		am, _ := cachedRef(ref)
		if am == nil {
			continue
		}
		if _, t, ok := am.Target(m.Provides); ok {
			for _, step := range t.Steps {
				before = append(before, ref.Name+"/"+step.Name)
			}
		}
	}
	return append(before, after...)
}

// stackCommands are the commands of the stacks of a project, and of its apps
// under their names. When the project is clear without asking, they are its
// commands; otherwise those of every project, and the project is asked for
// when one runs.
func stackCommands(s *streams, taken func(string) bool) []*cobra.Command {
	p, cfg, err := resolveProject("")
	if err != nil || cfg == nil {
		return nil
	}
	projects := []*project.Project{p}
	if p == nil {
		projects = nil
		for _, entry := range cfg.Projects {
			if other, err := project.Open(entry.Path); err == nil {
				projects = append(projects, other)
			}
		}
	}
	platform := map[string]string{}
	apps := map[string]map[string]string{}
	for _, p := range projects {
		m, _ := cachedStack(p)
		if m == nil {
			continue
		}
		for name := range m.Commands {
			platform[name] = m.Name
		}
		for _, ref := range p.Meta.Apps {
			am, _ := cachedRef(ref)
			if am == nil {
				continue
			}
			if _, t, ok := am.Target(m.Provides); ok {
				for name := range t.Commands {
					if apps[ref.Name] == nil {
						apps[ref.Name] = map[string]string{}
					}
					apps[ref.Name][name] = ref.Name
				}
			}
		}
	}
	var cmds []*cobra.Command
	for _, name := range slices.Sorted(maps.Keys(platform)) {
		if !taken(name) {
			cmds = append(cmds, stackCommand(s, "", name, "A command of the stack "+platform[name]))
		}
	}
	for _, app := range slices.Sorted(maps.Keys(apps)) {
		if taken(app) || platform[app] != "" {
			continue
		}
		group := &cobra.Command{Use: app, Short: "Commands of the app " + app}
		for _, name := range slices.Sorted(maps.Keys(apps[app])) {
			group.AddCommand(stackCommand(s, app, name, "A command of the app "+app))
		}
		cmds = append(cmds, group)
	}
	return cmds
}

// stackCommand runs a command of the platform of a project, or of one of its
// apps, the project found when it runs.
func stackCommand(s *streams, app, name, short string) *cobra.Command {
	return &cobra.Command{
		Use:                name + " [args]",
		Short:              short,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := pickProject(s, "")
			if err != nil {
				return err
			}
			platform, pdir := cachedStack(p)
			if platform == nil {
				return fmt.Errorf("the stack of %s is not on this machine yet; damstack deploy %s fetches it", p.Meta.Name, p.Meta.Name)
			}
			m, dir, step, ok := platform, pdir, manifest.Step{}, false
			if app == "" {
				step, ok = platform.Commands[name]
			} else if ref, found := p.Meta.App(app); found {
				if am, adir := cachedRef(ref); am != nil {
					if _, t, has := am.Target(platform.Provides); has {
						m, dir = am, adir
						step, ok = t.Commands[name]
					}
				}
			}
			if !ok {
				return fmt.Errorf("%s has no command %s", p.Meta.Name, strings.TrimSpace(app+" "+name))
			}
			e, _, err := newEngine(cmd.Context(), s, p, m, dir)
			if err != nil {
				return err
			}
			if app != "" {
				e.App = app
				if e.BaseEnv, err = appEnv(p, platform, pdir, e.Password); err != nil {
					return err
				}
			}
			if step.Tunnel && platform.Server != nil {
				if err := waitTunnel(cmd.Context(), s, p, platform.Server); err != nil {
					return err
				}
			}
			command := strings.TrimSpace(app + " " + name)
			start := time.Now()
			err = e.Run(cmd.Context(), step, args)
			if rerr := record(p, command, "", start, err); rerr != nil && err == nil {
				err = rerr
			}
			return err
		},
	}
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
	ref := project.StackRef{Name: m.Name, URL: "file://" + dir, Tag: devTag}
	var p *project.Project
	if m.IsApp() {
		fmt.Fprintf(s.out, "== a project on %s, with the app from %s\n", m.Check.Platform, m.Check.Answers)
		platform, err := os.ReadFile(filepath.Join(dir, m.Check.Platform))
		if err != nil {
			return err
		}
		p, err = project.Create(filepath.Join(tmp, "project"), project.Meta{Name: "check"}, map[string][]byte{project.ConfigFile: platform})
		if err != nil {
			return err
		}
		err = setup.AddApp(setup.AppOptions{Manifest: m, Stack: dir, Ref: ref, Project: p, Password: password, Given: given, Placeholders: true})
		if err != nil {
			return err
		}
	} else {
		fmt.Fprintf(s.out, "== a project from %s\n", m.Check.Answers)
		p, _, err = setup.Create(setup.Options{
			Manifest: m, Stack: dir, Name: "check", Dir: filepath.Join(tmp, "project"),
			Ref: ref, Given: given, Placeholders: true, Password: password,
		})
		if err != nil {
			return err
		}
		if _, err := ensureKeys(p, m, dir, password); err != nil {
			return err
		}
	}
	tb, cache, err := fetchToolbox(ctx, s, m)
	if err != nil {
		return err
	}
	runner := &toolbox.Runner{
		Toolbox: tb, Stack: dir, Project: p.Dir, Password: passwordPath, Cache: cache,
		PublicKey: "ssh-ed25519 AAAA damstack-check", Stdout: s.out, Stderr: s.err,
	}
	e := &engine.Engine{Manifest: m, Stack: dir, Project: p, Runner: runner, Password: password, PasswordFile: passwordPath, Out: s.out}
	if m.IsApp() {
		e.App = m.Name
	}
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

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}
