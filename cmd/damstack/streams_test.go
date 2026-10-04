package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/eugene-panin/damstack/internal/ask"
)

func TestConfirmWithoutTerminal(t *testing.T) {
	var out, errOut bytes.Buffer
	s := newStreams(strings.NewReader("y\n"), &out, &errOut) // a pipe: no terminal
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
