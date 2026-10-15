package tui

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
)

// Session keeps one program on the terminal while screens follow one another:
// a screen that quits hands the terminal to the next without leaving it, so
// the shell never shows between two screens. Each Show runs a screen until it
// quits, as tea.NewProgram(...).Run() would.
type Session struct {
	in  io.Reader
	out io.Writer

	once sync.Once
	p    *tea.Program
	done chan struct{} // closed when the program has ended
	err  error
}

// NewSession is a session reading keys from in and drawing on out; its
// program starts with the first screen.
func NewSession(in io.Reader, out io.Writer) *Session {
	return &Session{in: in, out: out, done: make(chan struct{})}
}

// ErrSessionEnded is returned by Show when the session's program ended, such
// as on ctrl+c, before the screen quit.
var ErrSessionEnded = errors.New("the screen session ended")

func (s *Session) start() {
	s.once.Do(func() {
		s.p = tea.NewProgram(&stage{}, tea.WithInput(s.in), tea.WithOutput(s.out))
		go func() {
			_, s.err = s.p.Run()
			close(s.done)
		}()
	})
}

// Show runs model on the session's terminal until the screen quits.
func (s *Session) Show(ctx context.Context, model tea.Model) error {
	s.start()
	quit := make(chan struct{})
	s.p.Send(showMsg{model: model, quit: quit})
	select {
	case <-quit:
		return nil
	case <-s.done:
		if s.err != nil {
			return s.err
		}
		return ErrSessionEnded
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Exec runs cmd on the terminal the session lets go of meanwhile, such as
// ssh or an editor; the screen comes back when it ends.
func (s *Session) Exec(cmd *exec.Cmd) error {
	s.start()
	if err := s.p.ReleaseTerminal(); err != nil {
		return err
	}
	err := cmd.Run()
	if rerr := s.p.RestoreTerminal(); err == nil {
		err = rerr
	}
	return err
}

// Close ends the session's program and gives the terminal back.
func (s *Session) Close() {
	if s.p == nil {
		return
	}
	s.p.Quit()
	<-s.done
}

// showMsg puts a screen on the stage; quit is closed when it quits.
type showMsg struct {
	model tea.Model
	quit  chan struct{}
}

// quitScreenMsg is a screen's tea.Quit: that screen is done, not the session.
type quitScreenMsg struct{ quit chan struct{} }

// screenMsg is a message for one screen only (the work's events for the
// deploy screen); dropped once that screen is gone.
type screenMsg struct {
	quit chan struct{}
	msg  tea.Msg
}

// stage is the session's model: the screen showing now, or none between two.
type stage struct {
	screen tea.Model
	quit   chan struct{}
	size   *tea.WindowSizeMsg
}

func (s *stage) Init() tea.Cmd { return nil }

func (s *stage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case showMsg:
		s.screen, s.quit = msg.model, msg.quit
		cmds := []tea.Cmd{s.wrap(s.screen.Init(), s.quit)}
		if s.size != nil {
			var cmd tea.Cmd
			s.screen, cmd = s.screen.Update(*s.size)
			cmds = append(cmds, s.wrap(cmd, s.quit))
		}
		return s, tea.Batch(cmds...)
	case quitScreenMsg:
		if msg.quit == s.quit && s.screen != nil {
			s.screen = nil
			close(s.quit)
		}
		return s, nil
	case screenMsg:
		if msg.quit != s.quit || s.screen == nil {
			return s, nil
		}
		return s.forward(msg.msg)
	case tea.WindowSizeMsg:
		s.size = &msg
	}
	if s.screen == nil {
		return s, nil
	}
	return s.forward(msg)
}

func (s *stage) forward(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	s.screen, cmd = s.screen.Update(msg)
	return s, s.wrap(cmd, s.quit)
}

// wrap turns a screen's tea.Quit, also inside a batch, into the end of that
// screen alone, and the screen's own messages into ones for it alone, so a
// tick or a refresh of a screen gone does not reach the next. Bubble Tea's
// messages go to the program as they are.
func (s *stage) wrap(cmd tea.Cmd, quit chan struct{}) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		switch msg := cmd().(type) {
		case tea.QuitMsg:
			return quitScreenMsg{quit: quit}
		case tea.BatchMsg:
			out := make(tea.BatchMsg, len(msg))
			for i, c := range msg {
				out[i] = s.wrap(c, quit)
			}
			return out
		case nil:
			return nil
		default:
			if strings.HasPrefix(reflect.TypeOf(msg).PkgPath(), "charm.land/") {
				return msg
			}
			return screenMsg{quit: quit, msg: msg}
		}
	}
}

func (s *stage) View() tea.View {
	if s.screen != nil {
		return s.screen.View()
	}
	// Between two screens: the same colors, nothing on them.
	v := tea.NewView("")
	v.AltScreen = true
	v.BackgroundColor = colorBg
	v.ForegroundColor = colorFg
	return v
}
