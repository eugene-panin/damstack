package tui

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// syncBuffer is a terminal's output, read while the program writes it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// quitting is a screen that shows its name and quits once it got a message
// it is waiting for, or at once.
type quitting struct {
	name string
	wait bool
	got  []tea.Msg
}

type pokeMsg struct{}

func (q *quitting) Init() tea.Cmd {
	if q.wait {
		return nil
	}
	return tea.Quit
}

func (q *quitting) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(pokeMsg); ok {
		q.got = append(q.got, msg)
		return q, tea.Batch(nil, tea.Quit) // a quit inside a batch
	}
	return q, nil
}

func (q *quitting) View() tea.View {
	v := tea.NewView(q.name)
	v.AltScreen = true
	return v
}

const leaveAltScreen = "\x1b[?1049l"

func TestScreensFollowOneAnotherWithoutLeavingTheTerminal(t *testing.T) {
	in, w := io.Pipe()
	defer w.Close()
	out := &syncBuffer{}
	s := NewSession(in, out)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.Show(ctx, &quitting{name: "first"}); err != nil {
		t.Fatalf("the first screen: %v", err)
	}
	// The deploy screen's way: shown at once, its events for it alone.
	second := &quitting{name: "second", wait: true}
	p := s.Program(second)
	go p.Emit(nil)
	go p.p.Send(screenMsg{quit: p.quit, msg: pokeMsg{}})
	if err := p.Run(); err != nil {
		t.Fatalf("the second screen: %v", err)
	}
	if len(second.got) != 1 {
		t.Errorf("the second screen must get what is sent to it: %v", second.got)
	}
	// What is sent to a screen gone reaches no other.
	third := &quitting{name: "third", wait: true}
	p.p.Send(screenMsg{quit: p.quit, msg: pokeMsg{}})
	done := make(chan error, 1)
	go func() { done <- s.Show(ctx, third) }()
	time.Sleep(200 * time.Millisecond)
	if len(third.got) != 0 {
		t.Error("a message for the second screen reached the third")
	}
	s.p.Send(pokeMsg{})
	if err := <-done; err != nil {
		t.Fatalf("the third screen: %v", err)
	}
	if strings.Contains(out.String(), leaveAltScreen) {
		t.Error("the terminal left the screen between two screens: the shell showed")
	}
	s.Close()
	if !strings.Contains(out.String(), leaveAltScreen) {
		t.Error("closing the session must give the terminal back")
	}
}

// leaving is a screen that quits at once and leaves a late message behind,
// as a tick does.
type leaving struct{ quitting }

func (l *leaving) Init() tea.Cmd {
	late := func() tea.Msg { time.Sleep(100 * time.Millisecond); return pokeMsg{} }
	return tea.Batch(tea.Quit, late)
}

func TestALateMessageOfAScreenGoneReachesNoOther(t *testing.T) {
	in, w := io.Pipe()
	defer w.Close()
	s := NewSession(in, &syncBuffer{})
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Show(ctx, &leaving{quitting{name: "first"}}); err != nil {
		t.Fatal(err)
	}
	next := &quitting{name: "next", wait: true}
	done := make(chan error, 1)
	go func() { done <- s.Show(ctx, next) }()
	time.Sleep(300 * time.Millisecond)
	if len(next.got) != 0 {
		t.Error("the tick of the screen gone reached the next")
	}
	s.p.Send(pokeMsg{})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
