package login

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/skeema/knownhosts"
	"golang.org/x/crypto/ssh"
)

// sshServer answers SSH with a host key of its own on a local port, until
// the test ends.
func sshServer(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				ssh.NewServerConn(conn, config)
			}()
		}
	}()
	return ln.Addr().String(), signer.PublicKey()
}

func knownHostsFile(t *testing.T, public string, key ssh.PublicKey) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{net.JoinHostPort(public, "22")}, key)
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSameServer(t *testing.T) {
	ours, ourKey := sshServer(t)
	other, _ := sshServer(t)
	kh := knownHostsFile(t, "34.1.2.3", ourKey)

	if err := SameServer(t.Context(), kh, "34.1.2.3", ours); err != nil {
		t.Errorf("the server of the project: %v", err)
	}
	if err := SameServer(t.Context(), kh, "34.1.2.3", other); !errors.Is(err, ErrOtherServer) {
		t.Errorf("another server: %v", err)
	}
	if err := SameServer(t.Context(), kh, "34.9.9.9", other); err != nil {
		t.Errorf("a server not known yet: %v", err)
	}
}
