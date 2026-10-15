package ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/sshkey"
)

// keyServer makes the server take a new key when takes; the keys it was
// asked about are in the slice returned.
func keyServer(t *testing.T, takes bool) *[]*sshkey.Key {
	t.Helper()
	old := serverTakesKey
	t.Cleanup(func() { serverTakesKey = old })
	var asked []*sshkey.Key
	serverTakesKey = func(_ context.Context, _ *project.Project, _ *manifest.Manifest, k *sshkey.Key) bool {
		asked = append(asked, k)
		return takes
	}
	return &asked
}

func publicKey(t *testing.T, v any) string {
	t.Helper()
	k, err := sshkey.FromSecret(v)
	if err != nil {
		t.Fatal(err)
	}
	if k == nil {
		return ""
	}
	return k.PublicKey
}

const keyStack = "server:\n  address: 203.0.113.10\n  first_user: root\n  ops_user: ops\n  ssh_key: {secret: ssh_key, apply: authorize}\n" +
	"commands:\n  authorize: {run: [bin/up]}\n"

func TestANewSSHKeyIsKeptOnlyWhenTheServerTakesIt(t *testing.T) {
	for _, c := range []struct {
		name        string
		fails       bool
		takes       bool
		want        string
		keepsTheOld bool
	}{
		{"applied", false, false, "", false},
		{"not applied, and the server does not take it", true, false, "boom; the old key stays, nothing changed on this Mac", true},
		{"not applied, but the server takes it", true, true, "boom; the server takes the new key already, so it stays in vault.yml, " +
			"and the old one may no longer work: run damstack ssh rotate demo again to finish", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, password := newProject(t, "{}\n")
			devStack(t, p, keyStack)
			m, _ := CachedStack(p)
			if _, err := EnsureSSHKey(p, m, password); err != nil {
				t.Fatal(err)
			}
			before, _ := p.Secrets(password)
			old := publicKey(t, before["ssh_key"])
			r := &fakeRunner{do: map[string]func(context.Context) error{}}
			fakeEngines(t, r)
			asked := keyServer(t, c.takes)
			var during string
			r.do["up"] = func(context.Context) error {
				secrets, _ := p.Secrets(password)
				during = publicKey(t, secrets["ssh_key"])
				if c.fails {
					return errors.New("boom")
				}
				return nil
			}
			ui := &fakeUI{yes: true}
			err := RotateSSHKey(context.Background(), ui, true, p)
			if c.want == "" && err != nil || c.want != "" && (err == nil || err.Error() != c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			if got := r.steps(); got != "up" {
				t.Errorf("ran %q", got)
			}
			if during == "" || during == old {
				t.Errorf("the new key is in vault.yml while it is applied: %v", during)
			}
			after, _ := p.Secrets(password)
			if kept := publicKey(t, after["ssh_key"]); c.keepsTheOld && kept != old || !c.keepsTheOld && kept != during {
				t.Errorf("vault.yml keeps %q; old %q, new %q", kept, old, during)
			}
			switch {
			case !c.fails && len(*asked) != 0:
				t.Error("a key applied needs no asking")
			case c.fails && len(*asked) != 1:
				t.Errorf("the server is asked once, asked %d", len(*asked))
			case c.fails:
				if (*asked)[0].PublicKey != during {
					t.Error("the server is asked about the new key")
				}
			}
			entries, _ := p.History()
			if len(entries) != 1 || entries[0].Command != "ssh rotate" || (entries[0].Result == project.OK) == c.fails {
				t.Errorf("history: %+v", entries)
			}
			if !c.fails {
				if !strings.Contains(ui.said(), "demo has a new SSH key; the server takes it and no longer the old one.") {
					t.Errorf("said %q", ui.said())
				}
				if written, _ := os.ReadFile(filepath.Join(p.Dir, ProjectKeyFile+".pub")); strings.TrimSpace(string(written)) != during {
					t.Errorf("the new key is the one written for ssh: %q", written)
				}
			}
		})
	}
}

func TestASSHKeyIsNotRotatedWithoutAYes(t *testing.T) {
	p, password := newProject(t, "{}\n")
	devStack(t, p, keyStack)
	r := &fakeRunner{}
	fakeEngines(t, r)
	keyServer(t, false)
	ui := &fakeUI{}
	if err := RotateSSHKey(context.Background(), ui, false, p); err != nil {
		t.Fatal(err)
	}
	if len(ui.confirms) != 1 || ui.confirms[0] != "A new SSH key for demo: the server stops taking the old one. Go?" {
		t.Errorf("asked %q", ui.confirms)
	}
	if secrets, _ := p.Secrets(password); secrets["ssh_key"] != nil || r.steps() != "" {
		t.Errorf("a no changes nothing: %v, ran %q", secrets, r.steps())
	}

	plain, _ := newProject(t, "{}\n")
	devStack(t, plain, "")
	if err := RotateSSHKey(context.Background(), &fakeUI{yes: true}, true, plain); err == nil ||
		err.Error() != "the stack demo of demo keeps no SSH key of its own" {
		t.Errorf("got %v", err)
	}
}
