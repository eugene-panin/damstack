package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/ops"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/ui/tui"
)

// screen is whether a command may take the terminal over for its TUI: both
// ends a terminal, someone to answer, and not plain, for --plain or --verbose
// (whose tool output goes to the terminal as it comes).
func (t *terminal) screen(plain bool) bool {
	return t.tty() && !plain && !t.prompt.NoTerminal
}

// show runs a screen until it quits: on the home screen's session, so the
// terminal stays with damstack between two screens, else on its own program.
func (s *cli) show(ctx context.Context, model tea.Model) error {
	if s.session != nil {
		return s.session.Show(ctx, model)
	}
	_, err := tea.NewProgram(model, tea.WithInput(s.in), tea.WithOutput(s.out), tea.WithContext(ctx)).Run()
	return err
}

// run runs cmd on the terminal, such as ssh or an editor: a session lets go
// of it meanwhile and takes it back after.
func (s *cli) run(cmd *exec.Cmd) error {
	if s.session != nil {
		return s.session.Exec(cmd)
	}
	return cmd.Run()
}

// onScreen runs work while the deploy screen shows it, when the terminal
// allows and plain does not ask for lines; otherwise it just runs work with
// ui, its events as plain lines. stop is set when the person asks to stop
// after the step running now.
//
// While the screen is up, work is given the screen's UI: its events, its
// questions, and any stray output, as notes, go to the screen.
func onScreen(ctx context.Context, s *cli, ui lineUI, plain bool, p *project.Project, m *manifest.Manifest, work func(context.Context, ops.UI, <-chan struct{}) error) error {
	if !s.screen(plain) {
		return work(ctx, ui, nil)
	}
	tui.UseProfile(colorprofile.Detect(s.out, os.Environ()))
	// The failure screen's r runs the work again, on a new screen.
	for {
		retry, err := screenRound(ctx, s, ui.yes, p, m, work)
		if !retry {
			return err
		}
	}
}

// screenRound runs work once on the deploy screen, yes, --yes, answering its
// questions; retry is whether the person asked to run it again.
func screenRound(ctx context.Context, s *cli, yes bool, p *project.Project, m *manifest.Manifest, work func(context.Context, ops.UI, <-chan struct{}) error) (bool, error) {
	stop := make(chan struct{})
	var stopOnce sync.Once
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	model := tui.NewDeploy(tui.Actions{
		Stop:  func() { stopOnce.Do(func() { close(stop) }) },
		Abort: cancel,
		SSH: func() *exec.Cmd {
			return exec.Command(self(), "ssh", p.Meta.Name)
		},
		Open:  openURL,
		Check: check,
		CopyToken: func(name string) error {
			value, err := projectToken(p, m, name)
			if err != nil {
				return err
			}
			return copyText(value)
		},
		QR: func() *exec.Cmd {
			client := firstDevice(p)
			return exec.Command("sh", "-c", `"$0" tunnel show "$2" -p "$1" --qr; printf "\nEnter to go back "; read _`, self(), p.Meta.Name, client)
		},
		Copy: copyText,
	})
	screen := tui.NewProgram(model, s.in, s.out)
	if s.session != nil {
		screen = s.session.Program(model)
	}
	ui := screenUI{screen: screen, out: screen.Writer(), err: screen.Writer(), prompt: s.prompt, yes: yes}

	done := make(chan error, 1)
	go func() {
		var err error
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("damstack failed inside: %v\n%s", r, debug.Stack())
			}
			screen.Finish(err)
			done <- err
		}()
		err = work(ctx, ui, stop)
	}()
	shown := screen.Run()
	if shown != nil {
		cancel() // the screen failed: the work must not go on unseen
	}
	err := <-done
	if err != nil && model.Retry() {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, shown
}

// check says whether an address answers: its HTTP status, or why not.
func check(link string) string {
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(link)
	var dnsErr *net.DNSError
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return "no DNS record"
	case err != nil:
		return "no answer"
	}
	resp.Body.Close()
	return strconv.Itoa(resp.StatusCode)
}

// openURL opens an address in the browser of this machine.
func openURL(link string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	return exec.Command(name, link).Start()
}

// self is the path of this damstack, to run it again.
func self() string {
	if path, err := os.Executable(); err == nil {
		return path
	}
	return os.Args[0]
}

// firstDevice is the first device of the project's private network.
func firstDevice(p *project.Project) string {
	config, _ := p.Config()
	if network, ok := config["network"].(map[string]any); ok {
		if clients, ok := network["clients"].([]any); ok && len(clients) > 0 {
			return fmt.Sprint(clients[0])
		}
	}
	return "laptop"
}
