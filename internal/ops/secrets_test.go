package ops

import (
	"os"
	"path/filepath"
	"strings"
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
