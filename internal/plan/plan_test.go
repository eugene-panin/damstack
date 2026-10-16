package plan

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/eugene-panin/damstack/internal/manifest"
)

func caps(list ...string) []manifest.Capability { return manifest.Capabilities(list) }

func needs(list ...string) []manifest.Need {
	out := make([]manifest.Need, len(list))
	for i, s := range list {
		c, when, _ := strings.Cut(s, " when ")
		out[i] = manifest.Need{Capability: c, When: when}
	}
	return out
}

func cap1(s string) manifest.Capability {
	c, err := manifest.ParseCapability(s)
	if err != nil {
		panic(err)
	}
	return c
}

var catalog = []Module{
	{Name: "postgres", Platform: "hashi", Provides: caps("postgres@16")},
	{Name: "traefik", Platform: "hashi", Provides: caps("ingress-public@1"), Requires: needs("tls-certificates@1")},
	{Name: "manual", Platform: "hashi", Provides: caps("tls-certificates@1", "public-dns@1")},
	{Name: "cloudflare", Platform: "hashi", Provides: caps("tls-certificates@1", "public-dns@1"), Recommended: true},
	{Name: "k8s-postgres", Platform: "k0s", Provides: caps("postgres@15")},
	{Name: "mimir", Platform: "hashi", Provides: caps("metrics@1")},
}

func have(m map[string]string) map[manifest.Capability][]string {
	out := map[manifest.Capability][]string{}
	for c, by := range m {
		out[cap1(c)] = []string{by}
	}
	return out
}

func TestWhatTheProjectHasJoinsNothing(t *testing.T) {
	pl, err := Resolve(Input{Root: Module{Name: "mail", Requires: needs("nomad@1")}, Platform: "hashi",
		Have: have(map[string]string{"nomad": "hashi"}), Catalog: catalog})
	if err != nil || !reflect.DeepEqual(pl.Names(), []string{"mail"}) {
		t.Errorf("got %v, %v", pl.Names(), err)
	}
}

func TestTheOneProviderJoinsBeforeWhatRequiresIt(t *testing.T) {
	pl, err := Resolve(Input{Root: Module{Name: "odoo", Requires: needs("postgres@16")}, Platform: "hashi", Catalog: catalog})
	if err != nil || !reflect.DeepEqual(pl.Names(), []string{"postgres", "odoo"}) {
		t.Fatalf("got %v, %v", pl.Names(), err)
	}
	if !reflect.DeepEqual(pl.For["postgres"], []string{"postgres@16 for odoo"}) {
		t.Errorf("for %v", pl.For)
	}
}

func TestOfSeveralProvidersOneIsAskedTheRecommendedFirst(t *testing.T) {
	var asked []string
	in := Input{Root: Module{Name: "site", Requires: needs("ingress-public@1")}, Platform: "hashi", Catalog: catalog, Chosen: map[manifest.Capability]string{},
		Choose: func(c manifest.Capability, by string, candidates []Module) (string, error) {
			for _, m := range candidates {
				asked = append(asked, m.Name)
			}
			if c.String() != "tls-certificates@1" || by != "traefik" {
				t.Errorf("asked for %s of %s", c, by)
			}
			return "manual", nil
		}}
	pl, err := Resolve(in)
	if err != nil || !reflect.DeepEqual(pl.Names(), []string{"manual", "traefik", "site"}) {
		t.Fatalf("got %v, %v", pl.Names(), err)
	}
	if !reflect.DeepEqual(asked, []string{"cloudflare", "manual"}) {
		t.Errorf("asked %v", asked)
	}
	asked = nil
	if pl, err := Resolve(in); err != nil || len(asked) != 0 || pl.Names()[0] != "manual" {
		t.Errorf("chosen once, asked again: %v, %v, %v", asked, pl.Names(), err)
	}
}

func TestNoProviderStopsAndNamesTheCapability(t *testing.T) {
	_, err := Resolve(Input{Root: Module{Name: "odoo", Requires: needs("postgres@15")}, Platform: "hashi", Catalog: catalog})
	var missing *MissingError
	if !errors.As(err, &missing) || missing.Capability.String() != "postgres@15" || !strings.Contains(err.Error(), "odoo requires postgres@15") {
		t.Errorf("got %v", err)
	}
}

func TestEachModuleComesAfterWhatItRequires(t *testing.T) {
	cat := []Module{
		{Name: "aa", Provides: caps("aa@1"), Requires: needs("bb@1", "cc@1")},
		{Name: "bb", Provides: caps("bb@1"), Requires: needs("cc@1")},
		{Name: "cc", Provides: caps("cc@1")},
	}
	pl, err := Resolve(Input{Root: Module{Name: "app", Requires: needs("aa@1", "cc@1")}, Platform: "hashi", Catalog: cat})
	if err != nil || !reflect.DeepEqual(pl.Names(), []string{"cc", "bb", "aa", "app"}) {
		t.Errorf("got %v, %v", pl.Names(), err)
	}
}

func TestModulesThatRequireEachOtherAreRefused(t *testing.T) {
	cat := []Module{
		{Name: "aa", Provides: caps("aa@1"), Requires: needs("bb@1")},
		{Name: "bb", Provides: caps("bb@1"), Requires: needs("app@1")},
	}
	_, err := Resolve(Input{Root: Module{Name: "app", Provides: caps("app@1"), Requires: needs("aa@1")}, Platform: "hashi", Catalog: cat})
	var cycle *CycleError
	if !errors.As(err, &cycle) || !reflect.DeepEqual(cycle.Path, []string{"app", "aa", "bb", "app"}) {
		t.Errorf("got %v", err)
	}
}

