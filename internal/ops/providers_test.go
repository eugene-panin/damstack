package ops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/plan"
	"github.com/eugene-panin/damstack/internal/project"
)

// capApp is an app on the demo platform whose damstack.yaml has requires
// and the rest as given.
func capApp(t *testing.T, name, requires, more string) project.StackRef {
	t.Helper()
	dir := t.TempDir()
	for path, text := range map[string]string{
		manifest.File: "apiVersion: " + manifest.APIVersion + "\nname: " + name + "\nkind: app\ndescription: an app of a test\n" +
			"requires: {provides: [demo]" + requires + "}\ncheck: {answers: answers.yaml, platform: answers.yaml}\n" +
			"targets:\n  demo:\n    steps:\n      - {name: apply, run: [bin/app]}\n" + more,
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

type fakeFinder struct {
	found map[string][]plan.Provider
	down  bool
	asked int
}

func (f *fakeFinder) Find(_ context.Context, c manifest.Capability) ([]plan.Provider, error) {
	f.asked++
	if f.down {
		return nil, errors.New("the platform's Consul does not answer")
	}
	return f.found[c.String()], nil
}

func fakeFinders(t *testing.T, f *fakeFinder) {
	t.Helper()
	old := openFinder
	t.Cleanup(func() { openFinder = old })
	openFinder = func(env map[string]string) (plan.Finder, error) {
		if env["CONSUL_HTTP_ADDR"] == "" {
			t.Error("the finder is opened without the platform's Consul")
		}
		return f, nil
	}
}

func TestAnAppIsGivenTheProvidersItRequiresAndConnectsTo(t *testing.T) {
	p, _ := newProject(t, "apps:\n  grafana:\n    features: {logs: true}\n    connect:\n      metrics@1: [mimir]\n  hello: {}\n  mimir: {}\n")
	devStack(t, p, "provides: [demo]\napp_env: {CONSUL_HTTP_ADDR: 'https://10.77.0.1:8501'}\n")
	p.Meta.Apps = []project.StackRef{
		capApp(t, "mimir", "", "provides: [metrics@1]\n"),
		capApp(t, "grafana", ", capabilities: [{capability: logs@1, when: features.logs}]", "uses: [metrics@1, traces@1]\n"),
		appStack(t, "hello", "targets:\n  demo:\n    steps:\n      - {name: apply, run: [bin/app]}\n"),
	}
	mimir := plan.Provider{Name: "mimir", Address: "10.77.0.1", Port: 9009, Meta: map[string]string{"path": "/prometheus"}}
	loki := plan.Provider{Name: "loki", Address: "10.77.0.1", Port: 3100, Meta: map[string]string{}}
	f := &fakeFinder{found: map[string][]plan.Provider{
		"metrics@1": {mimir, {Name: "prometheus", Address: "10.77.0.2", Port: 9090}},
		"logs@1":    {loki},
	}}
	fakeFinders(t, f)
	fakeEngines(t, &fakeRunner{})
	m, dir := CachedStack(p)
	ui := &fakeUI{}
	e, _, err := newEngine(context.Background(), ui, true, p, m, dir)
	if err != nil {
		t.Fatal(err)
	}
	given := func() map[string]plan.Providers {
		jobs, err := BuildJobs(context.Background(), ui, true, p, m, e, false, false)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]plan.Providers{}
		for _, j := range jobs {
			if j.Part() != j.Name {
				out[j.Part()] = j.Engine.Providers
			}
		}
		return out
	}

	got := given()
	if want := (plan.Providers{"logs@1": {loki}, "metrics@1": {mimir}}); !reflect.DeepEqual(got["grafana"], want) {
		t.Errorf("grafana is given %+v", got["grafana"])
	}
	if got["hello"] != nil || got["mimir"] != nil {
		t.Errorf("apps that need nothing are given %+v and %+v", got["hello"], got["mimir"])
	}

	f.down, f.asked = true, 0
	got = given()
	if want := (plan.Providers{"logs@1": {}, "metrics@1": {{Name: "mimir", Meta: map[string]string{}}}}); !reflect.DeepEqual(got["grafana"], want) {
		t.Errorf("the platform down, grafana is given %+v", got["grafana"])
	}
	if f.asked != 1 {
		t.Errorf("Consul was asked %d times once it did not answer", f.asked)
	}
	if !strings.Contains(ui.said(), "Could not look for the providers of grafana on the platform") {
		t.Errorf("said %q", ui.said())
	}
}

func TestAPlatformWithoutConsulIsNotAsked(t *testing.T) {
	p, _ := newProject(t, "apps:\n  grafana: {connect: {metrics@1: [mimir]}}\n")
	devStack(t, p, "provides: [demo]\n")
	p.Meta.Apps = []project.StackRef{capApp(t, "grafana", "", "uses: [metrics]\n")}
	fakeFinders(t, &fakeFinder{})
	openFinder = func(map[string]string) (plan.Finder, error) {
		t.Error("a finder is opened for a platform without Consul")
		return nil, nil
	}
	fakeEngines(t, &fakeRunner{})
	m, dir := CachedStack(p)
	e, _, err := newEngine(context.Background(), &fakeUI{}, true, p, m, dir)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := BuildJobs(context.Background(), &fakeUI{}, true, p, m, e, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := jobs[len(jobs)-1].Engine.Providers; !reflect.DeepEqual(got, plan.Providers{"metrics@1": {}}) {
		t.Errorf("got %+v", got)
	}
}
