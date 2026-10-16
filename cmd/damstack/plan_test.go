package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/ops"
	"github.com/eugene-panin/damstack/internal/project"
)

// writeStack writes a stack of the test, its damstack.yaml and files, and
// loads it.
func writeStack(t *testing.T, files map[string]string) (*manifest.Manifest, string) {
	t.Helper()
	dir := t.TempDir()
	files["answers.yaml"] = "{}\n"
	files["bin/up"] = "#!/bin/sh\n"
	for path, text := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m, err := manifest.Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	return m, dir
}

func testApp(name, more string) map[string]string {
	return map[string]string{manifest.File: "apiVersion: " + manifest.APIVersion + "\nname: " + name + "\nkind: app\ndescription: " + name + " of a test\n" +
		"check: {answers: answers.yaml, platform: answers.yaml}\ntargets:\n  demo:\n    steps:\n      - {name: apply, run: [bin/up]}\n" + more}
}

// planProject is a project on a demo platform with mimir, and a library
// that has loki, all on this machine.
func planProject(t *testing.T) (*project.Project, *manifest.Manifest, string, string) {
	t.Helper()
	home := t.TempDir()
	for name, dir := range map[string]string{"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_CACHE_HOME": "cache", "DAMSTACK_HOME": "projects"} {
		t.Setenv(name, filepath.Join(home, dir))
	}
	library := filepath.Join(home, "cache", "damstack", "library.yaml")
	if err := os.MkdirAll(filepath.Dir(library), 0o755); err != nil {
		t.Fatal(err)
	}
	index := "stacks:\n  - {name: loki, url: https://example.org/registry, path: apps/loki, kind: app, platform: demo, provides: [logs@1]}\n"
	if err := os.WriteFile(library, []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	pm, pdir := writeStack(t, map[string]string{manifest.File: "apiVersion: " + manifest.APIVersion +
		"\nname: demo\ndescription: a platform of a test\nprovides: [demo]\ncheck: {answers: answers.yaml}\nsteps:\n  - {name: up, run: [bin/up]}\n"})
	_, mimir := writeStack(t, testApp("mimir", "provides: [metrics@1]\nrequires: {provides: [demo]}\n"))
	p, err := project.Create(filepath.Join(home, "projects", "shop"), project.Meta{Name: "shop",
		Stack: project.StackRef{Name: "demo", URL: "file://" + pdir, Tag: ops.DevTag},
		Apps:  []project.StackRef{{Name: "mimir", URL: "file://" + mimir, Tag: ops.DevTag}},
	}, map[string][]byte{project.ConfigFile: []byte("apps:\n  mimir: {}\n")})
	if err != nil {
		t.Fatal(err)
	}
	password, err := project.NewPassword("shop")
	if err != nil {
		t.Fatal(err)
	}
	return p, pm, pdir, password
}

func TestAnAppIsOfferedWhatItUsesAndConnectedWithYes(t *testing.T) {
	p, pm, pdir, password := planProject(t)
	files := testApp("grafana", "requires:\n  provides: [demo]\n  capabilities: [{capability: postgres@16, when: db.mode=external}]\n"+
		"uses: [metrics@1, logs@1]\nconfig: grafana.yaml.tmpl\n")
	files["grafana.yaml.tmpl"] = "db: {mode: bundled}\n"
	am, adir := writeStack(t, files)
	var out, errOut bytes.Buffer
	s := newCLI(t.Context(), strings.NewReader(""), &out, &errOut)
	order, err := addPlan(t.Context(), p, pm, pdir, &planned{m: am, dir: adir, ref: project.StackRef{Name: "grafana", Tag: ops.DevTag}},
		terminalFlow(s, p, password, "grafana", nil, true))
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(order) != 1 || order[0].m.Name != "grafana" {
		t.Fatalf("the plan %v", order)
	}
	for _, want := range []string{
		"grafana needs postgres@16 too when db.mode=external: settled after its questions.",
		"grafana can use what these give:",
		"mimir   metrics@1   on the project",
		"grafana can use more; you can add: loki (logs@1)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("no %q in\n%s", want, out.String())
		}
	}
	if err := order[0].pending.Add(); err != nil {
		t.Fatal(err)
	}
	if got := ops.ConnectOf(ops.Settings(p, "grafana")); !reflect.DeepEqual(got, map[string][]string{"metrics@1": {"mimir"}, "logs@1": {}}) {
		t.Errorf("connect %v", got)
	}
}

func TestARequirementNothingProvidesStopsThePlan(t *testing.T) {
	p, pm, pdir, password := planProject(t)
	am, adir := writeStack(t, testApp("files", "requires:\n  provides: [demo]\n  capabilities: [s3@1]\n"))
	var out bytes.Buffer
	s := newCLI(t.Context(), strings.NewReader(""), &out, &out)
	_, err := addPlan(t.Context(), p, pm, pdir, &planned{m: am, dir: adir}, terminalFlow(s, p, password, "files", nil, true))
	if err == nil || !strings.Contains(err.Error(), "files requires s3@1, which no module of the project provides") {
		t.Errorf("got %v", err)
	}
	if _, ok := p.Meta.App("files"); ok {
		t.Error("the app was added")
	}
}

func TestAnAppWithoutCapabilitiesLooksNothingUp(t *testing.T) {
	p, pm, pdir, password := planProject(t)
	if err := os.Remove(filepath.Join(os.Getenv("XDG_CACHE_HOME"), "damstack", "library.yaml")); err != nil {
		t.Fatal(err)
	}
	am, adir := writeStack(t, testApp("hello", "requires: {provides: [demo]}\n"))
	var out bytes.Buffer
	s := newCLI(t.Context(), strings.NewReader(""), &out, &out)
	order, err := addPlan(t.Context(), p, pm, pdir, &planned{m: am, dir: adir}, terminalFlow(s, p, password, "hello", nil, true))
	if err != nil || len(order) != 1 || out.Len() > 0 {
		t.Errorf("got %v, %v, said %q", order, err, out.String())
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CACHE_HOME"), "damstack", "library.yaml")); !os.IsNotExist(err) {
		t.Errorf("the library was fetched: %v", err)
	}
}
