package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/muesli/cancelreader"

	"github.com/eugene-panin/damstack/internal/ask"
)

func TestConfirmWithoutTerminal(t *testing.T) {
	var out, errOut bytes.Buffer
	s := newStreams(t.Context(), strings.NewReader("y\n"), &out, &errOut) // a pipe: no terminal
	ok, err := s.confirm("Apply the plan?", true)
	if ok || !errors.Is(err, ask.ErrNoTerminal) || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("without --yes: %v, %v", ok, err)
	}
	if out.Len()+errOut.Len() > 0 {
		t.Errorf("asked anyway: %q %q", out.String(), errOut.String())
	}
	s.yes = true
	if ok, err := s.confirm("Apply the plan?", false); !ok || err != nil {
		t.Errorf("with --yes: %v, %v", ok, err)
	}
}

type cancelledRead struct{}

func (cancelledRead) Read([]byte) (int, error) { return 0, cancelreader.ErrCanceled }

// A question given up at Ctrl-C ends the run as interrupted, quietly.
func TestCancelledQuestion(t *testing.T) {
	p := ask.NewPrompter(cancelled{cancelledRead{}}, &bytes.Buffer{})
	if _, err := p.Line("Name: "); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}
