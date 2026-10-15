package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eugene-panin/damstack/internal/engine"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/remote"
)

func TestTwoCommandsDoNotChangeAProjectAtOnce(t *testing.T) {
	p := &project.Project{Dir: t.TempDir(), Meta: project.Meta{Name: "demo"}}
	first, err := LockHere(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LockHere(p); err == nil || !strings.Contains(err.Error(), "another damstack") {
		t.Fatalf("a second lock must be refused, got %v", err)
	}
	first.Close()
	again, err := LockHere(p)
	if err != nil {
		t.Fatalf("once the first is done, the lock is free: %v", err)
	}
	again.Close()
}

func sharedProject(t *testing.T, c *fakeConsul) (*project.Project, *manifest.Manifest, string) {
	t.Helper()
	p, password := newProject(t, "server: {address: 203.0.113.10}\n")
	p.Meta.Shared = true
	fakeRemotes(t, c)
	return p, &manifest.Manifest{Name: "demo", AppEnv: map[string]string{"CONSUL_HTTP_ADDR": "https://10.0.0.1:8501"}}, password
}

func TestAProjectKeptHereTakesOnlyTheLockHere(t *testing.T) {
	p, password := newProject(t, "server: {address: 203.0.113.10}\n")
	ui := &fakeUI{}
	m := &manifest.Manifest{Name: "demo"}
	sh, err := LockProject(context.Background(), ui, false, p, m, t.TempDir(), password, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	if sh.Store() != nil || sh.Check() != nil {
		t.Error("a project kept here shares no state and has no lock to lose")
	}
	_, lost, cancel := sh.Watch(context.Background())
	defer cancel()
	boom := errors.New("boom")
	if lost(boom) != boom {
		t.Error("with no lock to lose, an error stays what it is")
	}
	if _, err := LockProject(context.Background(), ui, false, p, m, t.TempDir(), password, "deploy"); err == nil ||
		!strings.Contains(err.Error(), "another damstack is changing demo on this computer now") {
		t.Fatalf("a second command must wait for the first: %v", err)
	}
	sh.Release()
	again, err := LockProject(context.Background(), ui, false, p, m, t.TempDir(), password, "deploy")
	if err != nil {
		t.Fatalf("once released, the lock is free: %v", err)
	}
	again.Release()
}

func TestBreakGlassGoesOnWithoutThePlatformsLock(t *testing.T) {
	c := newFakeConsul()
	p, m, password := sharedProject(t, c)
	other, err := remote.New("demo", c, c).Lock(context.Background(), remote.Holder{Person: "garen"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Release()
	ui := &fakeUI{}
	sh, err := LockProject(context.Background(), ui, true, p, m, t.TempDir(), password, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	defer sh.Release()
	if !strings.Contains(ui.said(), "--break-glass: changing demo without its lock, from the state on this machine. Anyone else changing it now races this.") {
		t.Errorf("break-glass must warn: %q", ui.said())
	}
	if sh.Store() != nil || c.n != 1 {
		t.Errorf("break-glass touches nothing of the platform: store %v, sessions %d", sh.Store(), c.n)
	}
	if meta, _ := project.Open(p.Dir); meta.Meta.Requires != project.SharedRequires {
		t.Errorf("a shared project says it needs a damstack that knows it: %q", meta.Meta.Requires)
	}
	if _, err := LockProject(context.Background(), ui, true, p, m, t.TempDir(), password, "deploy"); err == nil {
		t.Error("break-glass still takes the lock here")
	}
}

func TestASharedProjectTakesThePlatformsLockAndKeepsItsStateThere(t *testing.T) {
	c := newFakeConsul()
	p, m, password := sharedProject(t, c)
	sh, err := LockProject(context.Background(), &fakeUI{}, false, p, m, t.TempDir(), password, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	if h, _ := remote.New("demo", c, c).Holder(); h == nil || h.Action != "deploy" {
		t.Errorf("the platform must say who holds the lock: %+v", h)
	}
	e := &engine.Engine{}
	sh.Attach([]Job{{Engine: e}})
	if sh.Store() == nil || e.State != sh.Store() {
		t.Error("the steps keep their state on the platform")
	}
	sh.Release()
	if h, _ := remote.New("demo", c, c).Holder(); h != nil {
		t.Errorf("released, no one holds it: %+v", h)
	}
}

func TestAPlatformThatDoesNotAnswerChangesNothing(t *testing.T) {
	c := newFakeConsul()
	c.down = true
	p, m, password := sharedProject(t, c)
	_, err := LockProject(context.Background(), &fakeUI{}, false, p, m, t.TempDir(), password, "deploy")
	if !errors.Is(err, remote.ErrUnreachable) || !strings.Contains(err.Error(), "and demo is shared there: nothing was changed") ||
		!strings.Contains(err.Error(), "--break-glass") {
		t.Fatalf("got %v", err)
	}
	if _, err := LockHere(p); err != nil {
		t.Errorf("a refused lock lets go of the one here: %v", err)
	}
}

func TestALostLockStopsWhatRunsUnderIt(t *testing.T) {
	c := newFakeConsul()
	p, m, password := sharedProject(t, c)
	sh, err := LockProject(context.Background(), &fakeUI{}, false, p, m, t.TempDir(), password, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	defer sh.Release()
	ctx, lost, cancel := sh.Watch(context.Background())
	defer cancel()
	boom := errors.New("boom")
	if lost(boom) != boom || sh.Check() != nil {
		t.Fatal("while the lock is held, an error is the step's own")
	}
	c.expireAll()
	err = sh.Check()
	if !errors.Is(err, remote.ErrLost) || !strings.Contains(err.Error(), "Nothing more was changed") {
		t.Fatalf("a lost lock stops the deploy before its next step: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("a lost lock must stop what runs under it")
	}
	if err := lost(boom); !errors.Is(err, remote.ErrLost) || !strings.Contains(err.Error(), "while a step ran, so it was stopped") {
		t.Errorf("got %v", err)
	}
	if lost(nil) != nil {
		t.Error("a step that went well did not fail")
	}
}
