package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/sshkey"
)

func sshCommand(s *streams) *cobra.Command {
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
			m, _, err := projectStack(cmd.Context(), p, "")
			if err != nil {
				return err
			}
			password, err := p.Password()
			if err != nil {
				return err
			}
			address, user, err := serverLogin(p, m)
			if err != nil {
				return err
			}
			key, err := keyFor(p, m, password)
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
	rotate := &cobra.Command{
		Use:   "rotate [project]",
		Short: "Give the project a new SSH key; the server stops taking the old one at once",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := pickProject(s, firstArg(args))
			if err != nil {
				return err
			}
			return rotateSSHKey(cmd.Context(), s, p)
		},
	}
	rotate.Flags().BoolVar(&s.yes, "yes", false, "go ahead without asking; needed without a terminal")
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

func serverLogin(p *project.Project, m *manifest.Manifest) (string, string, error) {
	if m.Server == nil {
		return "", "", fmt.Errorf("the stack %s of %s has no server", m.Name, p.Meta.Name)
	}
	config, err := p.Config()
	if err != nil {
		return "", "", err
	}
	address, err := manifest.RenderLogin("server.address", m.Server.Address, config, nil)
	if err != nil {
		return "", "", err
	}
	user, err := manifest.RenderLogin("server.ops_user", m.Server.OpsUser, config, nil)
	return address, user, err
}

// takesKey is whether the server of p lets k log in.
func takesKey(ctx context.Context, p *project.Project, m *manifest.Manifest, k *sshkey.Key) bool {
	address, user, err := serverLogin(p, m)
	if err != nil {
		return false
	}
	path := filepath.Join(p.Dir, projectKeyFile+".new")
	if err := k.Write(path); err != nil {
		return false
	}
	defer os.Remove(path)
	defer os.Remove(path + ".pub")
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "ssh", "-F", "/dev/null", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "IdentitiesOnly=yes",
		"-o", sshkey.KnownHostsOption(filepath.Join(p.Dir, project.KnownHosts)), "-o", "StrictHostKeyChecking=accept-new",
		"-i", path, user+"@"+address, "true").Run() == nil
}

// rotateSSHKey authorizes a new key of the project on the server, logging in
// with the old one, so that losing the race leaves the old key working; when
// the server does not take the new key, the old one stays in vault.yml.
func rotateSSHKey(ctx context.Context, s *streams, p *project.Project) error {
	m, dir, err := projectStack(ctx, p, "")
	if err != nil {
		return err
	}
	if m.Server == nil || m.Server.SSHKey == nil {
		return fmt.Errorf("the stack %s of %s keeps no SSH key of its own", m.Name, p.Meta.Name)
	}
	ok, err := s.confirm(fmt.Sprintf("A new SSH key for %s: the server stops taking the old one. Go?", p.Meta.Name), false)
	if err != nil || !ok {
		return err
	}
	password, err := p.Password()
	if err != nil {
		return err
	}
	if _, err := ensureSSHKey(p, m, password); err != nil {
		return err
	}
	e, _, err := newEngine(ctx, s, p, m, dir)
	if err != nil {
		return err
	}
	secrets, err := p.Secrets(password)
	if err != nil {
		return err
	}
	name := m.Server.SSHKey.Secret
	old := secrets[name]
	if e.KeyFile == filepath.Join(p.Dir, projectKeyFile) {
		k, _ := sshkey.FromSecret(old)
		held := filepath.Join(p.Dir, projectKeyFile+".old")
		if err := k.Write(held); err != nil {
			return err
		}
		defer os.Remove(held)
		defer os.Remove(held + ".pub")
		e.KeyFile = held
	}
	k, err := sshkey.New(time.Now())
	if err != nil {
		return err
	}
	if secrets[name], err = k.Secret(); err != nil {
		return err
	}
	if err := p.SaveSecrets(password, secrets); err != nil {
		return err
	}
	start := time.Now()
	err = e.Run(ctx, m.Commands[m.Server.SSHKey.Apply], nil)
	rerr := record(p, "ssh rotate", "", start, err)
	if err != nil {
		if takesKey(ctx, p, m, k) {
			return fmt.Errorf("%w; the server takes the new key already, so it stays in vault.yml, "+
				"and the old one may no longer work: run damstack ssh rotate %s again to finish", err, p.Meta.Name)
		}
		secrets[name] = old
		if serr := p.SaveSecrets(password, secrets); serr != nil {
			return errors.Join(err, serr)
		}
		return fmt.Errorf("%w; the old key stays, nothing changed on this Mac", err)
	}
	if rerr != nil {
		return rerr
	}
	if _, err := keyFor(p, m, password); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "%s has a new SSH key; the server takes it and no longer the old one. Make a new recovery kit: damstack backup kit %s\n",
		p.Meta.Name, p.Meta.Name)
	return nil
}
