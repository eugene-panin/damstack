// Package login makes sure the SSH key of this machine logs in to a server,
// putting it there with the password the provider gave when it does not yet.
package login

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/skeema/knownhosts"
	"golang.org/x/crypto/ssh"

	"github.com/eugene-panin/damstack/internal/sshkey"
)

type Server struct {
	Address   string
	FirstUser string
	OpsUser   string
	// HostKey is the SHA256 fingerprint of the server's host key, when the
	// stack gives it: the key it first answers with must be that one.
	HostKey string
}

type Login struct {
	Key        string
	KnownHosts string
	// Probe runs a command and returns its output; Interactive runs one on
	// the terminal. Both run ssh on this machine unless a test replaces them.
	Probe       func(ctx context.Context, name string, args ...string) ([]byte, error)
	Interactive func(ctx context.Context, name string, args ...string) error
	// Confirm asks the person whether the key a new server answers with is
	// its own; nil takes it, as with no one to ask.
	Confirm func(question string) (bool, error)
	Out     io.Writer
}

func New(key, knownHosts string, out io.Writer) *Login {
	return &Login{
		Key:        key,
		KnownHosts: knownHosts,
		Probe: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
		Interactive: func(ctx context.Context, name string, args ...string) error {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			return cmd.Run()
		},
		Out: out,
	}
}

var errDenied = errors.New("denied")

// Ensure returns once the key logs in as the ops user or the first user.
func (l *Login) Ensure(ctx context.Context, s Server) error {
	if err := l.knowHost(ctx, s); err != nil {
		return err
	}
	users := []string{s.FirstUser}
	if s.OpsUser != "" && s.OpsUser != s.FirstUser {
		users = []string{s.OpsUser, s.FirstUser}
	}
	for _, user := range users {
		err := l.try(ctx, user, s.Address)
		if err == nil {
			return nil
		}
		if !errors.Is(err, errDenied) {
			return err
		}
	}

	fmt.Fprintf(l.Out, "\nYour SSH key does not log in to %s yet. damstack puts it there for %s now:\n"+
		"type the password of %s your provider gave you. It is not kept.\n\n", s.Address, s.FirstUser, s.FirstUser)
	target := s.FirstUser + "@" + s.Address
	if err := l.Interactive(ctx, "ssh-copy-id", "-i", l.Key+".pub", "-o", sshkey.KnownHostsOption(l.KnownHosts),
		"-o", "StrictHostKeyChecking=accept-new", target); err != nil {
		return fmt.Errorf("ssh-copy-id %s: %w; check the address and the password, or ask your provider how %s logs in", target, err, s.FirstUser)
	}
	if err := l.try(ctx, s.FirstUser, s.Address); err != nil {
		return fmt.Errorf("the key was put on %s, and still does not log in: %w", target, err)
	}
	return nil
}

// knowHost puts the host key of a server not known yet in the known hosts,
// once it is the one the stack gives, or the person says it is.
func (l *Login) knowHost(ctx context.Context, s Server) error {
	known, err := knownhosts.NewDB(l.KnownHosts)
	if err == nil && len(known.HostKeys(net.JoinHostPort(s.Address, "22"))) > 0 {
		return nil
	}
	out, err := l.Probe(ctx, "ssh-keyscan", "-T", "10", "-t", "ed25519,ecdsa,rsa", s.Address)
	if err != nil {
		return fmt.Errorf("reading the host key of %s: %s", s.Address, lastLine(strings.TrimSpace(string(out)), err))
	}
	var lines, prints []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.Join(fields[1:], " ")))
		if err != nil {
			continue
		}
		lines = append(lines, strings.TrimSpace(line))
		prints = append(prints, key.Type()+" "+ssh.FingerprintSHA256(key))
	}
	if len(lines) == 0 {
		return fmt.Errorf("%s gives no host key", s.Address)
	}
	switch {
	case s.HostKey != "":
		if !slices.ContainsFunc(prints, func(p string) bool { return strings.HasSuffix(p, " "+s.HostKey) }) {
			return fmt.Errorf("%s answers with the host key %s, not %s as the stack says: someone may be in between, stop here",
				s.Address, strings.Join(prints, ", "), s.HostKey)
		}
	case l.Confirm != nil:
		fmt.Fprintf(l.Out, "\n%s is a server this machine does not know yet. It answers with the host key\n\n    %s\n\n"+
			"Your provider shows the server's key on its console or in the mail about the server.\n", s.Address, strings.Join(prints, "\n    "))
		ok, err := l.Confirm("Is it the key your provider shows?")
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("the host key of %s was not taken: nothing was sent to it", s.Address)
		}
	default:
		fmt.Fprintf(l.Out, "%s is new to this machine; its host key is taken as it is: %s\n", s.Address, strings.Join(prints, ", "))
	}
	if err := os.MkdirAll(filepath.Dir(l.KnownHosts), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.KnownHosts, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(strings.Join(lines, "\n") + "\n")
	return errors.Join(err, f.Close())
}

func (l *Login) try(ctx context.Context, user, address string) error {
	out, err := l.Probe(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "IdentitiesOnly=yes",
		"-i", l.Key, "-o", sshkey.KnownHostsOption(l.KnownHosts), "-o", "StrictHostKeyChecking=accept-new",
		user+"@"+address, "true")
	if err == nil {
		return nil
	}
	text := strings.TrimSpace(string(out))
	switch {
	case strings.Contains(text, "Permission denied"):
		return errDenied
	case strings.Contains(text, "REMOTE HOST IDENTIFICATION HAS CHANGED"), strings.Contains(text, "Host key verification failed"):
		return fmt.Errorf("%s answers with another host key than before: if the server was reinstalled, "+
			"remove its line from %s; otherwise someone may be in between, stop here", address, l.KnownHosts)
	}
	return fmt.Errorf("ssh to %s: %s", address, lastLine(text, err))
}

func lastLine(text string, err error) string {
	if text == "" {
		return err.Error()
	}
	return text[strings.LastIndex(text, "\n")+1:]
}
