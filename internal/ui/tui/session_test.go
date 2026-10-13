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
