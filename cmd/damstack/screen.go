package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/eugene-panin/damstack/internal/event"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/ui/tui"
)

// screen is whether a command may take the terminal over for its TUI: both
// ends a terminal, someone to answer, and neither --plain nor --verbose (whose
// tool output goes to the terminal as it comes).
func (s *streams) screen() bool {
	return s.tty() && !s.plain && !s.verbose && !s.prompt.NoTerminal
}

// show runs a screen until it quits: on the home screen's session, so the
// terminal stays with damstack between two screens, else on its own program.
func (s *streams) show(ctx context.Context, model tea.Model) error {
	if s.session != nil {
		return s.session.Show(ctx, model)
	}
	_, err := tea.NewProgram(model, tea.WithInput(s.in), tea.WithOutput(s.out), tea.WithContext(ctx)).Run()
	return err
}

// run runs cmd on the terminal, such as ssh or an editor: a session lets go
// of it meanwhile and takes it back after.
func (s *streams) run(cmd *exec.Cmd) error {
	if s.session != nil {
		return s.session.Exec(cmd)
	}
	return cmd.Run()
}

// onScreen runs work while the deploy screen shows it, when the terminal
// allows; otherwise it just runs work, its events as plain lines. stop is set
// when the person asks to stop after the step running now.
//
// While the screen is up, what work says goes to it: its events, its
// questions, and any stray output, as notes. The streams are as they were
// once work returns.
func onScreen(ctx context.Context, s *streams, p *project.Project, m *manifest.Manifest, work func(context.Context, *atomic.Bool) error) error {
	if !s.screen() {
		var stop atomic.Bool
		return work(ctx, &stop)
	}
	tui.UseProfile(colorprofile.Detect(s.out, os.Environ()))
	// The failure screen's r runs the work again, on a new screen.
	for {
		retry, err := screenRound(ctx, s, p, m, work)
		if !retry {
			return err
		}
	}
}

// screenRound runs work once on the deploy screen; retry is whether the person
// asked to run it again.
func screenRound(ctx context.Context, s *streams, p *project.Project, m *manifest.Manifest, work func(context.Context, *atomic.Bool) error) (bool, error) {
	var stop atomic.Bool
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	model := tui.NewDeploy(tui.Actions{
		Stop:  func() { stop.Store(true) },
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

	saved := *s
	s.events, s.ask, s.planner = screen, screen.Confirm, screen.Approve
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
	err := <-done
	if err != nil && model.Retry() {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, shown
}

var urlRe = regexp.MustCompile(`https?://[^\s,;)]+`)

// runDone says what a deploy of p set up: the addresses in the done lines of
// its platform and of its apps, the admin pages first, and what to run next.
func runDone(p *project.Project, m *manifest.Manifest) event.RunDone {
	config, _ := p.Config()
	infra, _ := config["infra_domain"].(string)
	var admin, apps []event.Link
	seen := map[string]bool{}
	add := func(lines []string) {
		for _, l := range lines {
			for _, raw := range urlRe.FindAllString(l, -1) {
				link := strings.TrimRight(raw, ".")
				u, err := url.Parse(link)
				if err != nil || seen[link] {
					continue
				}
				seen[link] = true
				if infra != "" && strings.HasSuffix(u.Hostname(), "."+infra) {
					token, _, _ := strings.Cut(u.Hostname(), ".")
					if _, ok := m.Tokens[token]; !ok {
						token = ""
					}
					admin = append(admin, event.Link{URL: link, Admin: true, Token: token})
				} else {
					apps = append(apps, event.Link{URL: link})
				}
			}
		}
	}
	add(doneLines(p, m, ""))
	for _, ref := range p.Meta.Apps {
		if am, _ := cachedRef(ref); am != nil {
			add(doneLines(p, am, ref.Name))
		}
	}
	run := event.RunDone{Links: append(admin, apps...)}
	if network, ok := config["network"].(map[string]any); ok {
		if clients, ok := network["clients"].([]any); ok && len(clients) > 0 {
			run.Next = append(run.Next, event.NextStep{Command: fmt.Sprintf("damstack tunnel show %v", clients[0]),
				Why: "the WireGuard config, as a QR code too"})
		}
	}
	if m.Backup != nil {
		run.Next = append(run.Next, event.NextStep{Command: "damstack backup status", Why: "what is backed up, and where"})
	}
	return run
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

// approve asks whether to apply a plan: on the screen when it is up, else as
// lines, the plan's question and, when it needs allocations replaced, a second
// one. --yes says yes to both.
func (s *streams) approve(p event.Plan) (event.Approval, error) {
	if s.yes {
		return event.Approval{Apply: true, Restart: true}, nil
	}
	if s.planner != nil {
		return s.planner(p)
	}
	ok, err := s.confirm(p.Question, false)
	if err != nil || !ok {
		return event.Approval{}, err
	}
	a := event.Approval{Apply: true}
	if len(p.Restarts) > 0 {
		groups := make([]string, len(p.Restarts))
		for i, r := range p.Restarts {
			groups[i] = r.Job + "/" + r.Group
		}
		s.events.Emit(event.Note{Break: true, Text: "Nomad changes " + strings.Join(groups, ", ") + " in place: " +
			"running allocations keep their old network and sidecar, so the change takes effect only in new ones."})
		if a.Restart, err = s.confirm("Replace those allocations after the apply, one at a time?", true); err != nil {
			return event.Approval{}, err
		}
	}
	return a, nil
}
