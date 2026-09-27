package doctor

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net"
	"strings"
	"testing"

	"github.com/eugene-panin/damstack/internal/project"
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

// machine is a healthy macOS host with Docker Desktop running, the image
// pulled, an SSH key without a passphrase and WireGuard installed; each test
// breaks one thing.
type machine struct {
	goos     string
	paths    map[string]bool
	commands map[string]reply
	env      map[string]string
	head     func() (int, error)
	dial     error
	project  *project.Project
}

func healthy() *machine {
	return &machine{
		goos: "darwin",
		paths: map[string]bool{
			"/usr/local/bin/docker":             true,
			"/home/u/.ssh/id_ed25519.pub":       true,
			"/home/u/.ssh/id_ed25519":           true,
			"/Applications/WireGuard.app":       true,
			"/home/u/.config/demo/vault-pass":   true,
			"/work/demo/secrets/cloudflare.env": true,
		},
		commands: map[string]reply{
			"docker version --format {{.Server.Version}}":                                         {out: "29.8.0\n"},
			"docker info --format {{.NCPU}}|{{.MemTotal}}|{{.Architecture}}|{{.OperatingSystem}}": {out: "8|8589934592|aarch64|Docker Desktop\n"},
			"docker image inspect --format {{.Id}} " + release.ImageRef():                         {out: "sha256:abc\n"},
			"ssh-keygen -y -P  -f /home/u/.ssh/id_ed25519":                                        {out: "ssh-ed25519 AAAA\n"},
		},
		env:  map[string]string{},
		head: func() (int, error) { return 401, nil },
	}
}

func (m *machine) Env() Env {
	return Env{
		GOOS:   m.goos,
		Home:   "/home/u",
		Getenv: func(k string) string { return m.env[k] },
		LookPath: func(name string) (string, error) {
			if name == "docker" && m.paths["/usr/local/bin/docker"] {
				return "/usr/local/bin/docker", nil
			}
			if name == "wg-quick" && m.paths["/usr/bin/wg-quick"] {
				return "/usr/bin/wg-quick", nil
			}
			return "", errors.New("not found")
		},
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
		Head:    func(context.Context, string) (int, error) { return m.head() },
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
			name:   "no docker on macOS",
			break_: func(m *machine) { delete(m.paths, "/usr/local/bin/docker") },
			text:   "Docker is not installed", status: Fail, fix: "brew install --cask docker",
		},
		{
			name:   "no docker on Linux",
			break_: func(m *machine) { m.goos = "linux"; delete(m.paths, "/usr/local/bin/docker") },
			text:   "Docker is not installed", status: Fail, fix: "docs.docker.com/engine/install",
		},
		{
			name: "docker not running",
			break_: func(m *machine) {
				m.commands["docker version --format {{.Server.Version}}"] = reply{"Cannot connect to the Docker daemon", exitError(1)}
			},
			text: "installed but not running", status: Fail, fix: "open Docker Desktop, or run colima start",
		},
		{
			name: "no permission on the docker socket",
			break_: func(m *machine) {
				m.goos = "linux"
				m.commands["docker version --format {{.Server.Version}}"] = reply{"permission denied while trying to connect", exitError(1)}
			},
			text: "this user may not use it", status: Fail, fix: "usermod -aG docker",
		},
		{
			name:   "old docker",
			break_: func(m *machine) { m.commands["docker version --format {{.Server.Version}}"] = reply{out: "20.10.24\n"} },
			text:   "is old", status: Warn, fix: "update Docker",
		},
		{
			name: "too little memory",
			break_: func(m *machine) {
				m.commands["docker info --format {{.NCPU}}|{{.MemTotal}}|{{.Architecture}}|{{.OperatingSystem}}"] = reply{out: "2|2147483648|aarch64|Docker Desktop\n"}
			},
			text: "Docker has 2.0 GB of memory", status: Warn, fix: "at least 4 GB",
		},
		{
			name: "unsupported architecture",
			break_: func(m *machine) {
				m.commands["docker info --format {{.NCPU}}|{{.MemTotal}}|{{.Architecture}}|{{.OperatingSystem}}"] = reply{out: "4|8589934592|riscv64|Ubuntu\n"}
			},
			text: "Docker runs on riscv64", status: Fail, fix: "ARM64",
		},
		{
			name:   "image not pulled yet",
			break_: func(m *machine) { delete(m.commands, "docker image inspect --format {{.Id}} "+release.ImageRef()) },
			text:   "is not downloaded yet", status: Warn, fix: "downloaded on first use",
		},
		{
			name: "image not pulled and ghcr.io unreachable",
			break_: func(m *machine) {
				delete(m.commands, "docker image inspect --format {{.Id}} "+release.ImageRef())
				m.head = func() (int, error) { return 0, errors.New("dial tcp: no route to host") }
			},
			text: "ghcr.io, where the tools image is, does not answer", status: Fail, fix: "internet connection",
		},
		{
			name:   "no ssh key",
			break_: func(m *machine) { delete(m.paths, "/home/u/.ssh/id_ed25519.pub") },
			text:   "no SSH key", status: Fail, fix: "ssh-keygen -t ed25519",
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

func TestDockerNotRunningSkipsTheImageCheck(t *testing.T) {
	m := healthy()
	m.commands["docker version --format {{.Server.Version}}"] = reply{"Cannot connect", exitError(1)}
	for _, r := range Run(t.Context(), m.Env()) {
		if r.Group == "Tools image" {
			t.Errorf("the image was checked with Docker down: %+v", r)
		}
	}
}

func TestProject(t *testing.T) {
	m := healthy()
	m.project = &project.Project{Dir: "/work/demo"}
	m.project.Stack.Network.CIDR = "10.77.0.0/24"

	results := Run(t.Context(), m.Env())
	if r, ok := find(results, "the server answers at 10.77.0.1"); !ok || r.Status != OK {
		t.Errorf("tunnel: %+v", r)
	}

	delete(m.paths, "/home/u/.config/demo/vault-pass")
	m.dial = &net.OpError{Op: "dial", Err: errors.New("i/o timeout")}
	results = Run(t.Context(), m.Env())
	if r, _ := find(results, "no vault password at ~/.config/demo/vault-pass"); r.Status != Fail {
		t.Errorf("vault password: %+v", r)
	}
	if r, _ := find(results, "the server does not answer at 10.77.0.1"); r.Status != Warn || !strings.Contains(r.Fix, "WireGuard") {
		t.Errorf("tunnel down: %+v", r)
	}
}

func TestPrintListsAFailureWithoutAFix(t *testing.T) {
	var out bytes.Buffer
	failed := Print(&out, []Result{
		{Group: "Docker", Status: OK, Text: "Docker 29.8.0 is running"},
		{Group: "Project ~/demo", Status: Fail, Text: "network.cidr in stack.yaml: bad prefix"},
	})
	todo := out.String()[strings.Index(out.String(), "To do:"):]
	if !failed || !strings.Contains(todo, "1. network.cidr in stack.yaml: bad prefix") {
		t.Errorf("failed=%v, output:\n%s", failed, out.String())
	}
}
