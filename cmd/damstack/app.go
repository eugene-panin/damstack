package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/release"
	"github.com/eugene-panin/damstack/internal/setup"
	"github.com/eugene-panin/damstack/internal/stack"
)

func appCommand(s *streams) *cobra.Command {
	app := &cobra.Command{
		Use:   "app",
		Short: "Add apps to the project you are in, and list them",
	}
	var from, answers string
	var yes, asJSON bool
	add := &cobra.Command{
		Use:   "add <app>",
		Short: "Add an app: a name damstack stacks lists, owner/name for github.com/owner/damstack-name, or any address",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && from == "" {
				return asUsage(cmd, fmt.Errorf("requires at least 1 arg(s): name the app, or give --from"))
			}
			arg := ""
			if len(args) == 1 {
				arg = args[0]
			}
			s.yes = yes
			return addApp(cmd.Context(), s, arg, from, answers)
		},
	}
	add.Flags().StringVar(&from, "from", "", "add the app in this directory as it is, instead of a release; for writing an app")
	add.Flags().StringVar(&answers, "answers", "", "a YAML file with answers to the questions, and the secrets the app asks for")
	add.Flags().BoolVar(&yes, "yes", false, "deploy the project right after, without asking")
	list := &cobra.Command{
		Use:   "list",
		Short: "List the apps of the project you are in",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			p, err := pickProject(s, "")
			if err != nil {
				return err
			}
			if asJSON {
				apps := make([]stackJSON, 0, len(p.Meta.Apps))
				for _, a := range p.Meta.Apps {
					apps = append(apps, newStackJSON(a))
				}
				return writeJSON(s.out, apps)
			}
			w := tabwriter.NewWriter(s.out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "APP\tRELEASE\tFROM")
			for _, a := range p.Meta.Apps {
				fmt.Fprintf(w, "%s\t%s\t%s\n", a.Name, a.Tag, a.URL)
			}
			return w.Flush()
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "print the apps as JSON: name, tag, commit, url")
	app.AddCommand(add, list)
	return app
}

func addApp(ctx context.Context, s *streams, arg, from, answers string) error {
	p, err := pickProject(s, "")
	if err != nil {
		return err
	}
	pm, pdir, err := projectStack(ctx, p, "")
	if err != nil {
		return err
	}
	am, adir, ref, err := findApp(ctx, s, arg, from)
	if err != nil {
		return err
	}
	if !am.IsApp() {
		return fmt.Errorf("%s is a platform, not an app; damstack deploy sets up a project on it", am.Name)
	}
	for _, need := range am.Requires.Provides {
		if !slices.Contains(pm.Provides, need) {
			return fmt.Errorf("%s needs %s, which the platform %s of this project does not provide", am.Name, need, pm.Name)
		}
	}
	if _, _, ok := am.Target(pm.Provides); !ok {
		return fmt.Errorf("%s has no way to run on %s, which provides %s", am.Name, pm.Name, strings.Join(pm.Provides, ", "))
	}
	password, err := p.Password()
	if err != nil {
		return err
	}
	given := map[string]any{}
	if answers != "" {
		data, err := os.ReadFile(answers)
		if err != nil {
			return err
		}
		if err := yaml.Unmarshal(data, &given); err != nil {
			return fmt.Errorf("%s: %w", answers, err)
		}
	}
	err = setup.AddApp(setup.AppOptions{Manifest: am, Stack: adir, Ref: ref, Project: p, Password: password, Given: given, Prompter: s.prompt})
	if err != nil {
		return err
	}
	fmt.Fprintf(s.out, "\nAdded %s to %s: its settings are under apps.%s in stack.yaml.\n\n", am.Name, p.Meta.Name, am.Name)
	// Without a terminal and --yes, adding is all that was asked for.
	ok := s.yes
	if !ok && !s.prompt.NoTerminal {
		if ok, err = s.prompt.Confirm("Deploy it now?", true); err != nil {
			return err
		}
	}
	if !ok {
		fmt.Fprintf(s.out, "Edit stack.yaml if you want, then run damstack deploy in %s.\n", p.Dir)
		return nil
	}
	return runSteps(ctx, s, p, pm, pdir)
}

// findApp fetches an app by what a person names it: a stack damstack knows,
// an address, or a directory.
func findApp(ctx context.Context, s *streams, arg, from string) (*manifest.Manifest, string, project.StackRef, error) {
	if from != "" {
		dir, err := filepath.Abs(from)
		if err != nil {
			return nil, "", project.StackRef{}, err
		}
		m, err := manifest.Load(dir, release.Version)
		if err != nil {
			return nil, "", project.StackRef{}, err
		}
		return m, dir, project.StackRef{Name: m.Name, URL: "file://" + dir, Tag: devTag}, nil
	}
	cfg, err := loadConfig(ctx)
	if err != nil {
		return nil, "", project.StackRef{}, err
	}
	url := ""
	if st, ok := cfg.Stack(arg); ok {
		url = st.URL
	} else if url, err = stack.NormalizeURL(arg); err != nil {
		return nil, "", project.StackRef{}, fmt.Errorf("no app named %s; damstack stacks lists them, or give owner/name", arg)
	}
	cache, err := config.CacheDir()
	if err != nil {
		return nil, "", project.StackRef{}, err
	}
	fmt.Fprintf(s.out, "Looking at %s\n", url)
	r, err := stack.Latest(ctx, url)
	if err != nil {
		return nil, "", project.StackRef{}, err
	}
	dir, err := stack.Fetch(ctx, cache, url, r)
	if err != nil {
		return nil, "", project.StackRef{}, err
	}
	m, err := manifest.Load(dir, release.Version)
	if err != nil {
		return nil, "", project.StackRef{}, err
	}
	fmt.Fprintf(s.out, "%s %s: %s\n\n", m.Name, r.Tag, m.Description)
	return m, dir, project.StackRef{Name: m.Name, URL: url, Tag: r.Tag, Commit: r.Commit}, nil
}
