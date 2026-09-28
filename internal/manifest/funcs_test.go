package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderConfig(t *testing.T) {
	dir := t.TempDir()
	tmpl := `name: {{ .project }}
address: {{ yaml .address }}
note: {{ yaml .note }}
clients: {{ yaml .clients }}
{{- if .mail }}
mail: {hostname: {{ .mail_hostname }}}
{{- end }}
`
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml.tmpl"), []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Manifest{Config: "stack.yaml.tmpl"}
	out, err := m.RenderConfig(dir, "demo", map[string]any{
		"address": "10.0.0.1", "note": "a: b # c", "clients": []any{"laptop", "phone"}, "mail": false, "mail_hostname": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "name: demo\naddress: 10.0.0.1\nnote: 'a: b # c'\nclients: [laptop, phone]\n"
	if string(out) != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}

	if _, err := m.RenderConfig(dir, "demo", map[string]any{"address": "x"}); err == nil || !strings.Contains(err.Error(), "note") {
		t.Errorf("a missing answer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml.tmpl"), []byte("a: [\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RenderConfig(dir, "demo", nil); err == nil || !strings.Contains(err.Error(), "not YAML") {
		t.Errorf("broken YAML: %v", err)
	}
}

func TestRender(t *testing.T) {
	config := map[string]any{"network": map[string]any{"cidr": "10.77.0.0/24"}}
	secrets := map[string]any{"token": "t0k", "keys": []any{"a", "b"}}
	for text, want := range map[string]string{
		"https://{{ firstHost .config.network.cidr }}:4646": "https://10.77.0.1:4646",
		`{{ secret "token" }}`:                              "t0k",
		`{{ secret "keys" }}`:                               "[a, b]",
		`[{{ secret "not_yet" }}]`:                          "[]",
	} {
		got, err := Render("env", text, config, secrets)
		if err != nil || got != want {
			t.Errorf("%s: %q, %v; want %q", text, got, err, want)
		}
	}
	if _, err := Render("env", "{{ .config.missing.cidr }}", config, nil); err == nil {
		t.Error("a missing key rendered")
	}
}
