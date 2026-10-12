package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugene-panin/damstack/internal/project"
)

func TestAnUnchangedPlatformStepIsSkipped(t *testing.T) {
	dir, stack := t.TempDir(), t.TempDir()
	for path, text := range map[string]string{
		filepath.Join(dir, project.MetaFile):     "name: ovh\n",
		filepath.Join(dir, project.ConfigFile):   "server: {address: 1.2.3.4}\napps:\n  sauron: {replicas: 1}\n",
		filepath.Join(stack, "ansible/site.yml"): "- hosts: all\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SaveSecrets("pw", map[string]any{"vault_consul_token": "a"}); err != nil {
		t.Fatal(err)
	}
	inputs := func() string {
		in, err := platformInputs(p, stack, "ansible/site.yml", "pw")
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	first := inputs()
	now := time.Now()
	if why := unchanged(p, "provision", first, now); why != "" {
		t.Errorf("a step that never ran must run: %q", why)
	}
	if err := recordRun(p, "deploy", "provision", first, now.Add(-time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	if why := unchanged(p, "provision", first, now); !strings.HasPrefix(why, "unchanged since") {
		t.Errorf("a step whose inputs are those of its last good run is skipped: %q", why)
	}

	// An app's settings are not the platform's.
	os.WriteFile(filepath.Join(dir, project.ConfigFile), []byte("server: {address: 1.2.3.4}\napps:\n  sauron: {replicas: 3}\n"), 0o644)
	if inputs() != first {
		t.Error("a change under apps must not change the platform's inputs")
	}
	if unchanged(p, "provision", first, now.Add(8*24*time.Hour)) != "" {
		t.Error("an unchanged step runs again once its last run is a week old")
	}
	for name, change := range map[string]func(){
		"stack.yaml": func() {
			os.WriteFile(filepath.Join(dir, project.ConfigFile), []byte("server: {address: 5.6.7.8}\n"), 0o644)
		},
		"a secret":   func() { p.SaveSecrets("pw", map[string]any{"vault_consul_token": "b"}) },
		"a playbook": func() { os.WriteFile(filepath.Join(stack, "ansible/site.yml"), []byte("- hosts: none\n"), 0o644) },
	} {
		before := inputs()
		change()
		if inputs() == before {
			t.Errorf("a change to %s must change the inputs", name)
		}
	}
	if err := recordRun(p, "deploy", "provision", inputs(), now, os.ErrClosed); err != nil {
		t.Fatal(err)
	}
	if unchanged(p, "provision", inputs(), now) != "" {
		t.Error("a step whose last run failed must run")
	}
}
