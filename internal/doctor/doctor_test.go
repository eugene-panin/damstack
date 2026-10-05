package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugene-panin/damstack/internal/login"
	"github.com/eugene-panin/damstack/internal/release"
)

type exitError int

func (e exitError) Error() string { return "exit status" }
func (e exitError) ExitCode() int { return int(e) }

type reply struct {
	out string
	err error
}

type fakeFile struct{ fs.FileInfo }

// machine is a healthy Mac with git, the toolbox reachable, an SSH key
// without a passphrase and WireGuard installed; each test breaks one thing.
type machine struct {
	goos     string
	paths    map[string]bool
	commands map[string]reply
	env      map[string]string
	head     func() (int, error)
	dial     error
	project  *Project
	other    error
}

func healthy() *machine {
	return &machine{
		goos: "darwin",
		paths: map[string]bool{
			"/home/u/.ssh/id_ed25519.pub":                       true,
			"/home/u/.ssh/id_ed25519":                           true,
			"/Applications/WireGuard.app":                       true,
			"/home/u/.config/damstack/projects/demo/vault-pass": true,
		},
		commands: map[string]reply{
			"git --version": {out: "git version 2.50.1 (Apple Git-155)\n"},
			"ssh-keygen -y -P  -f /home/u/.ssh/id_ed25519": {out: "ssh-ed25519 AAAA\n"},
		},
		env:  map[string]string{},
		head: func() (int, error) { return 200, nil },
	}
}

func (m *machine) Env() Env {
	return Env{
		GOOS:     m.goos,
		Home:     "/home/u",
		Getenv:   func(k string) string { return m.env[k] },
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			r, ok := m.commands[strings.Join(append([]string{name}, args...), " ")]
			if !ok {
				return []byte("unexpected command"), exitError(127)
			}
			return []byte(r.out), r.err
		},
		Stat: func(path string) (fs.FileInfo, error) {
			if m.paths[path] {
				return fakeFile{}, nil
			}
			return nil, fs.ErrNotExist
		},
		Dial: func(context.Context, string, string) (net.Conn, error) {
			if m.dial != nil {
				return nil, m.dial
			}
			client, server := net.Pipe()
			server.Close()
			return client, nil
		},
		Head: func(context.Context, string) (int, error) { return m.head() },
		SameServer: func(context.Context, string, string, string) error {
			return m.other
		},
		Project: m.project,
	}
}

func find(results []Result, text string) (Result, bool) {
	for _, r := range results {
		if strings.Contains(r.Text, text) {
			return r, true
		}
	}
	return Result{}, false
}

func TestHealthyMachineHasNothingToFix(t *testing.T) {
	results := Run(t.Context(), healthy().Env())
	for _, r := range results {
		if r.Status != OK {
			t.Errorf("%s: %v %q", r.Group, r.Status, r.Text)
		}
	}
	var out bytes.Buffer
	if Print(&out, results) || !strings.Contains(out.String(), "Everything damstack needs is here.") {
		t.Errorf("Print on a healthy machine:\n%s", out.String())
	}
}

