package main

import (
	"bufio"
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
		RunE: func(*cobra.Command, []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tFROM\tREPOSITORY")
			for _, s := range cfg.AllStacks() {
				from := "added"
				if s.Builtin {
					from = "built in"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", s.Name, from, s.URL)
			}
			return w.Flush()
		},
	}
}

func addCommand(stdin io.Reader, stdout io.Writer) *cobra.Command {
	var name string
	var yes bool
	cmd := &cobra.Command{
		Use:   "add <repository>",
		Short: "Add a stack from its git repository: owner/name for github.com/owner/damstack-name, or any address",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return addStack(cmd.Context(), stdin, stdout, args[0], name, yes)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "name to keep the stack under, instead of the one its damstack.yaml gives")
	cmd.Flags().BoolVar(&yes, "yes", false, "add without asking")
	return cmd
}

func addStack(ctx context.Context, stdin io.Reader, stdout io.Writer, raw, name string, yes bool) error {
	url, err := stack.NormalizeURL(raw)
	if err != nil {
		return err
	}
	cache, err := config.CacheDir()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Looking at %s\n", url)
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

	fmt.Fprintf(stdout, "\n%s %s, commit %.12s\n  %s\n", m.Name, r.Tag, r.Commit, m.Description)
	if len(m.Requires.Host) > 0 {
		fmt.Fprintf(stdout, "  needs on this machine: %s\n", strings.Join(m.Requires.Host, ", "))
	}
	fmt.Fprintf(stdout, "  steps: %s\n", stepNames(m))
	fmt.Fprintln(stdout, "\nA stack runs with your SSH key and the secrets of the projects you deploy with it.")
	fmt.Fprintln(stdout, "Add only a stack you trust, from people you trust.")

	if !yes && !confirm(stdin, stdout, fmt.Sprintf("Add it as %s?", name)) {
		return fmt.Errorf("not added")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	added, err := cfg.AddStack(config.Stack{Name: name, URL: url})
	if err != nil {
		return err
	}
	if !added {
		fmt.Fprintf(stdout, "%s is already added.\n", name)
		return nil
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Added %s. damstack deploy %s deploys it.\n", name, name)
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

func confirm(stdin io.Reader, stdout io.Writer, question string) bool {
	fmt.Fprintf(stdout, "%s [y/N] ", question)
	answer, _ := bufio.NewReader(stdin).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}
