package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/eugene-panin/hashistack/internal/doctor"
	"github.com/eugene-panin/hashistack/internal/project"
	"github.com/eugene-panin/hashistack/internal/release"
)

var errProblems = errors.New("hashistack cannot work until the problems above are fixed")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, errProblems) {
			fmt.Fprintln(os.Stderr, "hashistack:", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	root := &cobra.Command{
		Use:           "hashistack",
		Short:         "Run your own server as a small private cloud, from one config file",
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
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	root.AddCommand(&cobra.Command{
		Use:   "doctor",
		Short: "Check that this machine has what hashistack needs, and say how to get what it lacks",
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
			fmt.Fprintf(stdout, "hashistack %s, tools image %s\n", release.Version, release.ImageRef())
		},
	})
	return root.ExecuteContext(ctx)
}

func runDoctor(ctx context.Context, w io.Writer) error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	p, err := project.Find(wd)
	if err != nil {
		return err
	}
	if doctor.Print(w, doctor.Run(ctx, doctor.Host(p))) {
		return errProblems
	}
	markChecked()
	return nil
}

func markerPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "hashistack", "checked-"+release.Version)
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