func TestProblems(t *testing.T) {
	tests := []struct {
		name   string
		break_ func(*machine)
		text   string
		status Status
		fix    string
	}{
		{
			name:   "not a Mac",
			break_: func(m *machine) { m.goos = "linux" },
			text:   "damstack runs on macOS", status: Fail, fix: "on a Mac",
		},
		{
			name: "no command line tools of Xcode",
			break_: func(m *machine) {
				m.commands["git --version"] = reply{"xcrun: error: invalid active developer path", exitError(1)}
			},
			text: "git does not run", status: Fail, fix: "xcode-select --install",
		},
		{
			name:   "github unreachable before the toolbox is downloaded",
			break_: func(m *machine) { m.head = func() (int, error) { return 0, errors.New("dial tcp: no route to host") } },
			text:   "github.com, where the toolbox is, does not answer", status: Fail, fix: "internet connection",
		},
		{
			name:   "public key without its private half",
			break_: func(m *machine) { delete(m.paths, "/home/u/.ssh/id_ed25519") },
			text:   "only the public half of the key is here", status: Fail, fix: "copy the private key back",
		},
		{
			name:   "key with a passphrase and no agent",
			break_: func(m *machine) { delete(m.commands, "ssh-keygen -y -P  -f /home/u/.ssh/id_ed25519") },
			text:   "no ssh-agent runs", status: Fail, fix: "ssh-add ~/.ssh/id_ed25519",
		},
		{
			name: "key with a passphrase, agent without it",
			break_: func(m *machine) {
				delete(m.commands, "ssh-keygen -y -P  -f /home/u/.ssh/id_ed25519")
				m.env["SSH_AUTH_SOCK"] = "/tmp/agent"
				m.commands["ssh-add -l"] = reply{"The agent has no identities.", exitError(1)}
			},
			text: "ssh-agent does not hold it", status: Fail, fix: "ssh-add",
		},
		{
			name:   "no wireguard on macOS",
			break_: func(m *machine) { delete(m.paths, "/Applications/WireGuard.app") },
			text:   "WireGuard is not installed", status: Warn, fix: "App Store",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := healthy()
			tc.break_(m)
			results := Run(t.Context(), m.Env())
			r, ok := find(results, tc.text)
			if !ok {
				t.Fatalf("no result with %q in %+v", tc.text, results)
			}
			if r.Status != tc.status || !strings.Contains(r.Fix, tc.fix) {
				t.Errorf("got %v %q, fix %q; want %v, fix with %q", r.Status, r.Text, r.Fix, tc.status, tc.fix)
			}
			var out bytes.Buffer
			if got := Print(&out, results); got != (tc.status == Fail) {
				t.Errorf("Print reported failure %v, want %v", got, tc.status == Fail)
			}
			if tc.fix != "" && !strings.Contains(out.String(), tc.fix) {
				t.Errorf("the fix is not under To do:\n%s", out.String())
			}
		})
	}
}

func TestTheToolbox(t *testing.T) {
	m := healthy()
	if r, _ := find(Run(t.Context(), m.Env()), "is downloaded on first use"); r.Status != OK {
		t.Errorf("not downloaded: %+v", r)
	}
	cache := t.TempDir()
	os.MkdirAll(filepath.Join(cache, "toolbox", release.Toolbox), 0o755)
	os.WriteFile(filepath.Join(cache, "toolbox", release.Toolbox, "VERSION"), nil, 0o644)
	env := m.Env()
	env.Cache = cache
	if r, _ := find(Run(t.Context(), env), release.Toolbox+" is downloaded"); r.Status != OK || strings.Contains(r.Text, "first use") {
		t.Errorf("downloaded: %+v", r)
	}
}

func TestProject(t *testing.T) {
	m := healthy()
	m.project = &Project{Dir: "/home/u/damstack/demo", Password: "/home/u/.config/damstack/projects/demo/vault-pass", Tunnel: "10.77.0.1"}

	results := Run(t.Context(), m.Env())
	if r, ok := find(results, "the server answers at 10.77.0.1"); !ok || r.Status != OK {
		t.Errorf("tunnel: %+v", r)
	}

	m.project.Public, m.project.KnownHosts = "34.1.2.3", "/home/u/damstack/demo/.damstack/known_hosts"
	m.other = fmt.Errorf("10.77.0.1:22: %w", login.ErrOtherServer)
	if r, _ := find(Run(t.Context(), m.Env()), "answers another server than 34.1.2.3"); r.Status != Fail {
		t.Errorf("another server through the tunnel: %+v", r)
	}
	m.other = nil

	delete(m.paths, "/home/u/.config/damstack/projects/demo/vault-pass")
	m.dial = &net.OpError{Op: "dial", Err: errors.New("i/o timeout")}
	results = Run(t.Context(), m.Env())
	if r, _ := find(results, "no vault password at ~/.config/damstack/projects/demo/vault-pass"); r.Status != Fail {
		t.Errorf("vault password: %+v", r)
	}
	if r, _ := find(results, "the server does not answer at 10.77.0.1"); r.Status != Warn || !strings.Contains(r.Fix, "WireGuard") {
		t.Errorf("tunnel down: %+v", r)
	}
}

func TestPrintListsAFailureWithoutAFix(t *testing.T) {
	var out bytes.Buffer
	failed := Print(&out, []Result{
		{Group: "Tools", Status: OK, Text: "git version 2.50.1"},
		{Group: "Project ~/demo", Status: Fail, Text: "network.cidr in stack.yaml: bad prefix"},
	})
	todo := out.String()[strings.Index(out.String(), "To do:"):]
	if !failed || !strings.Contains(todo, "1. network.cidr in stack.yaml: bad prefix") {
		t.Errorf("failed=%v, output:\n%s", failed, out.String())
	}
}
