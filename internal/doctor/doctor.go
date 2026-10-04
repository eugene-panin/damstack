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
	"strings"
	"time"

	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/login"
	"github.com/eugene-panin/damstack/internal/release"
	"github.com/eugene-panin/damstack/internal/toolbox"
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
	GOOS string
	Home string
	// Cache is the cache directory of damstack, where the toolbox is.
	Cache    string
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
	cache, _ := config.CacheDir()
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	client := &http.Client{Timeout: 10 * time.Second}
	return Env{
		GOOS:     runtime.GOOS,
		Home:     home,
		Cache:    cache,
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

// Run runs every check. The tools are checked only on a Mac, the one
// system damstack runs on.
func Run(ctx context.Context, env Env) []Result {
	var results []Result
	mac := checkMac(env)
	results = append(results, mac)
	if mac.Status == OK {
		results = append(results, checkGit(ctx, env), checkToolbox(ctx, env))
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

func checkMac(env Env) Result {
	if env.GOOS != "darwin" {
		return Result{"This Mac", Fail, "damstack runs on macOS, and this is " + env.GOOS, "run damstack on a Mac"}
	}
	return Result{"This Mac", OK, "macOS", ""}
}

func checkGit(ctx context.Context, env Env) Result {
	const group = "Tools"
	out, err := env.Run(ctx, "git", "--version")
	if err != nil {
		return Result{group, Fail, "git does not run: " + firstLine(out),
			"install the command line tools of Xcode: xcode-select --install, and agree to their license"}
	}
	return Result{group, OK, firstLine(out), ""}
}

func checkToolbox(ctx context.Context, env Env) Result {
	const group = "Tools"
	name := "the toolbox " + release.Toolbox
	if env.Cache != "" && toolbox.Have(env.Cache, release.Toolbox) {
		return Result{group, OK, name + " is downloaded", ""}
	}
	status, err := env.Head(ctx, "https://github.com/")
	if err != nil || status >= 500 {
		return Result{group, Fail, "github.com, where the toolbox is, does not answer",
			"check the internet connection, or the proxy"}
	}
	return Result{group, OK, name + " is downloaded on first use, about 90 MB", ""}
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
	if exists(env, "/Applications/WireGuard.app") {
		return Result{group, OK, "the WireGuard app is installed", ""}
	}
	return Result{group, Warn, "WireGuard is not installed; the admin pages open only through it",
		"install the WireGuard app from the App Store: https://apps.apple.com/app/wireguard/id1451685025"}
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

func firstLine(b []byte) string {
	line, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return line
}
