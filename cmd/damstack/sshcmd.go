package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/eugene-panin/damstack/internal/ops"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/sshkey"
)

func sshCommand(s *cli) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ssh [project] [-- command]",
		Short: "Open a shell on the server of a project, as its ops user, over its public address, or run a command there",
		Example: "  damstack ssh ovh\n" +
			"  damstack ssh ovh -- timedatectl status",
		Args: func(cmd *cobra.Command, args []string) error {
			return cobra.MaximumNArgs(1)(cmd, beforeDash(cmd, args))
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			remote := args[len(beforeDash(cmd, args)):]
			p, err := pickProject(s, firstArg(beforeDash(cmd, args)))
			if err != nil {
				return err
			}
			m, _, err := ops.ProjectStack(cmd.Context(), p, "")
			if err != nil {
				return err
			}
			password, err := p.Password()
			if err != nil {
				return err
			}
			address, user, err := ops.ServerLogin(p, m)
			if err != nil {
				return err
			}
			key, err := ops.KeyFor(p, m, password)
			if err != nil {
				return err
			}
			ssh := exec.CommandContext(cmd.Context(), "ssh", "-F", "/dev/null", "-o", sshkey.KnownHostsOption(filepath.Join(p.Dir, project.KnownHosts)),
				"-o", "StrictHostKeyChecking=accept-new", "-i", key, user+"@"+address)
			if len(remote) > 0 {
				ssh.Args = append(append(ssh.Args, "--"), remote...)
			}
			ssh.Stdin, ssh.Stdout, ssh.Stderr = os.Stdin, os.Stdout, os.Stderr
			err = ssh.Run()
			var exit *exec.ExitError
			switch {
			case !errors.As(err, &exit):
				return err
			case len(remote) > 0, exit.ExitCode() == 255:
				return exitStatus(exit.ExitCode())
			}
			return nil
		},
	}
	var yes bool
	rotate := &cobra.Command{
		Use:   "rotate [project]",
		Short: "Give the project a new SSH key; the server stops taking the old one at once",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := pickProject(s, firstArg(args))
			if err != nil {
				return err
			}
			return ops.RotateSSHKey(cmd.Context(), s.ui(yes), yes, p)
		},
	}
	rotate.Flags().BoolVar(&yes, "yes", false, "go ahead without asking; needed without a terminal")
	cmd.AddCommand(rotate)
	return cmd
}

// beforeDash is the arguments of cmd before --, all of them without it.
func beforeDash(cmd *cobra.Command, args []string) []string {
	if n := cmd.ArgsLenAtDash(); n >= 0 {
		return args[:n]
	}
	return args
}
