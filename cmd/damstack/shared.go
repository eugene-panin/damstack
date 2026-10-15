package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/eugene-panin/damstack/internal/event"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/remote"
)

// sharing is how a command shares a project on its platform: the lock it
// holds while it changes the project, and the store the OpenTofu state is
// pulled from and pushed to. A nil sharing is a project kept on this machine
// only; its methods do nothing then. local is the lock of the project on this
// computer, which every command that changes it holds, shared or not.
type sharing struct {
	held  *remote.Held
	store *remote.Store
	local *os.File
}

// platformRemote is the platform's Consul a project's state is shared on, from
// its app_env; nil when the platform gives none.
func platformRemote(p *project.Project, m *manifest.Manifest, dir, password string) (*remote.Remote, error) {
	if _, ok := m.AppEnv["CONSUL_HTTP_ADDR"]; !ok {
		return nil, nil
	}
	env, err := appEnv(p, m, dir, password)
	if err != nil {
		return nil, err
	}
	return remote.Open(p.Meta.Name, env)
}

// me is who holds the lock, as the others see it.
func me(action string) remote.Holder {
	name := "someone"
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	host, _ := os.Hostname()
	return remote.Holder{Person: name, Host: strings.TrimSuffix(host, ".local"), Action: action, Since: time.Now()}
}

// lockProject takes the locks of a project before a command changes it: the
// one on this computer, so that two commands here do not change it at once,
// then that of a shared project on its platform, waiting in turn behind those
// who asked first; --break-glass goes on without the platform's.
func lockProject(ctx context.Context, s *streams, p *project.Project, m *manifest.Manifest, dir, password, action string) (sh *sharing, err error) {
	local, err := lockHere(p)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			local.Close()
		}
	}()
	if !p.Meta.Shared {
		return &sharing{local: local}, nil
	}
	if err := requireShared(p); err != nil {
		return nil, err
	}
	if s.breakGlass {
		fmt.Fprintf(s.err, "--break-glass: changing %s without its lock, from the state on this machine. "+
			"Anyone else changing it now races this.\n", p.Meta.Name)
		return &sharing{local: local}, nil
	}
	r, err := platformRemote(p, m, dir, password)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return &sharing{local: local}, nil
	}
	if m.Server != nil {
		if err := waitTunnel(ctx, s, p, m.Server); err != nil {
			return nil, err
		}
	}
	held, err := r.Lock(ctx, me(action), func(w remote.Waiting) {
		waiting := event.Waiting{Ahead: w.Position}
		if w.Holder != nil {
			waiting.Holder = w.Holder.String()
		}
		s.events.Emit(waiting)
	})
	if errors.Is(err, remote.ErrUnreachable) {
		return nil, fmt.Errorf("%w, and %s is shared there: nothing was changed. Bring the platform back, "+
			"or, if it is the platform that needs repair, run the same command with --break-glass", err, p.Meta.Name)
	}
	if err != nil {
		return nil, err
	}
	return &sharing{held: held, store: remote.NewStore(r, held), local: local}, nil
}

// lockHere takes the lock of a project on this computer; it goes with the
// process, also when it is killed.
func lockHere(p *project.Project) (*os.File, error) {
	work, err := p.Work()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(work, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another damstack is changing %s on this computer now; once it is done, run this again", p.Meta.Name)
		}
		return nil, fmt.Errorf("locking %s on this computer: %w", p.Meta.Name, err)
	}
	return f, nil
}

// readProject is the state store of a shared project for a command that only
// reads it: the platform's state, fresh, with no lock. When the platform does
// not answer it says so and leaves the state on this machine to be used.
func readProject(s *streams, p *project.Project, m *manifest.Manifest, dir, password string) *sharing {
	if !p.Meta.Shared {
		return nil
	}
	r, err := platformRemote(p, m, dir, password)
	if err == nil && r != nil {
		err = r.Reachable()
	}
	if err != nil || r == nil {
		if err != nil {
			fmt.Fprintf(s.err, "%v: using the state last seen on this machine, which may be behind.\n", err)
		}
		return nil
	}
	return &sharing{store: remote.NewStore(r, nil)}
}

