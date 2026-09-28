package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateFindAndSecrets(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "demo")
	meta := Meta{Name: "demo", Stack: StackRef{Name: "hashistack", URL: "https://github.com/o/s", Tag: "v0.1.0", Commit: "abc"}, Created: time.Now()}
	p, err := Create(dir, meta, map[string][]byte{ConfigFile: []byte("name: demo\nnetwork: {cidr: 10.0.0.0/24}\n")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(dir, meta, nil); err == nil || !strings.Contains(err.Error(), "is not empty") {
		t.Errorf("a second create: %v", err)
	}

	sub := filepath.Join(dir, "clients", "laptop")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	found, err := Find(sub)
	if err != nil || found == nil || found.Dir != dir || found.Meta.Stack.Tag != "v0.1.0" {
		t.Fatalf("find: %+v, %v", found, err)
	}
	if none, err := Find(t.TempDir()); none != nil || err != nil {
		t.Errorf("find outside a project: %+v, %v", none, err)
	}
	config, err := p.Config()
	if err != nil || config["name"] != "demo" {
		t.Errorf("config %v, %v", config, err)
	}

	if _, err := p.Password(); err == nil || !strings.Contains(err.Error(), "no vault password") {
		t.Errorf("password before it is made: %v", err)
	}
	password, err := NewPassword("demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewPassword("demo"); err == nil {
		t.Error("the vault password was replaced")
	}
	path, _ := p.PasswordPath()
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("password file %v, %v", info, err)
	}
	if got, err := p.Password(); err != nil || got != password {
		t.Errorf("password %q, %v", got, err)
	}

	if s, err := p.Secrets(password); err != nil || len(s) != 0 {
		t.Errorf("secrets without a vault: %v, %v", s, err)
	}
	if err := p.SaveSecrets(password, map[string]any{"token": "t", "keys": []any{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, VaultFile))
	if !strings.HasPrefix(string(data), "$ANSIBLE_VAULT;1.1;AES256") || strings.Contains(string(data), "token") {
		t.Errorf("vault.yml is not encrypted:\n%s", data)
	}
	s, err := p.Secrets(password)
	if err != nil || s["token"] != "t" || len(s["keys"].([]any)) != 2 {
		t.Errorf("secrets %v, %v", s, err)
	}
	if _, err := p.Secrets("wrong"); err == nil {
		t.Error("a wrong password opened the vault")
	}
}

func TestHistory(t *testing.T) {
	p, err := Create(t.TempDir(), Meta{Name: "demo"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done, err := p.Done("bootstrap"); done || err != nil {
		t.Errorf("done before any run: %v, %v", done, err)
	}
	for _, e := range []Entry{
		{Command: "deploy", Step: "bootstrap", Result: Failed, Error: "unreachable"},
		{Command: "output", Result: OK},
		{Command: "deploy", Step: "bootstrap", Result: OK, Seconds: 12.5},
	} {
		if err := p.Record(e); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := p.History()
	if err != nil || len(entries) != 3 || entries[0].Error != "unreachable" || entries[2].Seconds != 12.5 {
		t.Errorf("history %+v, %v", entries, err)
	}
	if done, err := p.Done("bootstrap"); !done || err != nil {
		t.Errorf("done %v, %v", done, err)
	}
}
