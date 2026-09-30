package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
)

func TestDevices(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"laptop", "phone"} {
		conf := "[Interface]\nPrivateKey = key-of-" + name + "\n"
		if err := os.MkdirAll(filepath.Join(dir, "clients"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "clients", name+".conf"), []byte(conf), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	devices(&out, &project.Project{Dir: dir}, &manifest.Server{TunnelConfigs: "clients/*.conf"}, "turn it on")
	text := out.String()
	if !strings.Contains(text, "This computer: import "+filepath.Join(dir, "clients", "laptop.conf")) ||
		!strings.Contains(text, "phone: scan this in the WireGuard app") || !strings.Contains(text, "█") {
		t.Errorf("got\n%s", text)
	}
	if strings.Contains(text, "laptop: scan") {
		t.Error("the configuration to import is also a code")
	}

	out.Reset()
	devices(&out, &project.Project{Dir: t.TempDir()}, &manifest.Server{TunnelConfigs: "clients/*.conf"}, "turn it on")
	if !strings.Contains(out.String(), "turn it on") {
		t.Errorf("without configurations: %s", out.String())
	}
}
