package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugene-panin/damstack/internal/engine"
	"github.com/eugene-panin/damstack/internal/manifest"
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

func TestADeployFingerprintsThePlatformsAnsibleStepsOrRunsWhatWasPicked(t *testing.T) {
	dir, stack := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".damstack"), 0o755)
	os.MkdirAll(filepath.Join(stack, "ansible"), 0o755)
	os.WriteFile(filepath.Join(dir, project.MetaFile), []byte("name: ovh\n"), 0o644)
	os.WriteFile(filepath.Join(dir, project.ConfigFile), []byte("server: {address: 1.2.3.4}\n"), 0o644)
	os.WriteFile(filepath.Join(stack, "ansible/site.yml"), []byte("- hosts: all\n"), 0o644)
	p, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	platform := &engine.Engine{Stack: stack}
	app := &engine.Engine{Stack: stack, App: "sauron"}
	jobs := func() []job {
		return []job{
			{e: platform, name: "bootstrap", step: manifest.Step{Name: "bootstrap", Once: true, Ansible: &manifest.Ansible{Playbook: "ansible/site.yml"}}},
			{e: platform, name: "provision", step: manifest.Step{Name: "provision", Ansible: &manifest.Ansible{Playbook: "ansible/site.yml"}}},
			{e: platform, name: "apply", step: manifest.Step{Name: "apply", Tofu: &manifest.Tofu{Dir: "infra"}}},
			{e: app, name: "sauron/apply", step: manifest.Step{Name: "apply", Tofu: &manifest.Tofu{Dir: "infra"}}},
		}
	}
	s := &streams{}
	got, err := chooseJobs(s, p, jobs(), false, "pw")
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range got {
		if want := j.name == "provision"; (j.inputs != "") != want {
			t.Errorf("%s: fingerprinted %v, want %v", j.name, j.inputs != "", want)
		}
	}
	s.all = true
	if got, _ = chooseJobs(s, p, jobs(), false, "pw"); got[1].inputs != "" {
		t.Error("--all runs every step: none is fingerprinted")
	}
	s.all, s.only = false, map[string]bool{"provision": true, "sauron": true}
	got, _ = chooseJobs(s, p, jobs(), false, "pw")
	var names []string
	for _, j := range got {
		names = append(names, j.name)
		if j.inputs != "" {
			t.Errorf("%s was picked: it runs, unchanged or not", j.name)
		}
	}
	if strings.Join(names, " ") != "provision sauron/apply" {
		t.Errorf("only the picked parts run: %v", names)
	}
}
