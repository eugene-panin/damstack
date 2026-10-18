package ops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/consul/api"

	"github.com/eugene-panin/damstack/internal/event"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
)

// appStack is an app named name in a directory of its own, on a platform
// that provides demo, with its damstack.yaml ending in more.
func appStack(t *testing.T, name, more string) project.StackRef {
	t.Helper()
	return moduleStack(t, name, manifest.KindApp, more)
}

// moduleStack is a module of kind named name, as appStack makes an app.
func moduleStack(t *testing.T, name, kind, more string) project.StackRef {
	t.Helper()
	dir := t.TempDir()
	for path, text := range map[string]string{
		manifest.File: "apiVersion: " + manifest.APIVersion + "\nname: " + name + "\nkind: " + kind + "\ndescription: a module of a test\n" +
			"requires: {provides: [demo]}\ncheck: {answers: answers.yaml, platform: answers.yaml}\n" + more,
		"answers.yaml": "{}\n",
		"bin/app":      "#!/bin/sh\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ref := project.StackRef{Name: name, URL: "file://" + dir, Tag: DevTag}
	if m, _ := CachedRef(ref); m == nil {
		_, err := manifest.Load(dir, "")
		t.Fatalf("the app of the test does not load: %v", err)
	}
	return ref
}

func jobNames(jobs []Job) string {
	names := make([]string, len(jobs))
	for i, j := range jobs {
		names[i] = j.Name
	}
	return strings.Join(names, " ")
}

func TestAnAppsStepsComeAfterThePlatformsAndBeforeThoseAfterTheApps(t *testing.T) {
	p, _ := newProject(t, "{}\n")
	devStack(t, p, "  - {name: back, run: [bin/up], restore: true}\n  - {name: later, run: [bin/up], after_apps: true}\n"+
		"provides: [demo]\napp_env: {APP_GREETING: hello}\n")
	p.Meta.Apps = []project.StackRef{appStack(t, "hello",
		"targets:\n  demo:\n    steps:\n      - {name: apply, run: [bin/app]}\n      - {name: refill, run: [bin/app], restore: true}\n")}
	fakeEngines(t, &fakeRunner{})
	m, dir := CachedStack(p)
	ui := &fakeUI{}
	e, _, err := newEngine(context.Background(), ui, true, p, m, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		restore bool
		want    string
	}{
		{"a deploy", false, "up hello/apply later"},
		{"a restore", true, "up back hello/apply hello/refill later"},
	} {
		t.Run(c.name, func(t *testing.T) {
			jobs, err := BuildJobs(context.Background(), ui, true, p, m, e, c.restore, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := jobNames(jobs); got != c.want {
				t.Fatalf("jobs %q, want %q", got, c.want)
			}
			for _, j := range jobs {
				app := j.Part() == "hello"
				switch {
				case !app && j.Engine != e:
					t.Errorf("%s runs on an engine of its own", j.Name)
				case app && (j.Engine == e || j.Engine.App != "hello" || j.Engine.Manifest.Name != "hello"):
					t.Errorf("%s runs on the engine of %q, %s", j.Name, j.Engine.App, j.Engine.Manifest.Name)
				case app && j.Engine.BaseEnv["APP_GREETING"] != "hello":
					t.Errorf("%s is not given the app_env of the platform: %v", j.Name, j.Engine.BaseEnv)
				}
			}
		})
	}

	p.Meta.Apps = []project.StackRef{appStack(t, "elsewhere", "targets:\n  nomad:\n    steps:\n      - {name: apply, run: [bin/app]}\n")}
	if _, err := BuildJobs(context.Background(), ui, true, p, m, e, false, false); err == nil ||
		err.Error() != "the app elsewhere has no way to run on demo, which provides demo" {
		t.Errorf("an app for another platform: %v", err)
	}
}

func TestADomainsStepsAfterTheAppsComeLast(t *testing.T) {
	p, _ := newProject(t, "{}\n")
	devStack(t, p, "  - {name: back, run: [bin/up], restore: true}\n  - {name: later, run: [bin/up], after_apps: true}\n"+
		"provides: [demo]\n")
	p.Meta.Apps = []project.StackRef{
		moduleStack(t, "zone", manifest.KindDomain, "targets:\n  demo:\n    steps:\n"+
			"      - {name: setup, run: [bin/app]}\n      - {name: publish, run: [bin/app], after_apps: true}\n"+
			"      - {name: refill, run: [bin/app], restore: true}\n"),
		appStack(t, "hello", "targets:\n  demo:\n    steps:\n      - {name: apply, run: [bin/app]}\n      - {name: refill, run: [bin/app], restore: true}\n"),
	}
	fakeEngines(t, &fakeRunner{})
	m, dir := CachedStack(p)
	ui := &fakeUI{}
	e, _, err := newEngine(context.Background(), ui, true, p, m, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		restore bool
		want    string
	}{
		{"a deploy", false, "up zone/setup hello/apply later zone/publish"},
		{"a restore", true, "up back zone/setup zone/refill hello/apply hello/refill later zone/publish"},
	} {
		t.Run(c.name, func(t *testing.T) {
			jobs, err := BuildJobs(context.Background(), ui, true, p, m, e, c.restore, false)
			if err != nil {
				t.Fatal(err)
			}
			if got := jobNames(jobs); got != c.want {
				t.Fatalf("jobs %q, want %q", got, c.want)
			}
			if j := jobs[len(jobs)-1]; j.Part() != "zone" || j.Engine.App != "zone" {
				t.Errorf("%s is a step of the domain, on its engine: part %s, engine of %q", j.Name, j.Part(), j.Engine.App)
			}
		})
	}
	for _, c := range []struct {
		name      string
		only      []string
		afterApps bool
		want      string
	}{
		{"the domain", []string{"zone"}, false, "zone/setup zone/publish"},
		{"an app", []string{"hello"}, false, "hello/apply"},
		{"an app and what comes after", []string{"hello"}, true, "hello/apply later zone/publish"},
	} {
		t.Run("only "+c.name, func(t *testing.T) {
			jobs, err := BuildJobs(context.Background(), ui, true, p, m, e, false, false)
			if err != nil {
				t.Fatal(err)
			}
			only := map[string]bool{}
			for _, name := range c.only {
				only[name] = true
			}
			if jobs, err = chooseJobs(only, c.afterApps, false, p, jobs, false, "pw"); err != nil {
				t.Fatal(err)
			}
			if got := jobNames(jobs); got != c.want {
				t.Errorf("jobs %q, want %q", got, c.want)
			}
		})
	}
}

func TestTheFirstDeployWithThePlatformUpPublishesTheState(t *testing.T) {
	d := newDeploy(t, runStep("a"))
	d.m.AppEnv = map[string]string{"CONSUL_HTTP_ADDR": "https://10.0.0.1:8501"}
	c := newFakeConsul()
	fakeRemotes(t, c)
	if err := os.MkdirAll(filepath.Join(d.p.Dir, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.p.Dir, "state", "net.tfstate"), []byte(`{"serial": 3}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.deploy(t, Options{}); err != nil {
		t.Fatal(err)
	}
	if pair, _, _ := c.Get("damstack/demo/state/net", nil); pair == nil || string(pair.Value) != `{"serial": 3}` {
		t.Errorf("the platform keeps the state: %+v", pair)
	}
	if meta, err := project.Open(d.p.Dir); err != nil || !meta.Meta.Shared || meta.Meta.Requires != project.SharedRequires {
		t.Errorf("the project is shared from now on: %+v, %v", meta, err)
	}
	said := false
	for _, e := range d.ui.emitted() {
		if n, ok := e.(event.Note); ok && strings.HasPrefix(n.Text, "The state of 1 steps is now kept on the platform.") {
			said = true
		}
	}
	if !said {
		t.Error("the move is said")
	}
}

func TestThePlatformsStateIsNotPublishedOver(t *testing.T) {
	d := newDeploy(t, runStep("a"))
	d.m.AppEnv = map[string]string{"CONSUL_HTTP_ADDR": "https://10.0.0.1:8501"}
	c := newFakeConsul()
	fakeRemotes(t, c)
	if ok, _, _ := c.CAS(&api.KVPair{Key: "damstack/demo/state/net", Value: []byte("theirs")}, nil); !ok {
		t.Fatal("no state on the platform")
	}
	err := d.deploy(t, Options{})
	want := "the platform already keeps the state of demo (net): set shared: true in " + filepath.Join(d.p.Dir, project.MetaFile)
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("got %v", err)
	}
	if meta, _ := project.Open(d.p.Dir); meta.Meta.Shared {
		t.Error("the project stays kept here")
	}
	if pair, _, _ := c.Get("damstack/demo/state/net", nil); string(pair.Value) != "theirs" {
		t.Errorf("the platform's state stays: %q", pair.Value)
	}
}

func TestAPlatformThatDoesNotAnswerKeepsTheStateHere(t *testing.T) {
	d := newDeploy(t, runStep("a"))
	d.m.AppEnv = map[string]string{"CONSUL_HTTP_ADDR": "https://10.0.0.1:8501"}
	c := newFakeConsul()
	c.silent = true
	fakeRemotes(t, c)
	if err := d.deploy(t, Options{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.ui.said(), "The state stays on this machine for now: ") {
		t.Errorf("said %q", d.ui.said())
	}
	if meta, _ := project.Open(d.p.Dir); meta.Meta.Shared {
		t.Error("the project stays kept here")
	}
}
