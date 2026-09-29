package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugene-panin/damstack/internal/ask"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
)

var stackManifest = &manifest.Manifest{
	Questions: []manifest.Question{
		{Name: "address", Prompt: "Address"},
		{Name: "mail", Prompt: "Mail?", Type: "bool"},
	},
	Secrets: []manifest.Secret{
		{Name: "ca_key", Generate: "ca", Cert: "ca.pem"},
		{Name: "gossip", Generate: "base64"},
		{Name: "api_token", Ask: "API token"},
		{Name: "mail_password", Generate: "password", When: "mail"},
	},
	Config: "stack.yaml.tmpl",
}

func stack(t *testing.T) string {
	dir := t.TempDir()
	tmpl := "name: {{ .project }}\naddress: {{ yaml .address }}\nmail: {{ .mail }}\n"
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml.tmpl"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCreateAsking(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "demo")
	p, password, err := Create(Options{
		Manifest: stackManifest, Stack: stack(t), Name: "demo", Dir: dir,
		Ref:      project.StackRef{Name: "s", Tag: "v1.0.0"},
		Prompter: ask.NewPrompter(strings.NewReader("1.2.3.4\ny\ns3cret\n"), &bytes.Buffer{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := os.ReadFile(filepath.Join(dir, project.ConfigFile))
	if err != nil || string(config) != "name: demo\naddress: 1.2.3.4\nmail: true\n" {
		t.Errorf("stack.yaml %q, %v", config, err)
	}
	if cert, err := os.ReadFile(filepath.Join(dir, "ca.pem")); err != nil || !strings.HasPrefix(string(cert), "-----BEGIN CERTIFICATE-----") {
		t.Errorf("ca.pem %q, %v", cert, err)
	}
	secrets, err := p.Secrets(password)
	if err != nil {
		t.Fatal(err)
	}
	if secrets["api_token"] != "s3cret" || !strings.Contains(secrets["ca_key"].(string), "PRIVATE KEY") ||
		secrets["gossip"] == "" || secrets["mail_password"] == "" {
		t.Errorf("secrets %v", secrets)
	}
	if got, err := p.Password(); err != nil || got != password {
		t.Errorf("password %q, %v", got, err)
	}
}

func TestCreateFromGivenAnswers(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "check")
	p, _, err := Create(Options{
		Manifest: stackManifest, Stack: stack(t), Name: "check", Dir: dir, Password: "pw", Placeholders: true,
		Given: map[string]any{"address": "10.0.0.1", "mail": false},
	})
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := p.Secrets("pw")
	if err != nil || secrets["api_token"] != "placeholder-api_token" || secrets["mail_password"] != nil {
		t.Errorf("secrets %v, %v", secrets, err)
	}
}

func TestCreateFailsWithoutLeavingAPassword(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, _, err := Create(Options{
		Manifest: stackManifest, Stack: stack(t), Name: "demo", Dir: filepath.Join(t.TempDir(), "demo"),
		Given: map[string]any{"address": "1.2.3.4"},
	})
	if err == nil || !strings.Contains(err.Error(), "secret api_token: not given") {
		t.Errorf("got %v", err)
	}
	path, _ := project.PasswordPath("demo")
	if _, err := os.Stat(path); err == nil {
		t.Error("a password was left for a project that was not made")
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x"), nil, 0o644)
	if _, _, err := Create(Options{Manifest: stackManifest, Stack: stack(t), Name: "demo", Dir: dir}); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Errorf("a directory with files: %v", err)
	}
}

func TestAddApp(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, password, err := Create(Options{
		Manifest: stackManifest, Stack: stack(t), Name: "demo", Dir: filepath.Join(t.TempDir(), "demo"),
		Given: map[string]any{"address": "1.2.3.4", "api_token": "tok"},
	})
	if err != nil {
		t.Fatal(err)
	}
	appDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(appDir, "app.yaml.tmpl"), []byte("hostname: {{ yaml .hostname }}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &manifest.Manifest{
		Name: "mail", Kind: manifest.KindApp, Config: "app.yaml.tmpl",
		Questions: []manifest.Question{{Name: "hostname", Prompt: "Mail host"}},
		Secrets:   []manifest.Secret{{Name: "mail_admin_password", Generate: "password"}},
	}
	o := AppOptions{Manifest: app, Stack: appDir, Ref: project.StackRef{Name: "mail", Tag: "v0.1.0"}, Project: p, Password: password,
		Given: map[string]any{"hostname": "mail.example.org"}}
	if err := AddApp(o); err != nil {
		t.Fatal(err)
	}
	config, err := p.Config()
	if err != nil || config["apps"].(map[string]any)["mail"].(map[string]any)["hostname"] != "mail.example.org" || config["address"] != "1.2.3.4" {
		t.Errorf("stack.yaml %v, %v", config, err)
	}
	secrets, err := p.Secrets(password)
	if err != nil || secrets["mail_admin_password"] == "" || secrets["api_token"] != "tok" {
		t.Errorf("secrets %v, %v", secrets, err)
	}
	reopened, err := project.Open(p.Dir)
	if err != nil || len(reopened.Meta.Apps) != 1 || reopened.Meta.Apps[0].Tag != "v0.1.0" {
		t.Errorf("project.yaml %+v, %v", reopened, err)
	}
	if err := AddApp(o); err == nil || !strings.Contains(err.Error(), "already an app") {
		t.Errorf("a second add: %v", err)
	}

	clash := *app
	clash.Name = "other"
	clash.Secrets = []manifest.Secret{{Name: "gossip", Generate: "base64"}}
	o.Manifest, o.Ref.Name = &clash, "other"
	if err := AddApp(o); err == nil || !strings.Contains(err.Error(), "has a secret gossip already") {
		t.Errorf("a secret of the platform taken: %v", err)
	}
}
