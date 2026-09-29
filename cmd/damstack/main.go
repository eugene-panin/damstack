package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/doctor"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/release"
)

var errProblems = errors.New("damstack cannot work until the problems above are fixed")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, errProblems) {
			fmt.Fprintln(os.Stderr, "damstack:", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	root := &cobra.Command{
		Use:           "damstack",
		Short:         "Deploy and run infrastructure stacks from one config file, with nothing but Docker installed",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !checkedBefore() {
				fmt.Fprintln(stdout, "First run: checking this machine.")
				fmt.Fprintln(stdout)
				if err := runDoctor(cmd.Context(), stdout); err != nil {
					return err
				}
				fmt.Fprintln(stdout)
			}
			return cmd.Help()
		},
	}
	s := newStreams(stdin, stdout, stderr)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	root.AddCommand(&cobra.Command{
		Use:   "doctor",
		Short: "Check that this machine has what damstack needs, and say how to get what it lacks",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctor(cmd.Context(), stdout)
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version and the tools image it uses",
		Args:  cobra.NoArgs,
		Run: func(*cobra.Command, []string) {
			fmt.Fprintf(stdout, "damstack %s, tools image %s\n", release.Version, release.ImageRef())
		},
	})
	stack := &cobra.Command{
		Use:   "stack",
		Short: "Work on a stack of your own",
	}
	stack.AddCommand(&cobra.Command{
		Use:   "lint [dir]",
		Short: "Check the damstack.yaml of a stack, the current directory by default",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			m, err := manifest.Load(dir, release.Version)
			if err != nil {
				return err
			}
			if m.IsApp() {
				fmt.Fprintf(stdout, "%s: app, %s, needs %s, %d questions, %d secrets, targets %s\n",
					m.Name, manifest.APIVersion, strings.Join(m.Requires.Provides, ", "), len(m.Questions), len(m.Secrets),
					strings.Join(slices.Sorted(maps.Keys(m.Targets)), ", "))
				return nil
			}
			fmt.Fprintf(stdout, "%s: %s, %d questions, %d secrets, %d steps, %d commands\n",
				m.Name, manifest.APIVersion, len(m.Questions), len(m.Secrets), len(m.Steps), len(m.Commands))
			return nil
		},
	})
	stack.AddCommand(&cobra.Command{
		Use:   "check [dir]",
		Short: "Prove a stack without a server: set up a project from its test answers, check its playbooks, OpenTofu and policies",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return checkStack(cmd.Context(), s, dir)
		},
	})
	root.AddCommand(stack, stacksCommand(stdout), addCommand(stdin, stdout), deployCommand(s), statusCommand(s), historyCommand(s), appCommand(s))
	root.InitDefaultCompletionCmd()
	root.AddCommand(stackCommands(s, func(name string) bool {
		c, _, err := root.Find([]string{name})
		return err == nil && c != root
	})...)
	return root.ExecuteContext(ctx)
}

func runDoctor(ctx context.Context, w io.Writer) error {
	var dp *doctor.Project
	if p, err := currentProject(); err == nil {
		password, err := p.PasswordPath()
		if err != nil {
			return err
		}
		dp = &doctor.Project{Dir: p.Dir, Password: password}
		if m, _ := cachedStack(p); m != nil && m.Server != nil && m.Server.Tunnel != "" {
			if config, err := p.Config(); err == nil {
				dp.Tunnel, _ = manifest.Render("server.tunnel", m.Server.Tunnel, config, nil)
				dp.Public, _ = manifest.Render("server.address", m.Server.Address, config, nil)
				dp.KnownHosts = filepath.Join(p.Dir, project.KnownHosts)
			}
		}
	} else if !errors.Is(err, errNoProject) {
		return err
	}
	if doctor.Print(w, doctor.Run(ctx, doctor.Host(dp))) {
		return errProblems
	}
	markChecked()
	return nil
}

func markerPath() string {
	dir, err := config.Dir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "checked-"+release.Version)
}

func checkedBefore() bool {
	path := markerPath()
	if path == "" {
		return true
	}
	_, err := os.Stat(path)
	return err == nil
}

func markChecked() {
	path := markerPath()
	if path == "" {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o755) == nil {
		_ = os.WriteFile(path, nil, 0o644)
	}
}
