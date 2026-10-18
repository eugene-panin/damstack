package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/ops"
	"github.com/eugene-panin/damstack/internal/project"
)

// resolverDir is where macOS takes the resolver of each domain from, a file
// named after it; tests put a directory of their own in its place.
var resolverDir = "/etc/resolver"

// asRoot runs a command as root, with sudo on the terminal; tests put one
// that runs it as it is in its place.
var asRoot = func(ctx context.Context, s *cli, args ...string) error {
	cmd := exec.CommandContext(ctx, "sudo", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = s.in, s.out, s.err
	if err := s.run(cmd); err != nil {
		return fmt.Errorf("sudo %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// resolverRecord records that damstack sent the names under .consul of this
// Mac to the tunnel of a project, so that it can take that back.
const resolverRecord = ".damstack/resolver.json"

type resolved struct {
	Path    string `json:"path"`
	Address string `json:"address"`
}

// resolverMark begins the resolver damstack writes, with the project that
// owns it: only one project's tunnel can answer for .consul at a time.
const resolverMark = "# damstack "

func resolverPath() string { return filepath.Join(resolverDir, "consul") }

// resolverText is the resolver that sends the names under .consul to the
// server of p at address.
func resolverText(p *project.Project, address string) string {
	return fmt.Sprintf("%s%s %s\nnameserver %s\nport 53\n", resolverMark, p.Meta.Name, p.Dir, address)
}

// resolverOwner is the project a resolver damstack wrote is of, by name and
// directory, and the address it sends the names to; ok is false for one
// damstack did not write.
func resolverOwner(text string) (name, dir, address string, ok bool) {
	first, rest, _ := strings.Cut(text, "\n")
	mark, found := strings.CutPrefix(first, resolverMark)
	if !found {
		return "", "", "", false
	}
	name, dir, _ = strings.Cut(mark, " ")
	for _, line := range strings.Split(rest, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "nameserver" {
			address = f[1]
		}
	}
	return name, dir, address, true
}

// resolve sends the names under .consul of this Mac to the server of p at
// address, through its tunnel. A resolver of another project, or not of
// damstack, is replaced only once the person agrees on a terminal.
func resolve(ctx context.Context, s *cli, p *project.Project, address string) error {
	path := resolverPath()
	want := resolverText(p, address)
	old, err := os.ReadFile(path)
	switch {
	case err == nil && string(old) == want:
		return recordResolver(p, address)
	case err == nil:
		name, dir, at, ours := resolverOwner(string(old))
		if !ours || dir != p.Dir {
			if ours {
				fmt.Fprintf(s.out, "The names under .consul on this Mac go to the tunnel of %s, at %s: only one project's tunnel can answer for them.\n", name, at)
			} else {
				fmt.Fprintf(s.out, "%s is there already, and damstack did not write it.\n", path)
			}
			ok, err := s.ui(false).Confirm(fmt.Sprintf("Send them to %s instead?", p.Meta.Name), false)
			if err != nil {
				return fmt.Errorf("%s is left as it is: %w", path, err)
			}
			if !ok {
				fmt.Fprintf(s.out, "%s is left as it is.\n", path)
				return nil
			}
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	tmp, err := os.CreateTemp("", "damstack-resolver-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(want); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "sudo asks for your password to write %s.\n", path)
	if err := asRoot(ctx, s, "mkdir", "-p", resolverDir); err != nil {
		return err
	}
	if err := asRoot(ctx, s, "install", "-m", "0644", tmp.Name(), path); err != nil {
		return err
	}
	if err := recordResolver(p, address); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "This Mac finds the names under .consul through the tunnel of %s, at %s.\n", p.Meta.Name, address)
	return nil
}

func recordResolver(p *project.Project, address string) error {
	rec, err := json.Marshal(resolved{Path: resolverPath(), Address: address})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(p.Dir, resolverRecord), append(rec, '\n'), 0o644)
}

// recordedResolver is the resolver damstack wrote for p, nil if none.
func recordedResolver(p *project.Project) (*resolved, error) {
	data, err := os.ReadFile(filepath.Join(p.Dir, resolverRecord))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r resolved
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// unresolve takes back the resolver damstack wrote for p, if it is still
// there and still p's, and reports whether it did.
func unresolve(ctx context.Context, s *cli, p *project.Project) (bool, error) {
	r, err := recordedResolver(p)
	if err != nil || r == nil {
		return false, err
	}
	removed := false
	if text, err := os.ReadFile(r.Path); err == nil {
		if _, dir, _, ours := resolverOwner(string(text)); ours && dir == p.Dir {
			fmt.Fprintf(s.out, "sudo asks for your password to remove %s.\n", r.Path)
			if err := asRoot(ctx, s, "rm", "-f", r.Path); err != nil {
				return false, err
			}
			removed = true
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	return removed, os.Remove(filepath.Join(p.Dir, resolverRecord))
}

// resolverNotice says, after a deploy of p, how this Mac finds the names
// under .consul when its platform m answers DNS on the tunnel: it offers the
// resolver once, and again when the address moved; another project's
// resolver is only named.
func resolverNotice(ctx context.Context, s *cli, yes bool, p *project.Project, m *manifest.Manifest) {
	address, err := ops.TunnelDNS(p, m)
	if err != nil || address == "" {
		return
	}
	want := resolverText(p, address)
	text, err := os.ReadFile(resolverPath())
	if err == nil && string(text) == want {
		return
	}
	name, dir, _, ours := resolverOwner(string(text))
	rec, _ := recordedResolver(p)
	switch {
	case err == nil && ours && dir != p.Dir:
		fmt.Fprintf(s.out, "\nThe names under .consul on this Mac go to the tunnel of %s; damstack trust %s sends them to this one.\n", name, p.Meta.Name)
		return
	case rec == nil:
		offered := filepath.Join(p.Dir, ".damstack", "resolver-offered")
		if _, err := os.Stat(offered); err == nil {
			return
		}
		os.WriteFile(offered, nil, 0o644)
		fmt.Fprintf(s.out, "\nThe addresses under .consul open on this Mac once it asks the server of %s for them, through the tunnel.\n", p.Meta.Name)
	default:
		fmt.Fprintf(s.out, "\nThe server of %s answers for the names under .consul at %s now.\n", p.Meta.Name, address)
	}
	if !s.tty() || yes {
		fmt.Fprintf(s.out, "damstack trust %s, on a terminal, does it.\n", p.Meta.Name)
		return
	}
	if ok, err := s.ui(false).Confirm("Do it now?", true); err != nil || !ok {
		fmt.Fprintf(s.out, "damstack trust %s does it later.\n", p.Meta.Name)
		return
	}
	if err := resolve(ctx, s, p, address); err != nil {
		fmt.Fprintf(s.err, "damstack: %v\n", err)
	}
}
