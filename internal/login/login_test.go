package login

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

type fake struct {
	accepts map[string]bool
	down    bool
	copied  []string
}

func (f *fake) login() *Login {
	return &Login{
		Key:        "/home/u/.ssh/id_ed25519",
		KnownHosts: "/p/.damstack/known_hosts",
		Out:        &bytes.Buffer{},
		Probe: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if f.down {
				return []byte("ssh: connect to host 1.2.3.4 port 22: Operation timed out"), errors.New("exit 255")
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
	if err := f.login().Ensure(t.Context(), server); err != nil || len(f.copied) != 0 {
		t.Errorf("%v, copied %v", err, f.copied)
	}
}

func TestPasswordOnlyServerGetsTheKey(t *testing.T) {
	f := &fake{accepts: map[string]bool{}}
	l := f.login()
	if err := l.Ensure(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	if len(f.copied) != 1 || f.copied[0] != "root@1.2.3.4" || !strings.Contains(l.Out.(*bytes.Buffer).String(), "password of root") {
		t.Errorf("copied %v, said %q", f.copied, l.Out)
	}
}

func TestUnreachableServer(t *testing.T) {
	f := &fake{accepts: map[string]bool{}, down: true}
	err := f.login().Ensure(t.Context(), server)
	if err == nil || !strings.Contains(err.Error(), "timed out") || len(f.copied) != 0 {
		t.Errorf("%v, copied %v", err, f.copied)
	}
}
