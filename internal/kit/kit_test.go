package kit

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestPassphrase(t *testing.T) {
	a, b := Passphrase(), Passphrase()
	if !regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){4}$`).MatchString(a) || a == b {
		t.Errorf("got %s and %s", a, b)
	}
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"stack.yaml":                  "name: demo\n",
		"vault.yml":                   "$ANSIBLE_VAULT;1.1;AES256\n",
		"state/infra.tfstate":         `{"serial": 3}`,
		".damstack/project.yaml":      "name: demo\n",
		".damstack/work/plan":         "a plan",
		".damstack/work/logs/one.log": "a log",
		".git/HEAD":                   "ref: refs/heads/main\n",
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	pass := Passphrase()
	if err := Pack(&buf, dir, "the-vault-password", pass); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(buf.Bytes(), []byte("the-vault-password")) || bytes.Contains(buf.Bytes(), []byte("serial")) {
		t.Fatal("the kit is not encrypted")
	}
	kit := buf.Bytes()

	dest := filepath.Join(t.TempDir(), "demo")
	if _, err := Unpack(bytes.NewReader(kit), "WRONG-PASS", dest); !errors.Is(err, ErrPassphrase) {
		t.Errorf("a wrong passphrase: %v", err)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("a wrong passphrase left a directory")
	}
	password, err := Unpack(bytes.NewReader(kit), pass, dest)
	if err != nil || password != "the-vault-password" {
		t.Fatalf("got %q, %v", password, err)
	}
	for name, content := range files {
		data, err := os.ReadFile(filepath.Join(dest, name))
		skipped := Skip(filepath.Dir(name)) || Skip(name)
		switch {
		case skipped && err == nil:
			t.Errorf("%s is in the kit", name)
		case !skipped && string(data) != content:
			t.Errorf("%s: got %q, %v", name, data, err)
		}
	}
	if info, _ := os.Stat(filepath.Join(dest, "vault.yml")); info.Mode().Perm() != 0o640 {
		t.Errorf("vault.yml has mode %v", info.Mode().Perm())
	}
	if _, err := Unpack(bytes.NewReader(kit), pass, dest); err == nil {
		t.Error("unpacked over a project that is there")
	}
}
