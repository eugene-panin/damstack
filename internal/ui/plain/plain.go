// Package plain shows damstack's events as plain lines, one after another: for
// a terminal without the TUI, for CI and for logs. It is what damstack printed
// before it had events, kept line for line.
package plain

import (
	"fmt"
	"io"
	"time"

	"github.com/eugene-panin/damstack/internal/event"
)

// Renderer writes events to W as lines. It is an event.Sink.
type Renderer struct {
	W io.Writer
}

// New is a Renderer writing to w.
func New(w io.Writer) *Renderer { return &Renderer{W: w} }

// Emit writes one event. Errors writing to W are dropped, as a terminal that
// went away is no reason to stop the work.
func (r *Renderer) Emit(e event.Event) {
	switch e := e.(type) {
	case event.StepStarted:
		takes := ""
		if e.Takes != "" {
			takes = " (" + e.Takes + ")"
		}
		fmt.Fprintf(r.W, "\n%d/%d  %s%s\n", e.N, e.Total, e.Title, takes)
	case event.StepSkipped:
		fmt.Fprintf(r.W, "\n%d/%d  %s: %s\n", e.N, e.Total, e.Title, e.Reason)
	case event.StepDone:
		fmt.Fprintf(r.W, "     ok, %s\n", e.Took.Round(time.Second))
	case event.StepFailed:
		for _, line := range e.Tail {
			fmt.Fprintf(r.W, "  | %s\n", line)
		}
		if e.Log != "" {
			fmt.Fprintf(r.W, "  The whole output is in %s\n", e.Log)
		}
	case event.Note:
		if e.Break {
			fmt.Fprintln(r.W)
		}
		fmt.Fprintln(r.W, e.Text)
	case event.Heading:
		fmt.Fprintf(r.W, "\n== %s\n", e.Text)
	case event.Waiting:
		switch {
		case e.Holder != "" && e.Ahead <= 1:
			fmt.Fprintf(r.W, "Waiting: %s. You are next.\n", e.Holder)
		case e.Holder != "":
			fmt.Fprintf(r.W, "Waiting: %s. %d ahead of you.\n", e.Holder, e.Ahead)
		case e.Ahead > 1:
			fmt.Fprintf(r.W, "Waiting for %d ahead of you.\n", e.Ahead)
		}
		// No holder and next in line: the lock was just freed and is taken in
		// a moment; nothing worth saying.
	}
}
