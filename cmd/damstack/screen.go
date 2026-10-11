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

// onScreen runs work while the deploy screen shows it, when the terminal
// allows; otherwise it just runs work, its events as plain lines. stop is set
// when the person asks to stop after the step running now.
//
// While the screen is up, what work says goes to it: its events, its
// questions, and any stray output, as notes. The streams are as they were
// once work returns.
func onScreen(ctx context.Context, s *streams, p *project.Project, m *manifest.Manifest, work func(context.Context, *atomic.Bool) error) error {
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
