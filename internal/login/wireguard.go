package login

import (
	"context"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// WireGuard is a tunnel of the WireGuard app that is on, and the endpoint it
// goes to.
type WireGuard struct {
	Name     string
	Endpoint string
}

var (
	ncService = regexp.MustCompile(`^\*?\s*\(Connected\)\s+(\S+)\s+VPN \(com\.wireguard\.macos\)\s+"([^"]*)"`)
	ncRemote  = regexp.MustCompile(`(?m)^\s*RemoteAddress\s*:\s*(\S+)`)
)

// WireGuardOn lists the tunnels of the WireGuard app that are on, and
// whether it could tell: on macOS only.
func WireGuardOn(ctx context.Context) ([]WireGuard, bool) {
	if runtime.GOOS != "darwin" {
		return nil, false
	}
	out, err := exec.CommandContext(ctx, "scutil", "--nc", "list").Output()
	if err != nil {
		return nil, false
	}
	var on []WireGuard
	for _, s := range connected(string(out)) {
		show, err := exec.CommandContext(ctx, "scutil", "--nc", "show", s.id).Output()
		if err != nil {
			continue
		}
		on = append(on, WireGuard{Name: s.name, Endpoint: remote(string(show))})
	}
	return on, true
}

type ncEntry struct{ id, name string }

func connected(list string) []ncEntry {
	var out []ncEntry
	for _, line := range strings.Split(list, "\n") {
		if m := ncService.FindStringSubmatch(line); m != nil {
			out = append(out, ncEntry{id: m[1], name: m[2]})
		}
	}
	return out
}

func remote(show string) string {
	if m := ncRemote.FindStringSubmatch(show); m != nil {
		return m[1]
	}
	return ""
}

// TunnelHint says why the private network may not answer, from the WireGuard
// tunnels that are on and the public address of the server: none on, or one
// that goes to another address. Empty when it cannot tell.
func TunnelHint(on []WireGuard, public string, known bool) string {
	if !known {
		return ""
	}
	if len(on) == 0 {
		return "No tunnel of the WireGuard app is on."
	}
	for _, t := range on {
		host, _, err := net.SplitHostPort(t.Endpoint)
		if err != nil {
			host = t.Endpoint
		}
		if host == public {
			return ""
		}
	}
	t := on[0]
	return "The WireGuard tunnel \"" + t.Name + "\" is on and goes to " + t.Endpoint + ", but the server is at " + public +
		": if it is the tunnel of this project, set its Endpoint to " + public + " in the WireGuard app (Edit), then turn it off and on."
}
