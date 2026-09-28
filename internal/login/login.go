// Package login makes sure the SSH key of this machine logs in to a server,
// putting it there with the password the provider gave when it does not yet.
package login

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

type Server struct {
	Address   string
	FirstUser string
	OpsUser   string
}

type Login struct {
	Key        string
	KnownHosts string
	// Probe runs a command and returns its output; Interactive runs one on
	// the terminal. Both run ssh on this machine unless a test replaces them.
	Probe       func(ctx context.Context, name string, args ...string) ([]byte, error)
	Interactive func(ctx context.Context, name string, args ...string) error
	Out         io.Writer
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
	if err := l.Interactive(ctx, "ssh-copy-id", "-i", l.Key+".pub", "-o", "UserKnownHostsFile="+l.KnownHosts,
		"-o", "StrictHostKeyChecking=accept-new", target); err != nil {
		return fmt.Errorf("ssh-copy-id %s: %w; check the address and the password, or ask your provider how %s logs in", target, err, s.FirstUser)
	}
	if err := l.try(ctx, s.FirstUser, s.Address); err != nil {
		return fmt.Errorf("the key was put on %s, and still does not log in: %w", target, err)
	}
	return nil
}

func (l *Login) try(ctx context.Context, user, address string) error {
	out, err := l.Probe(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "IdentitiesOnly=yes",
		"-i", l.Key, "-o", "UserKnownHostsFile="+l.KnownHosts, "-o", "StrictHostKeyChecking=accept-new",
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
