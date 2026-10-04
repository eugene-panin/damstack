package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/release"
	"github.com/eugene-panin/damstack/internal/stack"
)

func stacksCommand(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "stacks",
		Short: "List the stacks damstack can deploy",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfig(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tKIND\tFROM\tREPOSITORY")
			for _, s := range cfg.AllStacks() {
				from, kind := "added", "platform"
				if s.Builtin {
					from = "library"
				}
				if s.Kind == manifest.KindApp {
					kind = "app"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, kind, from, s.URL)
			}
			return w.Flush()
		},
	}
}

func addCommand(s *streams) *cobra.Command {
	var name string
	var yes bool
	cmd := &cobra.Command{
		Use:   "add <repository>",
		Short: "Add a stack from its git repository: owner/name for github.com/owner/damstack-name, or any address",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s.yes = yes
			return addStack(cmd.Context(), s, args[0], name)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "name to keep the stack under, instead of the one its damstack.yaml gives")
	cmd.Flags().BoolVar(&yes, "yes", false, "add without asking")
	return cmd
}

func addStack(ctx context.Context, s *streams, raw, name string) error {
	url, err := stack.NormalizeURL(raw)
	if err != nil {
		return err
	}
	cache, err := config.CacheDir()
	if err != nil {
		return err
	}
	fmt.Fprintf(s.out, "Looking at %s\n", url)
	r, err := stack.Latest(ctx, url)
	if err != nil {
		return err
	}
	dir, err := stack.Fetch(ctx, cache, url, r)
	if err != nil {
		return err
	}
	m, err := manifest.Load(dir, release.Version)
	if err != nil {
		return err
	}
	if name == "" {
		name = m.Name
	}

	fmt.Fprintf(s.out, "\n%s %s, commit %.12s\n  %s\n", m.Name, r.Tag, r.Commit, m.Description)
	if len(m.Requires.Host) > 0 {
		fmt.Fprintf(s.out, "  needs on this machine: %s\n", strings.Join(m.Requires.Host, ", "))
	}
	fmt.Fprintf(s.out, "  steps: %s\n", stepNames(m))
	fmt.Fprintln(s.out, "\nA stack runs with your SSH key and the secrets of the projects you deploy with it.")
	fmt.Fprintln(s.out, "Add only a stack you trust, from people you trust.")

	ok, err := s.confirm(fmt.Sprintf("Add it as %s?", name), false)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("not added")
	}
	cfg, err := loadConfig(ctx)
	if err != nil {
		return err
	}
	kind := ""
	if m.IsApp() {
		kind = manifest.KindApp
	}
	added, err := cfg.AddStack(config.Stack{Name: name, URL: url, Kind: kind})
	if err != nil {
		return err
	}
	if !added {
		fmt.Fprintf(s.out, "%s is already added.\n", name)
		return nil
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	if m.IsApp() {
		fmt.Fprintf(s.out, "Added %s. damstack app add %s adds it to the project you are in.\n", name, name)
		return nil
	}
	fmt.Fprintf(s.out, "Added %s. damstack deploy %s sets up a project on it.\n", name, name)
	return nil
}

func stepNames(m *manifest.Manifest) string {
	names := make([]string, len(m.Steps))
	for i, s := range m.Steps {
		names[i] = s.Name
		if s.Confirm {
			names[i] += " (asks first)"
		}
	}
	return strings.Join(names, ", ")
}