// attach makes the jobs' engines use the shared state.
func (sh *sharing) attach(jobs []job) {
	if sh == nil || sh.store == nil {
		return
	}
	for _, j := range jobs {
		j.e.State = sh.store
	}
}

// check is nil while the lock is still held; a lost lock stops the deploy
// before its next step.
func (sh *sharing) check() error {
	if sh == nil || sh.held == nil {
		return nil
	}
	if err := sh.held.Check(); err != nil {
		return fmt.Errorf("%w: someone else may be changing the project now. Nothing more was changed; "+
			"run the command again to wait for your turn", err)
	}
	return nil
}

// watch is ctx, canceled once the lock is lost; lost turns the error of what
// ran under it into the reason it stopped.
func (sh *sharing) watch(ctx context.Context) (context.Context, func(error) error, context.CancelFunc) {
	if sh == nil || sh.held == nil {
		return ctx, func(err error) error { return err }, func() {}
	}
	ctx, cancel := sh.held.Watch(ctx)
	lost := func(err error) error {
		if err != nil && errors.Is(context.Cause(ctx), remote.ErrLost) {
			return fmt.Errorf("%w while a step ran, so it was stopped: someone else may be changing the project now. "+
				"Run the command again to wait for your turn", remote.ErrLost)
		}
		return err
	}
	return ctx, lost, cancel
}

func (sh *sharing) release() {
	if sh != nil && sh.held != nil {
		sh.held.Release()
	}
	if sh != nil && sh.local != nil {
		sh.local.Close()
	}
}

// publish moves a project's state onto its platform the first time a deploy
// of it succeeds with the platform up: from then on it is shared, and every
// change takes its lock. A platform without a Consul keeps it here.
func publish(ctx context.Context, s *streams, p *project.Project, m *manifest.Manifest, dir, password string) error {
	if p.Meta.Shared || s.breakGlass {
		return nil
	}
	r, err := platformRemote(p, m, dir, password)
	if err != nil || r == nil {
		return err
	}
	if err := r.Reachable(); err != nil {
		fmt.Fprintf(s.err, "The state stays on this machine for now: %v.\n", err)
		return nil
	}
	held, err := r.Lock(ctx, me("publish"), nil)
	if err != nil {
		return err
	}
	defer held.Release()
	store := remote.NewStore(r, held)
	if units, err := store.Units(); err != nil {
		return err
	} else if len(units) > 0 {
		return fmt.Errorf("the platform already keeps the state of %s (%s): set shared: true in %s to use it, "+
			"after checking it is the state of this project", p.Meta.Name, strings.Join(units, ", "),
			filepath.Join(p.Dir, project.MetaFile))
	}
	files, err := filepath.Glob(filepath.Join(p.Dir, "state", "*.tfstate"))
	if err != nil {
		return err
	}
	for _, f := range files {
		unit := strings.TrimSuffix(filepath.Base(f), ".tfstate")
		if err := store.Pull(unit, f); err != nil {
			return err
		}
		if err := store.Push(unit, f); err != nil {
			return fmt.Errorf("publishing the state of %s: %w", unit, err)
		}
	}
	p.Meta.Shared, p.Meta.Requires = true, project.SharedRequires
	if err := p.SaveMeta(); err != nil {
		return err
	}
	s.events.Emit(event.Note{Break: true, Text: fmt.Sprintf("The state of %d steps is now kept on the platform. "+
		"From now on a change to %s waits its turn: one person at a time, the others queue.", len(files), p.Meta.Name)})
	return nil
}

// requireShared writes into a shared project that it needs a damstack that
// knows it is shared, for a project shared before damstack wrote that: an
// older one would change it without its lock.
func requireShared(p *project.Project) error {
	if !p.Meta.Shared || p.Meta.Requires != "" {
		return nil
	}
	p.Meta.Requires = project.SharedRequires
	return p.SaveMeta()
}
