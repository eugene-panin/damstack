package main

import (
	"context"
	"io"
	"sync/atomic"

	"github.com/eugene-panin/damstack/internal/event"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
)

// streamsUI is the ops.UI of the streams, read through them at each call: a
// screen takes them over while it is up.
type streamsUI struct{ s *streams }

func (u streamsUI) Out() io.Writer { return u.s.out }

func (u streamsUI) Err() io.Writer { return u.s.err }

func (u streamsUI) Emit(ev event.Event) { u.s.events.Emit(ev) }

func (u streamsUI) Confirm(question string, def bool) (bool, error) {
	return u.s.confirm(question, def)
}

func (u streamsUI) Approve(p event.Plan) (event.Approval, error) { return u.s.approve(p) }

func (u streamsUI) Line(prompt string) (string, error) { return u.s.prompt.Line(prompt) }

func (u streamsUI) Terminal() io.Reader {
	if u.s.tty() {
		return u.s.in
	}
	return nil
}

// streamsScreen is the ops.Screen of the streams: the deploy screen when the
// terminal allows.
type streamsScreen struct{ s *streams }

func (sc streamsScreen) Run(ctx context.Context, p *project.Project, m *manifest.Manifest, work func(context.Context, *atomic.Bool) error) error {
	return onScreen(ctx, sc.s, p, m, work)
}
