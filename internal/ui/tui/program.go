package tui

import (
	"bytes"
	"errors"
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/eugene-panin/damstack/internal/event"
)

// Program shows a screen while the work runs in another goroutine: it is the
// work's event.Sink, asks the work's questions on the screen, and turns any
// stray output of the work into notes on it, so nothing writes over the frame.
type Program struct {
	p    *tea.Program
	done chan struct{}
}

// NewProgram is a Program showing model, reading keys from in and drawing on
// out.
func NewProgram(model tea.Model, in io.Reader, out io.Writer) *Program {
	return &Program{p: tea.NewProgram(model, tea.WithInput(in), tea.WithOutput(out)), done: make(chan struct{})}
}

// Run shows the screen until it is closed.
func (p *Program) Run() error {
	defer close(p.done)
	_, err := p.p.Run()
	return err
}

// Emit shows an event on the screen.
func (p *Program) Emit(e event.Event) { p.p.Send(eventMsg{e}) }

// ErrClosed is returned by Confirm when the screen closed before the question
// was answered.
var ErrClosed = errors.New("the screen was closed before the question was answered")

// Confirm asks a yes-or-no question on the screen and waits for the answer.
func (p *Program) Confirm(question string) (bool, error) {
	answer := make(chan bool, 1)
	p.p.Send(confirmMsg{question: question, answer: answer})
	select {
	case a := <-answer:
		return a, nil
	case <-p.done:
		return false, ErrClosed
	}
}

// Finish tells the screen the work ended, with err when it failed.
func (p *Program) Finish(err error) { p.p.Send(finishMsg{err: err}) }

// Writer is where the work's stray output goes: each line a note on the screen.
func (p *Program) Writer() io.Writer { return &noteWriter{sink: p} }

type noteWriter struct {
	sink event.Sink
	buf  []byte
}

func (w *noteWriter) Write(b []byte) (int, error) {
	w.buf = append(w.buf, b...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(b), nil
		}
		if t := strings.TrimSpace(string(w.buf[:i])); t != "" {
			w.sink.Emit(event.Note{Text: t})
		}
		w.buf = w.buf[i+1:]
	}
}

// Approve shows a plan on the screen and waits for the person to approve it.
func (p *Program) Approve(plan event.Plan) (event.Approval, error) {
	answer := make(chan event.Approval, 1)
	p.p.Send(approveMsg{plan: plan, answer: answer})
	select {
	case a := <-answer:
		return a, nil
	case <-p.done:
		return event.Approval{}, ErrClosed
	}
}
