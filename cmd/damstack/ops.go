package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	"github.com/eugene-panin/damstack/internal/ask"
	"github.com/eugene-panin/damstack/internal/event"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/ops"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/ui/plain"
	"github.com/eugene-panin/damstack/internal/ui/tui"
)

// lineUI is the ops.UI of a command on its terminal: its events as plain
// lines, its questions on the prompter. yes, --yes, answers them all.
type lineUI struct {
	t   *terminal
	yes bool
}

// ui is the terminal's ops.UI, with yes for --yes.
func (t *terminal) ui(yes bool) lineUI { return lineUI{t: t, yes: yes} }

func (u lineUI) Out() io.Writer { return u.t.out }

func (u lineUI) Err() io.Writer { return u.t.err }

func (u lineUI) Emit(ev event.Event) { plain.New(u.t.out).Emit(ev) }

// Confirm asks a yes or no question, unless --yes answered it already;
// without a terminal to ask in, only --yes goes ahead.
func (u lineUI) Confirm(question string, def bool) (bool, error) {
	if u.yes {
		return true, nil
	}
	if u.t.prompt.NoTerminal {
		return false, fmt.Errorf("%q: %w; pass --yes to go ahead", question, ask.ErrNoTerminal)
	}
	return u.t.prompt.Confirm(question, def)
}

// Approve asks whether to apply a plan as lines: the plan's question and,
// when it needs allocations replaced, a second one. --yes says yes to both.
func (u lineUI) Approve(p event.Plan) (event.Approval, error) {
	if u.yes {
		return event.Approval{Apply: true, Restart: true}, nil
	}
	ok, err := u.Confirm(p.Question, false)
	if err != nil || !ok {
		return event.Approval{}, err
	}
	a := event.Approval{Apply: true}
	if len(p.Restarts) > 0 {
		groups := make([]string, len(p.Restarts))
		for i, r := range p.Restarts {
			groups[i] = r.Job + "/" + r.Group
		}
		u.Emit(event.Note{Break: true, Text: "Nomad changes " + strings.Join(groups, ", ") + " in place: " +
			"running allocations keep their old network and sidecar, so the change takes effect only in new ones."})
		if a.Restart, err = u.Confirm("Replace those allocations after the apply, one at a time?", true); err != nil {
			return event.Approval{}, err
		}
	}
	return a, nil
}

func (u lineUI) Line(prompt string) (string, error) { return u.t.prompt.Line(prompt) }

func (u lineUI) Terminal() io.Reader {
	if u.t.tty() {
		return u.t.in
	}
	return nil
}

// unattended is an ops.UI with no terminal to wait on the person at: what
// would wait there says how to do it instead.
type unattended struct{ ops.UI }

func (unattended) Terminal() io.Reader { return nil }

// screenUI is the ops.UI of a deploy while its screen is up: its events and
// questions go to the screen, any stray output as notes. yes, --yes, still
// answers the questions.
type screenUI struct {
	screen *tui.Program
	out    io.Writer
	err    io.Writer
	prompt *ask.Prompter
	yes    bool
}

func (u screenUI) Out() io.Writer { return u.out }

func (u screenUI) Err() io.Writer { return u.err }

func (u screenUI) Emit(ev event.Event) { u.screen.Emit(ev) }

func (u screenUI) Confirm(question string, _ bool) (bool, error) {
	if u.yes {
		return true, nil
	}
	return u.screen.Confirm(question)
}

func (u screenUI) Approve(p event.Plan) (event.Approval, error) {
	if u.yes {
		return event.Approval{Apply: true, Restart: true}, nil
	}
	return u.screen.Approve(p)
}

func (u screenUI) Line(prompt string) (string, error) { return u.prompt.Line(prompt) }

func (screenUI) Terminal() io.Reader { return nil }

// deployScreen is the ops.Screen of a deploy: the deploy screen when the
// terminal allows and plain, --plain or --verbose, does not ask for lines;
// else ui, the terminal's.
type deployScreen struct {
	s     *cli
	ui    lineUI
	plain bool
}

func (d deployScreen) Run(ctx context.Context, p *project.Project, m *manifest.Manifest, work func(context.Context, ops.UI, *atomic.Bool) error) error {
	return onScreen(ctx, d.s, d.ui, d.plain, p, m, work)
}
