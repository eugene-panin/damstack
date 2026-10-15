package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/sshkey"
)

// ServerLogin is the public address of the server of p and the ops user
// damstack logs in there as.
func ServerLogin(p *project.Project, m *manifest.Manifest) (string, string, error) {
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

// serverTakesKey is takesKey; tests answer in its place.
var serverTakesKey = takesKey

// takesKey is whether the server of p lets k log in.
func takesKey(ctx context.Context, p *project.Project, m *manifest.Manifest, k *sshkey.Key) bool {
	address, user, err := ServerLogin(p, m)
	if err != nil {
		return false
	}
	path := filepath.Join(p.Dir, ProjectKeyFile+".new")
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

// RotateSSHKey authorizes a new key of the project on the server, logging in
// with the old one, so that losing the race leaves the old key working; when
// the server does not take the new key, the old one stays in vault.yml. With
// yes it goes ahead without asking.
func RotateSSHKey(ctx context.Context, ui UI, yes bool, p *project.Project) error {
	m, dir, err := ProjectStack(ctx, p, "")
	if err != nil {
		return err
	}
	if m.Server == nil || m.Server.SSHKey == nil {
		return fmt.Errorf("the stack %s of %s keeps no SSH key of its own", m.Name, p.Meta.Name)
	}
	ok, err := ui.Confirm(fmt.Sprintf("A new SSH key for %s: the server stops taking the old one. Go?", p.Meta.Name), false)
	if err != nil || !ok {
		return err
	}
	password, err := p.Password()
	if err != nil {
		return err
	}
	if _, err := EnsureSSHKey(p, m, password); err != nil {
		return err
	}
	e, _, err := newEngine(ctx, ui, yes, p, m, dir)
	if err != nil {
		return err
	}
	secrets, err := p.Secrets(password)
	if err != nil {
		return err
	}
	name := m.Server.SSHKey.Secret
	old := secrets[name]
	if e.KeyFile == filepath.Join(p.Dir, ProjectKeyFile) {
		k, _ := sshkey.FromSecret(old)
		held := filepath.Join(p.Dir, ProjectKeyFile+".old")
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
	rerr := Record(p, "ssh rotate", "", start, err)
	if err != nil {
		if serverTakesKey(ctx, p, m, k) {
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
	if _, err := KeyFor(p, m, password); err != nil {
		return err
	}
	fmt.Fprintf(ui.Out(), "%s has a new SSH key; the server takes it and no longer the old one. Make a new recovery kit: damstack backup kit %s\n",
		p.Meta.Name, p.Meta.Name)
	return nil
}
