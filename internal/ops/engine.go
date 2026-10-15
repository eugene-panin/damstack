package ops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/doctor"
	"github.com/eugene-panin/damstack/internal/engine"
	"github.com/eugene-panin/damstack/internal/event"
	"github.com/eugene-panin/damstack/internal/login"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/release"
	"github.com/eugene-panin/damstack/internal/sshkey"
	"github.com/eugene-panin/damstack/internal/toolbox"
)

// ProjectKeyFile is where the SSH key of a project is written for ssh to
// read, inside the work directory, which no kit or git keeps.
const ProjectKeyFile = ".damstack/work/ssh/project-key"

func sshKey() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	for _, name := range doctor.KeyNames {
		key := filepath.Join(home, ".ssh", name)
		if _, err := os.Stat(key + ".pub"); err != nil {
			continue
		}
		if _, err := os.Stat(key); err == nil {
			return key, nil
		}
	}
	return "", errors.New("no SSH key in ~/.ssh; damstack doctor says how to make one")
}

// KeyFor is the private SSH key damstack logs in to the server of p with:
// yours in ~/.ssh when there is one, else the key of the project.
func KeyFor(p *project.Project, m *manifest.Manifest, password string) (string, error) {
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
	path := filepath.Join(p.Dir, ProjectKeyFile)
	return path, k.Write(path)
}

// EnsureSSHKey makes the SSH key of a project when it has none yet.
func EnsureSSHKey(p *project.Project, m *manifest.Manifest, password string) (bool, error) {
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

// NewEngine is the engine that runs the steps of the stack m for p, and the
// SSH key it logs in with. Its events go to ui as they come; with yes it
// applies every plan, replacing allocations too, without asking.
func NewEngine(ctx context.Context, ui UI, yes bool, p *project.Project, m *manifest.Manifest, dir string) (*engine.Engine, string, error) {
	password, err := p.Password()
	if err != nil {
		return nil, "", err
	}
	passwordPath, err := p.PasswordPath()
	if err != nil {
		return nil, "", err
	}
	key, err := KeyFor(p, m, password)
	if err != nil {
		return nil, "", err
	}
	public, err := os.ReadFile(key + ".pub")
	if err != nil {
		return nil, "", err
	}
	tb, cache, err := FetchToolbox(ctx, ui.Err(), m)
	if err != nil {
		return nil, "", err
	}
	stdin := ui.Terminal()
	runner := &toolbox.Runner{
		Toolbox: tb, Stack: dir, Project: p.Dir, Password: passwordPath, Cache: cache,
		PublicKey: strings.TrimSpace(string(public)), TTY: stdin != nil, Stdin: stdin, Stdout: ui.Out(), Stderr: ui.Err(),
	}
	if err := sshAccess(ctx, runner, key); err != nil {
		return nil, "", err
	}
	return &engine.Engine{
		Manifest: m, Stack: dir, Project: p, Runner: runner, Password: password, PasswordFile: passwordPath, KeyFile: runner.Key,
		Color:  stdin != nil,
		Events: event.SinkFunc(ui.Emit),
		Approve: func(plan event.Plan) (event.Approval, error) {
			if yes {
				return event.Approval{Apply: true, Restart: true}, nil
			}
			return ui.Approve(plan)
		},
	}, key, nil
}

// FetchToolbox is the toolbox the stack m runs with, fetched the first
// time, and the cache it is in; w hears when it is fetched.
func FetchToolbox(ctx context.Context, w io.Writer, m *manifest.Manifest) (string, string, error) {
	if m != nil && m.Requires.Toolbox != "" {
		if ok, err := manifest.Satisfies(release.Toolbox, m.Requires.Toolbox); err != nil || !ok {
			return "", "", fmt.Errorf("the stack needs the toolbox %s, this damstack has %s; update damstack", m.Requires.Toolbox, release.Toolbox)
		}
	}
	cache, err := config.CacheDir()
	if err != nil {
		return "", "", err
	}
	if !toolbox.Have(cache, release.Toolbox) {
		fmt.Fprintf(w, "Fetching the toolbox %s, about 90 MB, once.\n", release.Toolbox)
	}
	tb, err := toolbox.Fetch(ctx, cache, release.Toolbox, release.ToolboxSHA256)
	return tb, cache, err
}

func ensureLogin(ctx context.Context, ui UI, yes bool, e *engine.Engine, key string) error {
	config, err := e.Project.Config()
	if err != nil {
		return err
	}
	secrets, err := e.Project.Secrets(e.Password)
	if err != nil {
		return err
	}
	var server login.Server
	for _, f := range []struct {
		name, text string
		into       *string
	}{
		{"server.address", e.Manifest.Server.Address, &server.Address},
		{"server.first_user", e.Manifest.Server.FirstUser, &server.FirstUser},
		{"server.ops_user", e.Manifest.Server.OpsUser, &server.OpsUser},
	} {
		if *f.into, err = manifest.RenderLogin(f.name, f.text, config, secrets); err != nil {
			return fmt.Errorf("%s of the stack: %w", f.name, err)
		}
	}
	if server.HostKey, err = manifest.Render("server.host_key", e.Manifest.Server.HostKey, config, secrets); err != nil {
		return fmt.Errorf("server.host_key of the stack: %w", err)
	}
	server.HostKey = strings.TrimSpace(server.HostKey)
	l := login.New(key, filepath.Join(e.Project.Dir, project.KnownHosts), ui.Out())
	if ui.Terminal() != nil && !yes {
		l.Confirm = func(question string) (bool, error) { return ui.Confirm(question, false) }
	}
	return l.Ensure(ctx, server)
}
