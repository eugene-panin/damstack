package login

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/skeema/knownhosts"
	"golang.org/x/crypto/ssh"
)

// ErrOtherServer is a tunnel address answered by another server than the one
// of the project, such as through the tunnel of another project.
var ErrOtherServer = errors.New("another server answers there")

var errGotKey = errors.New("got the host key")

// SameServer checks that the SSH server at tunnel, host:port, has the host key
// knownHosts holds for the public address of the server. A server not in
// knownHosts yet cannot be told apart, and passes.
func SameServer(ctx context.Context, knownHosts, public, tunnel string) error {
	db, err := knownhosts.NewDB(knownHosts)
	if err != nil {
		return fmt.Errorf("read %s: %w", knownHosts, err)
	}
	host := net.JoinHostPort(public, "22")
	if len(db.HostKeys(host)) == 0 {
		return nil
	}
	var got ssh.PublicKey
	config := &ssh.ClientConfig{
		User:              "damstack",
		HostKeyAlgorithms: db.HostKeyAlgorithms(host),
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			got = key
			return errGotKey
		},
		Timeout: 5 * time.Second,
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", tunnel)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, _, _, err := ssh.NewClientConn(conn, tunnel, config); got == nil {
		return fmt.Errorf("ssh to %s: %w", tunnel, err)
	}
	remote, _ := net.ResolveTCPAddr("tcp", host)
	err = db.HostKeyCallback()(host, remote, got)
	switch {
	case err == nil:
		return nil
	case knownhosts.IsHostKeyChanged(err):
		return fmt.Errorf("%s: %w, not the server at %s", tunnel, ErrOtherServer, public)
	}
	return err
}