func TestANeedUnderAConditionWaitsForTheSettings(t *testing.T) {
	root := Module{Name: "odoo", Requires: needs("postgres@16 when postgres.mode=external")}
	pl, err := Resolve(Input{Root: root, Platform: "hashi", Catalog: catalog})
	if err != nil || !reflect.DeepEqual(pl.Names(), []string{"odoo"}) || len(pl.Maybe) != 1 || pl.Maybe[0].Need.Capability != "postgres@16" {
		t.Fatalf("before: %v, %+v, %v", pl.Names(), pl.Maybe, err)
	}
	root.Settings = map[string]any{"postgres": map[string]any{"mode": "bundled"}}
	if pl, err := Resolve(Input{Root: root, Platform: "hashi", Catalog: catalog}); err != nil || len(pl.Modules) != 1 || len(pl.Maybe) != 0 {
		t.Errorf("bundled: %v, %+v, %v", pl.Names(), pl.Maybe, err)
	}
	root.Settings = map[string]any{"postgres": map[string]any{"mode": "external"}}
	if pl, err := Resolve(Input{Root: root, Platform: "hashi", Catalog: catalog}); err != nil || !reflect.DeepEqual(pl.Names(), []string{"postgres", "odoo"}) {
		t.Errorf("external: %v, %v", pl.Names(), err)
	}
}

func TestAKnownModuleIsTakenOverItsEntry(t *testing.T) {
	known := map[string]Module{"postgres": {Name: "postgres", Provides: caps("postgres@16"), Requires: needs("s3@1 when backups")}}
	pl, err := Resolve(Input{Root: Module{Name: "odoo", Requires: needs("postgres@16")}, Platform: "hashi", Catalog: catalog, Known: known})
	if err != nil || len(pl.Maybe) != 1 || pl.Maybe[0].Module != "postgres" {
		t.Errorf("got %+v, %v", pl, err)
	}
}

func TestAModuleOthersRequireIsNotRemoved(t *testing.T) {
	installed := []Module{
		{Name: "postgres", Provides: caps("postgres@16")},
		{Name: "odoo", Requires: needs("postgres@16 when postgres.mode=external"), Settings: map[string]any{"postgres": map[string]any{"mode": "external"}}},
		{Name: "wiki", Requires: needs("postgres@16 when db=external"), Settings: map[string]any{"db": "bundled"}},
		{Name: "grafana", Requires: needs("nomad@1")},
	}
	if got := RequiredBy("postgres", installed, caps("nomad@1")); !reflect.DeepEqual(got, []string{"odoo"}) {
		t.Errorf("got %v", got)
	}
	if got := RequiredBy("postgres", append(installed, Module{Name: "pg2", Provides: caps("postgres@16")}), nil); len(got) != 0 {
		t.Errorf("another provides it: %v", got)
	}
	if got := RequiredBy("grafana", installed, caps("nomad@1")); len(got) != 0 {
		t.Errorf("nothing requires grafana: %v", got)
	}
}

type fakeFinder struct {
	services map[string][]Provider
	down     bool
	asked    []string
}

func (f *fakeFinder) Find(_ context.Context, c manifest.Capability) ([]Provider, error) {
	f.asked = append(f.asked, c.String())
	if f.down {
		return nil, errors.New("connection refused")
	}
	return f.services[c.String()], nil
}

func TestProvidersAreFoundOnThePlatformOrInTheProject(t *testing.T) {
	mimir := Provider{Name: "mimir", Address: "10.0.0.5", Port: 9009, Meta: map[string]string{"path": "/prometheus"}}
	f := &fakeFinder{services: map[string][]Provider{"metrics@1": {mimir}}}
	fallback := map[manifest.Capability][]string{cap1("metrics@1"): {"mimir"}, cap1("logs@1"): {"loki"}}

	got, reached := Discover(t.Context(), f, caps("metrics@1", "logs@1"), fallback)
	if !reached || !reflect.DeepEqual(got, Providers{"metrics@1": {mimir}, "logs@1": nil}) {
		t.Errorf("found: %v, %v", got, reached)
	}
	if !slices.Equal(f.asked, []string{"metrics@1", "logs@1"}) {
		t.Errorf("asked %v", f.asked)
	}

	f.down = true
	got, reached = Discover(t.Context(), f, caps("metrics@1", "logs@1"), fallback)
	if reached || got["logs@1"][0].Name != "loki" || got["metrics@1"][0].Name != "mimir" || got["metrics@1"][0].Address != "" {
		t.Errorf("down: %v, %v", got, reached)
	}
	if got, reached := Discover(t.Context(), nil, caps("s3@1"), fallback); reached || len(got["s3@1"]) != 0 {
		t.Errorf("no Consul, no provider: %v, %v", got, reached)
	}

	only := Providers{"metrics@1": {mimir, {Name: "prometheus"}}}.Only(map[string][]string{"metrics@1": {"prometheus"}})
	if len(only["metrics@1"]) != 1 || only["metrics@1"][0].Name != "prometheus" {
		t.Errorf("only %v", only)
	}
}
