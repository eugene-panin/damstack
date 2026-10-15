// Package ops runs what damstack does to a project, such as a deploy, apart
// from how it is shown: the person is reached through a UI.
package ops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/eugene-panin/damstack/internal/engine"
	"github.com/eugene-panin/damstack/internal/event"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/wg"
)

// UI is how a command reaches the person running it: where its output goes,
// where its events go, and how it asks.
type UI interface {
	Out() io.Writer
	Err() io.Writer
	Emit(event.Event)
	Confirm(question string, def bool) (bool, error)
	Approve(event.Plan) (event.Approval, error)
	Line(prompt string) (string, error)
	// Terminal is stdin when both ends are a terminal, else nil.
	Terminal() io.Reader
}

// Screen runs work while a deploy is shown: work reaches the person through ui
// meanwhile, the screen's while it is up. stop is set when the person asks to
// stop after the step running now.
type Screen interface {
	Run(ctx context.Context, p *project.Project, m *manifest.Manifest, work func(ctx context.Context, ui UI, stop *atomic.Bool) error) error
}

// Options are how a deploy runs.
type Options struct {
	// Yes goes ahead wherever damstack would ask to.
	Yes bool
	// Verbose shows the output of the tools instead of keeping it in a log.
	Verbose bool
	// BreakGlass changes a shared project without its lock, from the state on
	// this machine, for when the platform itself is down.
	BreakGlass bool
	// All runs every step, those whose inputs did not change too.
	All bool
	// AppsOnly skips the platform's Ansible steps.
	AppsOnly bool
	// Only, when set, is what a deploy runs: the platform's steps by name and
	// the apps, all their steps, by theirs; it runs them though unchanged.
	Only map[string]bool
	// Restore, the snapshot of a backup, sets up a new server from it.
	Restore string
}

// Deployer deploys projects.
type Deployer struct {
	UI     UI
	Screen Screen
	Opts   Options
}

// Result is what a deploy changed that the person needs to hear of after it.
type Result struct {
	// Keys are the WireGuard keys the deploy made or took away.
	Keys wg.Change
}

// Job is a step to run, of the platform or of an app.
type Job struct {
	Engine *engine.Engine
	Name   string
	Step   manifest.Step
	// Inputs is the fingerprint of what the step is given, for a step that
	// may be skipped while they stay the same; empty for one that always runs.
	Inputs string
}

// Part is what a job is part of, as a deploy picks it: its app, or for a step
// of the platform, the step.
func (j Job) Part() string {
	if app, _, ok := strings.Cut(j.Name, "/"); ok {
		return app
	}
	return j.Name
}

// Title is what a step is called for people: an app's step after its app.
func (j Job) Title() string {
	title := j.Step.Title
	if title == "" {
		title = j.Name
	}
	if app, _, ok := strings.Cut(j.Name, "/"); ok && j.Step.Title != "" {
		title = app + ": " + title
	}
	return title
}

// Kind is what runs a step, and its playbook or directory.
func (j Job) Kind() (string, string) {
	switch {
	case j.Step.Ansible != nil:
		return "ansible", filepath.Base(j.Step.Ansible.Playbook)
	case j.Step.Tofu != nil:
		return "tofu", j.Step.Tofu.Dir
	case len(j.Step.Run) > 0:
		return "run", filepath.Base(j.Step.Run[0])
	}
	return "", ""
}

// Deploy deploys a project: the steps of its platform, then those of each
// app, then the platform's steps that come after the apps. With Restore it
// sets up a new server from the snapshot, running the steps that run once and
// those of a restore too.
func (d *Deployer) Deploy(ctx context.Context, p *project.Project, m *manifest.Manifest, dir string) (Result, error) {
	restore := d.Opts.Restore
	password, err := p.Password()
	if err != nil {
		return Result{}, err
	}
	if _, err := EnsureSSHKey(p, m, password); err != nil {
		return Result{}, err
	}
	e, key, err := NewEngine(ctx, d.UI, d.Opts.Yes, p, m, dir)
	if err != nil {
		return Result{}, err
	}
	if m.Server != nil {
		if err := ensureLogin(ctx, d.UI, d.Opts.Yes, e, key); err != nil {
			return Result{}, err
		}
	}
	var keys wg.Change
	err = d.Screen.Run(ctx, p, m, func(ctx context.Context, ui UI, stop *atomic.Bool) error {
		e.Events, e.Approve = event.SinkFunc(ui.Emit), approver(ui, d.Opts.Yes)
		var err error
		// A restore sets up a new server from a backup, the platform's state
		// with it: there is no platform to lock yet.
		var sh *Sharing
		if restore == "" {
			if sh, err = LockProject(ctx, ui, d.Opts.BreakGlass, p, m, dir, e.Password, "deploy"); err != nil {
				return err
			}
			defer sh.Release()
		}
		ctx, lost, cancel := sh.Watch(ctx)
		defer cancel()
		if keys, err = EnsureKeys(p, m, e.Password); err != nil {
			return err
		}
		jobs, err := BuildJobs(ctx, ui, d.Opts.Yes, p, m, e, restore != "", d.Opts.AppsOnly)
		if err != nil {
			return err
		}
		if jobs, err = chooseJobs(d.Opts.Only, d.Opts.All, p, jobs, restore != "", e.Password); err != nil {
			return err
		}
		sh.Attach(jobs)
		command := "deploy"
		if restore != "" {
			command = "restore"
		}
		ui.Emit(RunStarted(p, command, jobs))
		for _, j := range jobs {
			if restore != "" {
				if j.Engine.BaseEnv == nil {
					j.Engine.BaseEnv = map[string]string{}
				}
				j.Engine.BaseEnv["DAMSTACK_RESTORE"] = restore
			}
		}
		for i, j := range jobs {
			if stop.Load() {
				if i == 0 {
					return fmt.Errorf("stopped before the first step, as asked; run damstack %s %s again to begin",
						command, p.Meta.Name)
				}
				return fmt.Errorf("stopped after %s, as asked; run damstack %s %s again to go on from the next step",
					jobs[i-1].Name, command, p.Meta.Name)
			}
			if err := sh.Check(); err != nil {
				return err
			}
			if err := d.runJob(ctx, ui, p, j, i+1, len(jobs), m.Server, restore != ""); err != nil {
				return lost(err)
			}
		}
		if restore == "" {
			if err := publish(ctx, ui, d.Opts.BreakGlass, p, m, dir, e.Password); err != nil {
				return lost(err)
			}
		}
		ui.Emit(RunDone(p, m))
		return nil
	})
	return Result{Keys: keys}, err
}

