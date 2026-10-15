package ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eugene-panin/damstack/internal/engine"
	"github.com/eugene-panin/damstack/internal/event"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/remote"
)

type deployRun struct {
	p      *project.Project
	m      *manifest.Manifest
	stack  string
	ui     *fakeUI
	runner *fakeRunner
	stop   chan struct{}
}

func runStep(name string) manifest.Step { return manifest.Step{Name: name, Run: []string{name}} }

func newDeploy(t *testing.T, steps ...manifest.Step) *deployRun {
	t.Helper()
	p, _ := newProject(t, "server: {address: 203.0.113.10}\n")
	d := &deployRun{p: p, m: &manifest.Manifest{Name: "demo", Steps: steps}, stack: t.TempDir(), ui: &fakeUI{yes: true},
		runner: &fakeRunner{do: map[string]func(context.Context) error{}}, stop: make(chan struct{})}
	fakeEngines(t, d.runner)
	return d
}

// share makes the project shared on a platform whose Consul is c.
func (d *deployRun) share(t *testing.T, c *fakeConsul) {
	t.Helper()
	d.p.Meta.Shared = true
	d.m.AppEnv = map[string]string{"CONSUL_HTTP_ADDR": "https://10.0.0.1:8501"}
	fakeRemotes(t, c)
}

func (d *deployRun) deploy(t *testing.T, opts Options) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	deployer := &Deployer{UI: d.ui, Screen: &fakeScreen{ui: d.ui, stop: d.stop}, Opts: opts}
	_, err := deployer.Deploy(ctx, d.p, d.m, d.stack)
	return err
}

func (d *deployRun) engine() *engine.Engine {
	d.runner.mu.Lock()
	defer d.runner.mu.Unlock()
	return d.runner.engine
}

