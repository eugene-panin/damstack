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

// projectKeyFile is where the SSH key of a project is written for ssh to
// read, inside the work directory, which no kit or git keeps.
const projectKeyFile = ".damstack/work/ssh/project-key"

// keyFor is the private SSH key damstack logs in to the server of p with:
// yours in ~/.ssh when there is one, else the key of the project.
func keyFor(p *project.Project, m *manifest.Manifest, password string) (string, error) {
	key, err := sshKey()
	if err == nil {
		return key, nil
	}
	if m == nil || m.Server == nil || m.Server.SSHKey == nil {
		return "", err
	}
	secrets, serr := p.Secrets(password)
	if serr != nil {
		return "", serr
	}
	k, serr := sshkey.FromSecret(secrets[m.Server.SSHKey.Secret])
	if serr != nil || k == nil {
		return "", err
	}
	path := filepath.Join(p.Dir, projectKeyFile)
	return path, k.Write(path)
}

// ensureSSHKey makes the SSH key of a project when it has none yet.
func ensureSSHKey(p *project.Project, m *manifest.Manifest, password string) (bool, error) {
	if m.Server == nil || m.Server.SSHKey == nil {
		return false, nil
	}
	secrets, err := p.Secrets(password)
	if err != nil {
		return false, err
	}
	k, err := sshkey.FromSecret(secrets[m.Server.SSHKey.Secret])
	if err != nil || k != nil {
		return false, err
	}
	if k, err = sshkey.New(time.Now()); err != nil {
		return false, err
	}
	v, err := k.Secret()
	if err != nil {
		return false, err
	}
	secrets[m.Server.SSHKey.Secret] = v
	return true, p.SaveSecrets(password, secrets)
}

func sshCommand(s *streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ssh [project]",
		Short: "Open a shell on the server of a project, as its ops user, over its public address",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := pickProject(s, firstArg(args))
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
			ssh := exec.CommandContext(cmd.Context(), "ssh", "-F", "/dev/null", "-o", "UserKnownHostsFile="+filepath.Join(p.Dir, project.KnownHosts),
				"-o", "StrictHostKeyChecking=accept-new", "-i", key, user+"@"+address)
			ssh.Stdin, ssh.Stdout, ssh.Stderr = os.Stdin, os.Stdout, os.Stderr
			err = ssh.Run()
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return nil
			}
			return err
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

func serverLogin(p *project.Project, m *manifest.Manifest) (string, string, error) {
	if m.Server == nil {
		return "", "", fmt.Errorf("the stack %s of %s has no server", m.Name, p.Meta.Name)
	}
	config, err := p.Config()
	if err != nil {
		return "", "", err
	}
	address, err := manifest.Render("server.address", m.Server.Address, config, nil)
	if err != nil {
		return "", "", err
	}
	user, err := manifest.Render("server.ops_user", m.Server.OpsUser, config, nil)
	return address, user, err
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
	if rerr := record(p, "ssh rotate", "", start, err); rerr != nil && err == nil {
		err = rerr
	}
	if err != nil {
		secrets[name] = old
		if serr := p.SaveSecrets(password, secrets); serr != nil {
			return errors.Join(err, serr)
		}
		return fmt.Errorf("%w; the old key stays, nothing changed on this Mac", err)
	}
	if _, err := keyFor(p, m, password); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "%s has a new SSH key; the server takes it and no longer the old one. Make a new recovery kit: damstack backup kit %s\n",
		p.Meta.Name, p.Meta.Name)
	return nil
}
