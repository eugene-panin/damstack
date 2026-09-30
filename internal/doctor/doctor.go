// Package doctor checks that a machine has what damstack needs, and says how
// to get what it lacks.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/eugene-panin/damstack/internal/login"
	"github.com/eugene-panin/damstack/internal/release"
)

type Status int

const (
	OK Status = iota + 1
	Warn
	Fail
)

type Result struct {
	Group  string
	Status Status
	Text   string
	Fix    string
}

// Env is what the checks read from the machine; tests replace any of it.
type Env struct {
	GOOS     string
	Home     string
	Getenv   func(string) string
	LookPath func(string) (string, error)
	Run      func(ctx context.Context, name string, args ...string) ([]byte, error)
	Stat     func(string) (fs.FileInfo, error)
	Dial     func(ctx context.Context, network, address string) (net.Conn, error)
	Head     func(ctx context.Context, url string) (int, error)
	// SameServer checks that the SSH server at tunnel is the one known_hosts
	// knows for public.
	SameServer func(ctx context.Context, knownHosts, public, tunnel string) error
	Project    *Project
}

// Project is what doctor checks of the project it runs in: that its vault
// password is there, and that the server answers at Tunnel, its address over
// the private network, when the stack names one.
type Project struct {
	Dir        string
	Password   string
	Tunnel     string
	Public     string
	KnownHosts string
}

func Host(p *Project) Env {
	home, _ := os.UserHomeDir()
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	client := &http.Client{Timeout: 10 * time.Second}
	return Env{
		GOOS:     runtime.GOOS,
		Home:     home,
		Getenv:   os.Getenv,
		LookPath: exec.LookPath,
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
		Stat: os.Stat,
		Dial: dialer.DialContext,
		Head: func(ctx context.Context, url string) (int, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
			if err != nil {
				return 0, err
			}
			resp, err := client.Do(req)
			if err != nil {
				return 0, err
			}
			resp.Body.Close()
			return resp.StatusCode, nil
		},
		SameServer: login.SameServer,
		Project:    p,
	}
}

// Run runs every check. Docker checks stop at the first failure, since the
// ones after it would only fail for the same reason.
func Run(ctx context.Context, env Env) []Result {
	var results []Result
	docker := checkDocker(ctx, env)
	results = append(results, docker...)
	if !failed(docker) {
		results = append(results, checkImage(ctx, env))
	}
	results = append(results, checkSSH(ctx, env)...)
	results = append(results, checkWireGuard(env))
	if env.Project != nil {
		results = append(results, checkProject(ctx, env)...)
	}
	return results
}

func failed(results []Result) bool {
	for _, r := range results {
		if r.Status == Fail {
			return true
		}
	}
	return false
}