func TestADeployRunsEveryStepInTurnAndSaysSo(t *testing.T) {
	d := newDeploy(t, runStep("a"), runStep("b"), runStep("c"))
	if err := d.deploy(t, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := d.runner.steps(); got != "a b c" {
		t.Errorf("ran %q", got)
	}
	events := d.ui.emitted()
	if _, ok := events[0].(event.RunStarted); !ok {
		t.Errorf("the first event is %T, want RunStarted", events[0])
	}
	if _, ok := events[len(events)-1].(event.RunDone); !ok {
		t.Errorf("the last event is %T, want RunDone", events[len(events)-1])
	}
	entries, _ := d.p.History()
	if len(entries) != 3 || entries[2].Step != "c" || entries[2].Result != project.OK || entries[2].Command != "deploy" {
		t.Errorf("history: %+v", entries)
	}
}

func TestAStopBeforeTheFirstStepRunsNone(t *testing.T) {
	d := newDeploy(t, runStep("a"), runStep("b"))
	close(d.stop)
	err := d.deploy(t, Options{})
	if err == nil || err.Error() != "stopped before the first step, as asked; run damstack deploy demo again to begin" {
		t.Fatalf("got %v", err)
	}
	if got := d.runner.steps(); got != "" {
		t.Errorf("no step may run, ran %q", got)
	}
}

func TestAStopAfterAStepRunsNoLaterOne(t *testing.T) {
	d := newDeploy(t, runStep("a"), runStep("b"), runStep("c"))
	d.runner.do["b"] = func(context.Context) error {
		close(d.stop)
		return nil
	}
	err := d.deploy(t, Options{})
	if err == nil || err.Error() != "stopped after b, as asked; run damstack deploy demo again to go on from the next step" {
		t.Fatalf("got %v", err)
	}
	if got := d.runner.steps(); got != "a b" {
		t.Errorf("the step running when asked ends, no later one starts: ran %q", got)
	}
}

func TestAStopWhileWaitingForTheLockEndsTheWait(t *testing.T) {
	d := newDeploy(t, runStep("a"))
	c := newFakeConsul()
	d.share(t, c)
	other, err := remote.New("demo", c, c).Lock(context.Background(), remote.Holder{Person: "garen", Action: "deploy"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Release()
	var once sync.Once
	d.ui.onEmit = func(e event.Event) {
		if w, ok := e.(event.Waiting); ok && strings.Contains(w.Holder, "garen") {
			once.Do(func() { close(d.stop) })
		}
	}
	err = d.deploy(t, Options{})
	if err == nil || err.Error() != "stopped before the first step, as asked; run damstack deploy demo again to begin" {
		t.Fatalf("got %v", err)
	}
	if got := d.runner.steps(); got != "" {
		t.Errorf("no step may run without the lock, ran %q", got)
	}
	if h, _ := remote.New("demo", c, c).Holder(); h == nil || h.Person != "garen" {
		t.Errorf("the lock stays with who held it: %+v", h)
	}
}

func TestALockLostWhileAStepRunsStopsTheDeploy(t *testing.T) {
	d := newDeploy(t, runStep("a"), runStep("b"))
	c := newFakeConsul()
	d.share(t, c)
	d.runner.do["a"] = func(ctx context.Context) error {
		c.expireAll()
		store, ok := d.engine().State.(*remote.Store)
		if !ok {
			t.Error("the steps of a shared project keep their state on the platform")
			return errors.New("no store")
		}
		store.Held.Check()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return errors.New("the step went on with the lock lost")
		}
	}
	err := d.deploy(t, Options{})
	if !errors.Is(err, remote.ErrLost) || !strings.Contains(err.Error(), "while a step ran, so it was stopped") {
		t.Fatalf("got %v", err)
	}
	if got := d.runner.steps(); got != "a" {
		t.Errorf("no step may run once the lock is lost, ran %q", got)
	}
}

func TestAFailedStepEndsTheRunAndIsRecorded(t *testing.T) {
	d := newDeploy(t, runStep("a"), runStep("b"), runStep("c"))
	d.runner.do["b"] = func(context.Context) error { return errors.New("boom") }
	err := d.deploy(t, Options{})
	if err == nil || !strings.Contains(err.Error(), "the step b failed: boom") || !strings.Contains(err.Error(), "run damstack deploy again in "+d.p.Dir) {
		t.Fatalf("got %v", err)
	}
	if got := d.runner.steps(); got != "a b" {
		t.Errorf("no step may run after one failed, ran %q", got)
	}
	entries, _ := d.p.History()
	if len(entries) != 2 || entries[1].Step != "b" || entries[1].Result != project.Failed || entries[1].Error != "boom" {
		t.Errorf("history: %+v", entries)
	}
	var failed []string
	for _, e := range d.ui.emitted() {
		if f, ok := e.(event.StepFailed); ok {
			failed = append(failed, f.Name)
		}
		if _, ok := e.(event.RunDone); ok {
			t.Error("a failed run is not done")
		}
	}
	if strings.Join(failed, " ") != "b" {
		t.Errorf("failed steps: %v", failed)
	}
}

func TestARestoreRunsItsStepsWithTheSnapshotAndNoLock(t *testing.T) {
	back := runStep("back")
	back.Restore = true
	d := newDeploy(t, runStep("a"), back, runStep("c"))
	here, err := LockHere(d.p)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.deploy(t, Options{}); err == nil || !strings.Contains(err.Error(), "another damstack is changing demo") {
		t.Fatalf("a deploy takes the lock: %v", err)
	}
	if err := d.deploy(t, Options{Restore: "4f2a9c1e"}); err != nil {
		t.Fatalf("a restore sets up a new server, with no lock to take: %v", err)
	}
	here.Close()
	if got := d.runner.steps(); got != "a back c" {
		t.Errorf("ran %q", got)
	}
	for i, env := range d.runner.envs {
		if env["DAMSTACK_RESTORE"] != "4f2a9c1e" {
			t.Errorf("step %d: DAMSTACK_RESTORE is %q", i, env["DAMSTACK_RESTORE"])
		}
	}
	entries, _ := d.p.History()
	for _, e := range entries {
		if e.Command != "restore" {
			t.Errorf("%s is recorded as %s", e.Step, e.Command)
		}
	}

	d.runner.ran, d.runner.envs = nil, nil
	if err := d.deploy(t, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := d.runner.steps(); got != "a c" {
		t.Errorf("a deploy runs no step of a restore, ran %q", got)
	}
}

func TestADeployRunsOnlyThePartsPicked(t *testing.T) {
	d := newDeploy(t, runStep("a"), runStep("b"), runStep("c"))
	if err := d.deploy(t, Options{Only: map[string]bool{"b": true}}); err != nil {
		t.Fatal(err)
	}
	if got := d.runner.steps(); got != "b" {
		t.Errorf("ran %q", got)
	}
	err := d.deploy(t, Options{Only: map[string]bool{"nope": true}})
	if err == nil || err.Error() != "nope is no part of a deploy of demo; the parts: a, b, c" {
		t.Fatalf("got %v", err)
	}
	if got := d.runner.steps(); got != "b" {
		t.Errorf("a part that is not there runs nothing, ran %q", got)
	}
}

func TestAnUnchangedAnsibleStepRunsAgainWithAll(t *testing.T) {
	d := newDeploy(t, manifest.Step{Name: "provision", Ansible: &manifest.Ansible{Playbook: "ansible/site.yml"}}, runStep("apply"))
	if err := os.MkdirAll(filepath.Join(d.stack, "ansible"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.stack, "ansible/site.yml"), []byte("- hosts: all\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plays := func() int { return strings.Count(d.runner.steps(), "ansible-playbook") }
	if err := d.deploy(t, Options{}); err != nil {
		t.Fatal(err)
	}
	first := plays()
	if first == 0 {
		t.Fatalf("the first deploy plays the playbook: ran %q", d.runner.steps())
	}
	if err := d.deploy(t, Options{}); err != nil {
		t.Fatal(err)
	}
	if plays() != first {
		t.Errorf("an unchanged step is skipped: ran %q", d.runner.steps())
	}
	skipped := false
	for _, e := range d.ui.emitted() {
		if s, ok := e.(event.StepSkipped); ok && s.Name == "provision" && strings.HasPrefix(s.Reason, "unchanged since") {
			skipped = true
		}
	}
	if !skipped {
		t.Error("the skip is said")
	}
	if err := d.deploy(t, Options{All: true}); err != nil {
		t.Fatal(err)
	}
	if plays() != 2*first {
		t.Errorf("--all runs it again: ran %q", d.runner.steps())
	}
	if got := strings.Count(d.runner.steps(), "apply"); got != 3 {
		t.Errorf("a step that is not fingerprinted runs every time, ran %d times", got)
	}
}
