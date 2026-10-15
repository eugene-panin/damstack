package login

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

type fake struct {
	accepts map[string]bool
	down    bool
	copied  []string
	hostKey ssh.PublicKey
	known   string
}

func (f *fake) login(t *testing.T) *Login {
	if f.hostKey == nil {
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		f.hostKey, _ = ssh.NewPublicKey(pub)
	}
	if f.known == "" {
		f.known = filepath.Join(t.TempDir(), "known_hosts")
	}
	return &Login{
		Key:        "/home/u/.ssh/id_ed25519",
		KnownHosts: f.known,
		Out:        &bytes.Buffer{},
		Probe: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if f.down {
				return []byte("ssh: connect to host 1.2.3.4 port 22: Operation timed out"), errors.New("exit 255")
			}
			if name == "ssh-keyscan" {
				return []byte("# 1.2.3.4:22 SSH-2.0-OpenSSH_9.6\n1.2.3.4 " + strings.TrimSpace(string(ssh.MarshalAuthorizedKey(f.hostKey))) + "\n"), nil
			}
			if f.accepts[args[len(args)-2]] {
				return nil, nil
			}
			return []byte("root@1.2.3.4: Permission denied (publickey,password)."), errors.New("exit 255")
		},
		Interactive: func(_ context.Context, name string, args ...string) error {
			target := args[len(args)-1]
			f.copied = append(f.copied, target)
			f.accepts[target] = true
			return nil
		},
	}
}

var server = Server{Address: "1.2.3.4", FirstUser: "root", OpsUser: "ops"}

func TestOpsUserLogsIn(t *testing.T) {
	f := &fake{accepts: map[string]bool{"ops@1.2.3.4": true}}
	if err := f.login(t).Ensure(t.Context(), server); err != nil || len(f.copied) != 0 {
		t.Errorf("%v, copied %v", err, f.copied)
	}
}

func TestPasswordOnlyServerGetsTheKey(t *testing.T) {
	f := &fake{accepts: map[string]bool{}}
	l := f.login(t)
	if err := l.Ensure(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	if len(f.copied) != 1 || f.copied[0] != "root@1.2.3.4" || !strings.Contains(l.Out.(*bytes.Buffer).String(), "password of root") {
		t.Errorf("copied %v, said %q", f.copied, l.Out)
	}
}

func TestUnreachableServer(t *testing.T) {
	f := &fake{accepts: map[string]bool{}, down: true}
	err := f.login(t).Ensure(t.Context(), server)
	if err == nil || !strings.Contains(err.Error(), "timed out") || len(f.copied) != 0 {
		t.Errorf("%v, copied %v", err, f.copied)
	}
}

func TestANewServersHostKeyIsTakenOnlyOnceSaidToBeItsOwn(t *testing.T) {
	f := &fake{accepts: map[string]bool{"ops@1.2.3.4": true}}
	l := f.login(t)
	var asked string
	l.Confirm = func(q string) (bool, error) { asked = q; return false, nil }
	if err := l.Ensure(t.Context(), server); err == nil || !strings.Contains(err.Error(), "was not taken") {
		t.Fatalf("a host key the person does not know must stop it: %v", err)
	}
	if !strings.Contains(l.Out.(*bytes.Buffer).String(), ssh.FingerprintSHA256(f.hostKey)) || asked == "" {
		t.Errorf("the fingerprint must be shown and asked about: %q", l.Out)
	}
	if _, err := os.Stat(f.known); err == nil {
		t.Error("a host key said not to be the server's was kept")
	}
	l.Confirm = func(string) (bool, error) { return true, nil }
	if err := l.Ensure(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(f.known); !strings.HasPrefix(string(data), "1.2.3.4 ssh-ed25519 ") {
		t.Errorf("known hosts: %q", data)
	}
	l.Confirm = func(string) (bool, error) { t.Error("a known server must not be asked about"); return true, nil }
	if err := l.Ensure(t.Context(), server); err != nil {
		t.Fatal(err)
	}
}

func TestTheHostKeyTheStackGivesIsCheckedWithoutAsking(t *testing.T) {
	f := &fake{accepts: map[string]bool{"ops@1.2.3.4": true}}
	l := f.login(t)
	l.Confirm = func(string) (bool, error) { t.Error("asked though the stack gives the key"); return false, nil }
	wrong := server
	wrong.HostKey = "SHA256:not-this-one"
	if err := l.Ensure(t.Context(), wrong); err == nil || !strings.Contains(err.Error(), "someone may be in between") {
		t.Fatalf("got %v", err)
	}
	right := server
	right.HostKey = ssh.FingerprintSHA256(f.hostKey)
	if err := l.Ensure(t.Context(), right); err != nil {
		t.Fatal(err)
	}
}
