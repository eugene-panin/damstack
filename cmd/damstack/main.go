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
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/doctor"
	"github.com/eugene-panin/damstack/internal/library"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/release"
)

var errProblems = errors.New("damstack cannot work until the problems above are fixed")

// Exit statuses, part of the interface: scripts tell these apart.
const (
	exitFailure   = 1
	exitUsage     = 2   // a malformed command line
	exitInterrupt = 130 // stopped with Ctrl-C
)

func main() {
	ctx, stop := interruptible(os.Stderr)
	code := report(ctx, run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr), os.Stderr)
	stop()
	os.Exit(code)
}

// interruptible is cancelled by the first Ctrl-C; the second quits at once,
// without waiting for the tools to stop.
func interruptible(stderr io.Writer) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		if _, ok := <-sigs; !ok {
			return
		}
		cancel()
		// A question ends at once; only stopping that takes a while, such as
		// of a tool, needs saying.
		select {
		case _, ok := <-sigs:
			if ok {
				os.Exit(exitInterrupt)
			}
			return
		case <-time.After(300 * time.Millisecond):
		}
		fmt.Fprintln(stderr, "\ndamstack: stopping; press Ctrl-C again to quit at once")
		if _, ok := <-sigs; ok {
			os.Exit(exitInterrupt)
		}
	}()
	return ctx, func() { signal.Stop(sigs); close(sigs); cancel() }
}

// usageError is a malformed command line, with what to read about it.
type usageError struct {
	err  error
	help string
}

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

// report prints err, once, and returns the exit status for it.
func report(ctx context.Context, err error, stderr io.Writer) int {
	var ue *usageError
	switch {
	case err == nil:
		return 0
	case ctx.Err() != nil:
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(stderr) // end the line the ^C was typed on
		} else {
			fmt.Fprintln(stderr, "damstack:", err)
		}
		return exitInterrupt
	case errors.As(err, &ue):
		fmt.Fprintln(stderr, "damstack:", ue.err)
		fmt.Fprintln(stderr, ue.help)
		return exitUsage
	case errors.Is(err, errProblems):
		return exitFailure
	}
	fmt.Fprintln(stderr, "damstack:", err)
	return exitFailure
}

// asUsage makes a command line error of cobra's a usageError. Run without the
// arguments it needs, a command shows what it does, its usage and examples.
func asUsage(c *cobra.Command, err error) error {
	msg := err.Error()
	if !slices.ContainsFunc([]string{"unknown command", "unknown flag", "unknown shorthand", "accepts ", "requires ", "invalid argument", "flag needs an argument"},
		func(prefix string) bool { return strings.HasPrefix(msg, prefix) }) {
		return err
	}
	more := fmt.Sprintf("Run '%s --help' for usage.", c.CommandPath())
	if strings.HasSuffix(msg, "received 0") || strings.HasPrefix(msg, "requires at least") {
		var b strings.Builder
		fmt.Fprintf(&b, "\n%s\n\nUsage:\n  %s\n", c.Short, c.UseLine())
		if c.Example != "" {
			fmt.Fprintf(&b, "\nExamples:\n%s\n", c.Example)
		}
		fmt.Fprintf(&b, "\nRun '%s --help' for all flags.", c.CommandPath())
		more = b.String()
	}
	return &usageError{err: err, help: more}
}

// helpWins shows the help when -h or --help is on the line, even next to a
// flag cobra does not know.
func helpWins(args []string) func(*cobra.Command, error) error {
	return func(c *cobra.Command, err error) error {
		for _, arg := range args {
			if arg == "--" {
				break
			}
			if arg == "-h" || arg == "--help" {
				return pflag.ErrHelp
			}
		}
		return err
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	root := &cobra.Command{
		Use:   "damstack",
		Short: "Deploy and run infrastructure stacks from one config file, on a Mac, with nothing else installed",
		Long: `damstack sets up your own server and runs apps on it, from one file you edit.

Docs:   https://github.com/eugene-panin/damstack
Issues: https://github.com/eugene-panin/damstack/issues`,
		Example: `  damstack                  check this machine, then show the projects
  damstack deploy hashi     set up a new project on the hashi platform
  damstack status           how each step of the current project went last
  damstack app add mail     add an app to the project you are in`,
		Version:       release.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return home(cmd.Context(), stdout)
		},
	}
	s := newStreams(ctx, stdin, stdout, stderr)
	root.SetArgs(takeProjectFlag(args))
	root.PersistentFlags().StringP("project", "p", "", "the project to work on, instead of the one of the directory or the current one")
	// Declared before cobra adds its own, so --version gets no -v shorthand: -v is --verbose.
	root.Flags().Bool("version", false, "print the version and the toolbox it uses")
	root.SetVersionTemplate(fmt.Sprintf("damstack %s, toolbox %s\n", release.Version, release.Toolbox))
	root.SetUsageTemplate(usageTemplate)
	root.SetFlagErrorFunc(helpWins(args))
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
		Short: "Print the version and the toolbox it uses",
		Args:  cobra.NoArgs,
		Run: func(*cobra.Command, []string) {
			fmt.Fprintf(stdout, "damstack %s, toolbox %s\n", release.Version, release.Toolbox)
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
	root.AddCommand(stack, stacksCommand(stdout), addCommand(s), deployCommand(s), statusCommand(s), historyCommand(s), appCommand(s), tokenCommand(s), useCommand(s), editCommand(s), backupCommand(s), tunnelCommand(s), sshCommand(s))
	root.InitDefaultCompletionCmd()
	root.AddCommand(stackCommands(s, func(name string) bool {
		c, _, err := root.Find([]string{name})
		return err == nil && c != root
	})...)
	describe(root, s)
	c, err := root.ExecuteContextC(ctx)
	if err != nil {
		return asUsage(c, err)
	}
	return nil
}

func runDoctor(ctx context.Context, w io.Writer) error {
	var dp *doctor.Project
	if p, _, err := resolveProject(""); err == nil && p != nil {
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
	} else if err != nil {
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

// loadConfig is the config of damstack with the library fetched.
func loadConfig(ctx context.Context) (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if l, err := library.Default(); err == nil {
		cfg.Library = l.Get(ctx)
	}
	return cfg, nil
}
