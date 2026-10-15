package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
)

// devStack is a stack in a directory of its own, with its damstack.yaml
// ending in more, and the project p set up from it.
func devStack(t *testing.T, p *project.Project, more string) {
	t.Helper()
	dir := t.TempDir()
	for path, text := range map[string]string{
		manifest.File: "apiVersion: " + manifest.APIVersion + "\nname: demo\ndescription: a stack of a test\n" +
			"check: {answers: answers.yaml}\nsteps:\n  - name: up\n    run: [bin/up]\n" + more,
		"answers.yaml": "{}\n",
		"bin/up":       "#!/bin/sh\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p.Meta.Stack = project.StackRef{Name: "demo", URL: "file://" + dir, Tag: DevTag}
	if m, _ := CachedStack(p); m == nil {
		_, err := manifest.Load(dir, "")
		t.Fatalf("the stack of the test does not load: %v", err)
	}
}

func TestACertificateIsPutInPlaceOnlyOnceItsSecretIsKept(t *testing.T) {
	p, _ := newProject(t, "{}\n")
	cert := filepath.Join(p.Dir, "ca.pem")
	if err := os.WriteFile(cert, []byte("the old one"), 0o644); err != nil {
		t.Fatal(err)
	}
	secrets := map[string]any{"ca_key": "old key"}
	put, err := MakeAnew(p, manifest.Secret{Name: "ca_key", Generate: "ca", Cert: "ca.pem"}, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if key, _ := secrets["ca_key"].(string); key == "" || key == "old key" {
		t.Errorf("the key is new: %v", secrets["ca_key"])
	}
	if data, _ := os.ReadFile(cert); string(data) != "the old one" {
		t.Fatalf("until the key is kept, the certificate stays: %q", data)
	}
	if err := put(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(cert); !strings.Contains(string(data), "BEGIN CERTIFICATE") {
		t.Errorf("the new certificate: %q", data)
	}
	if data, _ := os.ReadFile(cert + ".old"); string(data) != "the old one" {
		t.Errorf("the old one is next to it: %q", data)
	}

	put, err = MakeAnew(p, manifest.Secret{Name: "token", Generate: "hex", Bytes: 8}, secrets)
	if err != nil || put() != nil {
		t.Fatal(err)
	}
	if token, _ := secrets["token"].(string); len(token) != 16 {
		t.Errorf("a secret without a certificate: %q", token)
	}
}

func TestOnlyASecretTheStackAllowsIsMadeAnew(t *testing.T) {
	p, password := newProject(t, "{}\n")
	if err := p.SaveSecrets(password, map[string]any{"token": "old", "ca_key": "key"}); err != nil {
		t.Fatal(err)
	}
	m := &manifest.Manifest{Name: "demo", Secrets: []manifest.Secret{
		{Name: "token", Generate: "hex", Rotate: true},
		{Name: "ca_key", Generate: "ca", Cert: "ca.pem"},
		{Name: "db_password", Generate: "password", Rotate: true},
	}}
	for name, want := range map[string]string{
		"ca_key": "the stack demo does not allow making ca_key anew; it allows db_password, token",
		"nope":   "nope is not a secret of the stack demo",
	} {
		if err := RotateSecret(&fakeUI{yes: true}, p, m, name); err == nil || err.Error() != want {
			t.Errorf("%s: got %v", name, err)
		}
	}
	none := &manifest.Manifest{Name: "demo", Secrets: []manifest.Secret{{Name: "ca_key", Generate: "ca"}}}
	if err := RotateSecret(&fakeUI{yes: true}, p, none, "ca_key"); err == nil || err.Error() != "the stack demo does not allow making ca_key anew" {
		t.Errorf("a stack that allows none: %v", err)
	}

	no := &fakeUI{}
	if err := RotateSecret(no, p, m, "token"); err != nil {
		t.Fatal(err)
	}
	if secrets, _ := p.Secrets(password); secrets["token"] != "old" || len(no.confirms) != 1 {
		t.Errorf("a no changes nothing: %v, asked %v", secrets["token"], no.confirms)
	}
	yes := &fakeUI{yes: true}
	if err := RotateSecret(yes, p, m, "token"); err != nil {
		t.Fatal(err)
	}
	secrets, _ := p.Secrets(password)
	if secrets["token"] == "old" || secrets["ca_key"] != "key" {
		t.Errorf("only the secret asked for is new: %v", secrets)
	}
	if !strings.Contains(yes.said(), "token of demo is new. damstack deploy demo puts it in place") {
		t.Errorf("said %q", yes.said())
	}
}

func TestOnlyASecretTheProjectAsksForIsSet(t *testing.T) {
	p, password := newProject(t, "{}\n")
	devStack(t, p, "secrets:\n  - {name: api_token, ask: An API token}\n  - {name: smtp_password, ask: The SMTP password, optional: true}\n")
	given := func(manifest.Secret) (string, error) { return "given", nil }
	if _, err := SetSecret(p, "nope", false, given); err == nil ||
		err.Error() != "nope is no secret demo asks for; damstack secret list shows them: api_token, smtp_password" {
		t.Errorf("got %v", err)
	}
	if src, err := SetSecret(p, "api_token", false, given); err != nil || src.Spec.Name != "api_token" || src.App != "" {
		t.Fatalf("got %+v, %v", src, err)
	}
	if _, err := SetSecret(p, "smtp_password", false, given); err != nil {
		t.Fatal(err)
	}
	if _, err := SetSecret(p, "api_token", true, nil); err == nil || err.Error() != "api_token is needed; it can be replaced, not unset" {
		t.Errorf("got %v", err)
	}
	if _, err := SetSecret(p, "smtp_password", true, nil); err != nil {
		t.Fatal(err)
	}
	secrets, _ := p.Secrets(password)
	if _, ok := secrets["smtp_password"]; ok || secrets["api_token"] != "given" {
		t.Errorf("got %v", secrets)
	}
}

// fakeVault is the userpass methods of a Vault, for the token root: ops has
// alice and bob, and bob's password cannot be set.
type fakeVault struct {
	mu  sync.Mutex
	set map[string]string
}

func (v *fakeVault) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Vault-Token") != "root" {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	switch {
	case r.Method == "LIST" && r.URL.Path == "/v1/auth/ops/users":
		fmt.Fprint(w, `{"data": {"keys": ["alice", "bob"]}}`)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/ops/users/alice/password":
		var body struct{ Password string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		v.mu.Lock()
		v.set["alice"] = body.Password
		v.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost:
		http.Error(w, "internal error", http.StatusInternalServerError)
	default:
		http.NotFound(w, r)
	}
}

func (v *fakeVault) password(user string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.set[user]
}

// vaultProject is a project whose platform gives the address of a Vault at
// v, with an app whose sign-ins are its userpass method ops.
func vaultProject(t *testing.T, v *fakeVault) (*project.Project, string) {
	t.Helper()
	srv := httptest.NewServer(v)
	t.Cleanup(srv.Close)
	p, password := newProject(t, "{}\n")
	devStack(t, p, "secrets:\n  - {name: token, generate: hex, bytes: 8, rotate: true}\n  - {name: api_token, ask: An API token}\n"+
		"  - {name: server_ip, ask: The server, checks: [ssh-port]}\n"+
		"provides: [demo]\napp_env: {VAULT_ADDR: '"+srv.URL+"', VAULT_TOKEN: root}\n")
	p.Meta.Apps = []project.StackRef{appStack(t, "hello", "accounts:\n  - {what: operator passwords, userpass: ops}\n"+
		"targets:\n  demo:\n    steps:\n      - {name: apply, run: [bin/app]}\n")}
	if err := p.SaveSecrets(password, map[string]any{"token": "old", "api_token": "old", "server_ip": "203.0.113.10"}); err != nil {
		t.Fatal(err)
	}
	return p, password
}

func renewedBy(out []Renewed) map[string]Renewed {
	by := map[string]Renewed{}
	for _, r := range out {
		by[r.Name] = r
	}
	return by
}

func TestSecretsAreMadeAnewHereAndInVault(t *testing.T) {
	v := &fakeVault{set: map[string]string{}}
	p, password := vaultProject(t, v)
	s, err := LoadSecrets(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	set := AccountSet{App: "hello", What: "operator passwords", Mount: "ops"}
	if got := s.Accounts[set.Name()]; !s.HasVault() || strings.Join(got.Users, " ") != "alice bob" {
		t.Fatalf("the sign-ins: %+v", s.Accounts)
	}
	out := s.Rotate(context.Background(), []Renewal{
		{Name: "token", Kind: Generated},
		{Name: "api_token", Kind: Asked},
		{Name: set.UserRow("alice"), Label: "alice", Kind: Accounts},
		{Name: set.UserRow("bob"), Label: "bob", Kind: Accounts},
		{Name: set.UserRow("carol"), Label: "carol", Kind: Accounts},
	}, map[string]string{"api_token": "given"})
	by := renewedBy(out)
	if len(out) != 5 || by["token"].Err != nil || by["api_token"].Err != nil {
		t.Fatalf("got %+v", out)
	}
	alice := by[set.UserRow("alice")]
	if alice.Err != nil || len(alice.Passwords) != 1 || alice.Passwords[0].User != "alice" || alice.Passwords[0].Password != v.password("alice") || v.password("alice") == "" {
		t.Errorf("alice: %+v, Vault has %q", alice, v.password("alice"))
	}
	if err := by[set.UserRow("bob")].Err; err == nil || err.Error() != "bob: vault: 500 Internal Server Error" {
		t.Errorf("bob: %v", err)
	}
	if err := by[set.UserRow("carol")].Err; err == nil || err.Error() != "carol: no such sign-in" {
		t.Errorf("carol: %v", err)
	}
	secrets, _ := p.Secrets(password)
	if token, _ := secrets["token"].(string); token == "old" || len(token) != 16 || secrets["api_token"] != "given" || secrets["server_ip"] != "203.0.113.10" {
		t.Errorf("vault.yml: %v", secrets)
	}
	records, _ := p.Records()
	if records.Secrets[set.UserRow("alice")].Set.IsZero() || !records.Secrets[set.UserRow("bob")].Set.IsZero() {
		t.Errorf("only the sign-in set in Vault is recorded: %+v", records.Secrets)
	}
}

func TestAnAskedSecretWhoseCheckFailsIsNotKept(t *testing.T) {
	p, password := vaultProject(t, &fakeVault{set: map[string]string{}})
	s, err := LoadSecrets(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := s.Rotate(ctx, []Renewal{{Name: "server_ip", Kind: Asked}}, map[string]string{"server_ip": "198.51.100.7"})
	if len(out) != 1 || out[0].Err == nil || out[0].Err.Error() != "nothing answers on port 22 at 198.51.100.7: check the address, and that the server is on" {
		t.Fatalf("got %+v", out)
	}
	if secrets, _ := p.Secrets(password); secrets["server_ip"] != "203.0.113.10" {
		t.Errorf("the old value stays: %v", secrets["server_ip"])
	}
}

func TestWhatCouldNotBeKeptIsSaidOnItsOwnRow(t *testing.T) {
	set := AccountSet{App: "hello", Mount: "ops"}
	renewals := []Renewal{{Name: "token", Kind: Generated}, {Name: "api_token", Kind: Asked}, {Name: set.UserRow("alice"), Label: "alice", Kind: Accounts}}
	for _, c := range []struct {
		name        string
		spoil       func(t *testing.T, p *project.Project)
		here, vault string
	}{
		{"records that cannot be read", func(t *testing.T, p *project.Project) {
			if err := os.WriteFile(filepath.Join(p.Dir, project.RecordsFile), []byte("secrets: [\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, project.RecordsFile, "set in Vault, but not recorded: "},
		{"a vault.yml of another password", func(t *testing.T, p *project.Project) {
			data, err := project.EncryptSecrets("another", map[string]any{"token": "theirs"})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(p.Dir, project.VaultFile), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "it is left as it is", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := &fakeVault{set: map[string]string{}}
			p, _ := vaultProject(t, v)
			s, err := LoadSecrets(context.Background(), p)
			if err != nil {
				t.Fatal(err)
			}
			c.spoil(t, p)
			by := renewedBy(s.Rotate(context.Background(), renewals, map[string]string{"api_token": "given"}))
			for _, name := range []string{"token", "api_token"} {
				if err := by[name].Err; err == nil || !strings.Contains(err.Error(), c.here) {
					t.Errorf("%s: %v, want %q", name, err, c.here)
				}
			}
			alice := by[set.UserRow("alice")]
			if len(alice.Passwords) != 1 || alice.Passwords[0].Password != v.password("alice") {
				t.Errorf("the new password of alice can still be copied: %+v", alice)
			}
			switch {
			case c.vault == "" && alice.Err != nil:
				t.Errorf("alice: %v", alice.Err)
			case c.vault != "" && (alice.Err == nil || !strings.HasPrefix(alice.Err.Error(), c.vault)):
				t.Errorf("alice: %v, want %q", alice.Err, c.vault)
			}
		})
	}
}