// BuildJobs lists the steps of a deploy in their order: the platform's, every
// app's, then the platform's that come after the apps; those of a restore only
// with restore. The apps' engines are made now, with ui and yes.
func BuildJobs(ctx context.Context, ui UI, yes bool, p *project.Project, m *manifest.Manifest, e *engine.Engine, restore, appsOnly bool) ([]Job, error) {
	var jobs, after []Job
	for _, step := range m.Steps {
		if step.Restore && !restore {
			continue
		}
		// --apps-only skips the platform's Ansible steps (bootstrap, provision):
		// the slow convergence of a platform that is already up. Its OpenTofu
		// steps (Traefik, and the after-apps DNS publish) still run.
		if appsOnly && step.Ansible != nil {
			continue
		}
		if step.AfterApps {
			after = append(after, Job{Engine: e, Name: step.Name, Step: step})
		} else {
			jobs = append(jobs, Job{Engine: e, Name: step.Name, Step: step})
		}
	}
	for _, ref := range p.Meta.Apps {
		am, adir, err := RefStack(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("the app %s: %w", ref.Name, err)
		}
		_, target, ok := am.Target(m.Provides)
		if !ok {
			return nil, fmt.Errorf("the app %s has no way to run on %s, which provides %s", ref.Name, m.Name, strings.Join(m.Provides, ", "))
		}
		ae, _, err := NewEngine(ctx, ui, yes, p, am, adir)
		if err != nil {
			return nil, err
		}
		ae.App = ref.Name
		if ae.BaseEnv, err = AppEnv(p, m, e.Stack, ae.Password); err != nil {
			return nil, err
		}
		for _, step := range target.Steps {
			if step.Restore && !restore {
				continue
			}
			jobs = append(jobs, Job{Engine: ae, Name: ref.Name + "/" + step.Name, Step: step})
		}
	}
	return append(jobs, after...), nil
}

func (d *Deployer) runJob(ctx context.Context, ui UI, p *project.Project, j Job, n, total int, srv *manifest.Server, restore bool) error {
	step := event.Step{N: n, Total: total, Name: j.Name, Title: j.Title()}
	if j.Step.Once && !restore {
		done, err := p.Done(j.Name)
		if err != nil {
			return err
		}
		if done {
			ui.Emit(event.StepSkipped{Step: step, Reason: "done before, runs once"})
			return nil
		}
	}
	if why := Unchanged(p, j.Name, j.Inputs, time.Now()); why != "" {
		ui.Emit(event.StepSkipped{Step: step, Reason: why})
		return nil
	}
	log := ""
	if !d.Opts.Verbose {
		var restore func()
		var err error
		if log, restore, err = LogTo(p, j); err != nil {
			return err
		}
		defer restore()
	}
	kind, what := j.Kind()
	ui.Emit(event.StepStarted{Step: step, Takes: j.Step.Takes, Kind: kind, What: what, Log: log})
	if j.Step.Tunnel && srv != nil {
		if err := WaitTunnel(ctx, ui, p, srv); err != nil {
			return err
		}
	}
	command := "deploy"
	if restore {
		command = "restore"
	}
	start := time.Now()
	err := j.Engine.Run(ctx, j.Step, nil)
	if rerr := RecordRun(p, command, j.Name, j.Inputs, start, err); rerr != nil && err == nil {
		err = rerr
	}
	if errors.Is(err, engine.ErrDeclined) {
		return fmt.Errorf("%s: %w; run damstack deploy again when you want it", j.Name, err)
	}
	if err != nil {
		failed := event.StepFailed{Step: step, Err: err}
		if log != "" {
			failed.Log, failed.Tail = log, LastLines(log, 30)
		}
		ui.Emit(failed)
		return fmt.Errorf("the step %s failed: %w\nFix what it says above, then run damstack %s again in %s", j.Name, err, command, p.Dir)
	}
	ui.Emit(event.StepDone{Step: step, Took: time.Since(start)})
	return nil
}