func checkDocker(ctx context.Context, env Env) []Result {
	const group = "Docker"
	if _, err := env.LookPath("docker"); err != nil {
		fix := "install Docker Engine: https://docs.docker.com/engine/install/"
		if env.GOOS == "darwin" {
			fix = "install Docker Desktop (brew install --cask docker) and open it once, " +
				"or Colima (brew install colima docker, then colima start)"
		}
		return []Result{{group, Fail, "Docker is not installed", fix}}
	}

	out, err := env.Run(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	if err != nil {
		text, fix := "Docker is installed but not running", "start it: sudo systemctl start docker"
		switch {
		case strings.Contains(string(out), "permission denied"):
			text, fix = "Docker runs, but this user may not use it",
				"add yourself to the docker group (sudo usermod -aG docker $USER), then log out and in"
		case env.GOOS == "darwin":
			fix = "open Docker Desktop, or run colima start"
		}
		return []Result{{group, Fail, text, fix}}
	}
	version := strings.TrimSpace(string(out))
	results := []Result{{group, OK, "Docker " + version + " is running", ""}}
	if major, _, _ := strings.Cut(version, "."); atoi(major) < 24 {
		results = append(results, Result{group, Warn, "Docker " + version + " is old; damstack is tested with 24 and later", "update Docker"})
	}

	info, err := env.Run(ctx, "docker", "info", "--format", "{{.NCPU}}|{{.MemTotal}}|{{.Architecture}}|{{.OperatingSystem}}")
	if err != nil {
		return append(results, Result{group, Warn, "docker info failed: " + firstLine(info), ""})
	}
	fields := strings.Split(strings.TrimSpace(string(info)), "|")
	if len(fields) < 4 {
		return append(results, Result{group, Warn, "docker info gave " + string(info), ""})
	}
	cpus, memory, arch, system := atoi(fields[0]), int64(atoi(fields[1])), fields[2], fields[3]
	gib := float64(memory) / (1 << 30)
	results = append(results, Result{group, OK, fmt.Sprintf("%s, %d CPUs, %.1f GB of memory, %s", system, cpus, gib, arch), ""})
	if arch != "x86_64" && arch != "aarch64" {
		results = append(results, Result{group, Fail, "Docker runs on " + arch + "; the tools image exists for x86_64 and aarch64 only",
			"use a machine with an Intel, AMD or ARM64 processor"})
	}
	if gib < 3.5 {
		fix := "give Docker at least 4 GB"
		if env.GOOS == "darwin" {
			fix += ": Docker Desktop, Settings, Resources; or colima start --memory 4"
		}
		results = append(results, Result{group, Warn, fmt.Sprintf("Docker has %.1f GB of memory", gib), fix})
	}
	return results
}

func checkImage(ctx context.Context, env Env) Result {
	const group = "Tools image"
	ref := release.ImageRef()
	if _, err := env.Run(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", ref); err == nil {
		return Result{group, OK, ref + " is downloaded", ""}
	}
	status, err := env.Head(ctx, "https://ghcr.io/v2/")
	if err != nil || (status != http.StatusOK && status != http.StatusUnauthorized) {
		return Result{group, Fail, "ghcr.io, where the tools image is, does not answer",
			"check the internet connection, or the proxy Docker uses"}
	}
	return Result{group, Warn, ref + " is not downloaded yet",
		"nothing to do: it is downloaded on first use, about 1 GB"}
}

// KeyNames are the SSH keys in ~/.ssh damstack uses, the first there.
var KeyNames = []string{"id_ed25519", "id_ecdsa", "id_rsa"}

func checkSSH(ctx context.Context, env Env) []Result {
	const group = "SSH"
	var results []Result
	key := ""
	for _, name := range KeyNames {
		if path := filepath.Join(env.Home, ".ssh", name+".pub"); exists(env, path) {
			key = path
			break
		}
	}
	if key == "" {
		results = append(results, Result{group, Fail, "no SSH key in ~/.ssh",
			"create one: ssh-keygen -t ed25519, and press Enter at every question"})
	} else {
		results = append(results, Result{group, OK, "key " + tilde(env.Home, key), ""})
	}

	if key == "" {
		return results
	}
	private := strings.TrimSuffix(key, ".pub")
	if !exists(env, private) {
		return append(results, Result{group, Fail, "only the public half of the key is here, " + tilde(env.Home, private) + " is missing",
			"copy the private key back from where you keep it, or create a new pair: ssh-keygen -t ed25519"})
	}
	if _, err := env.Run(ctx, "ssh-keygen", "-y", "-P", "", "-f", private); err == nil {
		return append(results, Result{group, OK, "the key has no passphrase, so no ssh-agent is needed", ""})
	}
	if env.Getenv("SSH_AUTH_SOCK") == "" {
		return append(results, Result{group, Fail, "the key has a passphrase and no ssh-agent runs",
			"start ssh-agent and add the key: ssh-add " + tilde(env.Home, private)})
	}
	_, err := env.Run(ctx, "ssh-add", "-l")
	var exit interface{ ExitCode() int }
	switch {
	case err == nil:
		results = append(results, Result{group, OK, "ssh-agent holds a key", ""})
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		results = append(results, Result{group, Fail, "the key has a passphrase and ssh-agent does not hold it", "add it: ssh-add " + tilde(env.Home, private)})
	default:
		results = append(results, Result{group, Warn, "ssh-agent does not answer", "restart it, or unset SSH_AUTH_SOCK"})
	}
	return results
}

func checkWireGuard(env Env) Result {
	const group = "WireGuard"
	if env.GOOS == "darwin" && exists(env, "/Applications/WireGuard.app") {
		return Result{group, OK, "the WireGuard app is installed", ""}
	}
	if _, err := env.LookPath("wg-quick"); err == nil {
		return Result{group, OK, "wg-quick is installed", ""}
	}
	fix := "install wireguard-tools: sudo apt install wireguard-tools, or your distribution's package"
	if env.GOOS == "darwin" {
		fix = "install the WireGuard app from the App Store: https://apps.apple.com/app/wireguard/id1451685025"
	}
	return Result{group, Warn, "WireGuard is not installed; the admin pages open only through it", fix}
}

func checkProject(ctx context.Context, env Env) []Result {
	p := env.Project
	group := "Project " + tilde(env.Home, p.Dir)
	var results []Result

	if exists(env, p.Password) {
		results = append(results, Result{group, OK, "vault password " + tilde(env.Home, p.Password), ""})
	} else {
		results = append(results, Result{group, Fail, "no vault password at " + tilde(env.Home, p.Password),
			"copy it there from where you keep it; nothing in this project can be decrypted without it"})
	}

	if p.Tunnel == "" {
		return results
	}
	conn, err := env.Dial(ctx, "tcp", net.JoinHostPort(p.Tunnel, "22"))
	if err != nil {
		return append(results, Result{group, Warn, "the server does not answer at " + p.Tunnel,
			"turn the WireGuard tunnel on; before the server is set up this is expected"})
	}
	conn.Close()
	if p.Public != "" && p.KnownHosts != "" {
		if err := env.SameServer(ctx, p.KnownHosts, p.Public, net.JoinHostPort(p.Tunnel, "22")); errors.Is(err, login.ErrOtherServer) {
			return append(results, Result{group, Fail, "at " + p.Tunnel + " answers another server than " + p.Public,
				"the tunnel of another project is on: turn it off, and this project's on"})
		}
	}
	return append(results, Result{group, OK, "the server answers at " + p.Tunnel + " through WireGuard", ""})
}

// PrintBrief writes the results as one list, then, when something is wrong,
// what to fix, and reports whether anything failed.
func PrintBrief(w io.Writer, results []Result) bool {
	fmt.Fprintln(w, "This machine")
	var fixes []Result
	for _, r := range results {
		fmt.Fprintf(w, "  %-4s  %s\n", label(r.Status), r.Text)
		if r.Status != OK && r.Fix != "" {
			fixes = append(fixes, r)
		}
	}
	if len(fixes) > 0 {
		fmt.Fprintln(w, "\nBefore anything else")
		for i, r := range fixes {
			fmt.Fprintf(w, "  %d. %s\n", i+1, capitalize(r.Fix))
		}
	}
	return failed(results)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Print writes the results grouped, then what to fix, and reports whether
// anything failed.
func Print(w io.Writer, results []Result) bool {
	group := ""
	var fixes []Result
	for _, r := range results {
		if r.Group != group {
			if group != "" {
				fmt.Fprintln(w)
			}
			fmt.Fprintln(w, r.Group)
			group = r.Group
		}
		fmt.Fprintf(w, "  %-4s  %s\n", label(r.Status), r.Text)
		if r.Status != OK {
			fixes = append(fixes, r)
		}
	}
	fmt.Fprintln(w)
	if len(fixes) == 0 {
		fmt.Fprintln(w, "Everything damstack needs is here.")
		return false
	}
	fmt.Fprintln(w, "To do:")
	for i, r := range fixes {
		if r.Fix == "" {
			fmt.Fprintf(w, "  %d. %s\n", i+1, r.Text)
			continue
		}
		fmt.Fprintf(w, "  %d. %s: %s\n", i+1, r.Text, r.Fix)
	}
	return failed(results)
}

func label(s Status) string {
	switch s {
	case OK:
		return "ok"
	case Warn:
		return "warn"
	default:
		return "FAIL"
	}
}

func exists(env Env, path string) bool {
	_, err := env.Stat(path)
	return err == nil
}

func tilde(home, path string) string {
	if rel, err := filepath.Rel(home, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.Join("~", rel)
	}
	return path
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func firstLine(b []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return line
}
