package main

import (
	"context"
	"os"
	"os/exec"
	"sync/atomic"

	"github.com/charmbracelet/colorprofile"

	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/ui/tui"
)

// screen is whether a command may take the terminal over for its TUI: both
// ends a terminal, someone to answer, and neither --plain nor --verbose (whose
// tool output goes to the terminal as it comes).
func (s *streams) screen() bool {
	return s.tty() && !s.plain && !s.verbose && !s.prompt.NoTerminal
}

// onScreen runs work while the deploy screen shows it, when the terminal
// allows; otherwise it just runs work, its events as plain lines. stop is set
// when the person asks to stop after the step running now.
//
// While the screen is up, what work says goes to it: its events, its
// questions, and any stray output, as notes. The streams are as they were
// once work returns.
func onScreen(ctx context.Context, s *streams, p *project.Project, work func(context.Context, *atomic.Bool) error) error {
	var stop atomic.Bool
	if !s.screen() {
		return work(ctx, &stop)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	model := tui.NewDeploy(tui.Actions{
		Stop:  func() { stop.Store(true) },
		Abort: cancel,
		SSH: func() *exec.Cmd {
			self, err := os.Executable()
			if err != nil {
				self = os.Args[0]
			}
			return exec.Command(self, "ssh", p.Meta.Name)
		},
	})
	tui.UseProfile(colorprofile.Detect(s.out, os.Environ()))
	screen := tui.NewProgram(model, s.in, s.out)

	saved := *s
	s.events, s.ask = screen, screen.Confirm
	s.out, s.err = screen.Writer(), screen.Writer()
	defer func() { *s = saved }()

	done := make(chan error, 1)
	go func() {
		err := work(ctx, &stop)
		screen.Finish(err)
		done <- err
	}()
	shown := screen.Run()
	if shown != nil {
		cancel() // the screen failed: the work must not go on unseen
	}
	if err := <-done; err != nil {
		return err
	}
	return shown
}
