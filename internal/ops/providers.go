package ops

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/plan"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/remote"
)

// Connect is the setting of an app's block in stack.yaml damstack owns: the
// providers it connects to, by name, for each capability it uses.
const Connect = "connect"

// openFinder finds providers in the platform's Consul; tests put a fake in
// its place.
var openFinder = func(env map[string]string) (plan.Finder, error) {
	f, err := remote.OpenFinder(env)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// PlatformFinder finds providers on a project's platform, in the Consul its
// app_env names; nil when it names none.
func PlatformFinder(p *project.Project, m *manifest.Manifest, dir, password string) (plan.Finder, error) {
	if _, ok := m.AppEnv["CONSUL_HTTP_ADDR"]; !ok {
		return nil, nil
	}
	env, err := AppEnv(p, m, dir, password)
	if err != nil {
		return nil, err
	}
	return openFinder(env)
}

// Settings are the settings of an app under apps of stack.yaml.
func Settings(p *project.Project, app string) map[string]any {
	config, err := p.Config()
	if err != nil {
		return map[string]any{}
	}
	apps, _ := config["apps"].(map[string]any)
	block, _ := apps[app].(map[string]any)
	if block == nil {
		block = map[string]any{}
	}
	return block
}

// CatalogModules are the apps of a catalog as a plan knows them.
func CatalogModules(stacks []config.Stack) []plan.Module {
	var out []plan.Module
	seen := map[string]bool{}
	for _, st := range stacks {
		if st.Kind != manifest.KindApp || seen[st.Name] {
			continue
		}
		seen[st.Name] = true
		out = append(out, plan.Module{Name: st.Name, Platform: st.Platform, Description: st.Description,
			Provides: manifest.Capabilities(st.Provides), Requires: st.Requires, Uses: st.Uses, Recommended: st.Recommended})
	}
	return out
}

// Installed are the apps of a project as a plan knows them: from their
// manifests on this machine, with their settings, or else from catalog.
func Installed(p *project.Project, catalog []plan.Module) []plan.Module {
	var out []plan.Module
	for _, ref := range p.Meta.Apps {
		if am, _ := CachedRef(ref); am != nil {
			out = append(out, plan.FromManifest(am, p.Meta.Stack.Name, Settings(p, ref.Name)))
			continue
		}
		m := plan.Module{Name: ref.Name}
		if i := slices.IndexFunc(catalog, func(c plan.Module) bool { return c.Name == ref.Name }); i >= 0 {
			m = catalog[i]
		}
		m.Settings = Settings(p, ref.Name)
		out = append(out, m)
	}
	return out
}

// Have are the capabilities a project has: its platform's and its apps', each
// with what provides it.
func Have(pm *manifest.Manifest, installed []plan.Module) map[manifest.Capability][]string {
	have := map[manifest.Capability][]string{}
	for _, c := range manifest.Capabilities(pm.Provides) {
		have[c] = append(have[c], pm.Name)
	}
	for _, m := range installed {
		for _, c := range m.Provides {
			have[c] = append(have[c], m.Name)
		}
	}
	return have
}

// Wanted are the capabilities an app with settings is given providers of:
// those it requires that hold, and those it connects to by its connect.
func Wanted(am *manifest.Manifest, settings map[string]any) ([]manifest.Capability, map[string][]string) {
	sure, _ := manifest.Split(am.Requires.Capabilities, settings)
	caps := manifest.NeedCapabilities(sure)
	connect := ConnectOf(settings)
	for _, c := range manifest.NeedCapabilities(am.Uses) {
		if _, ok := connect[c.String()]; ok && !slices.Contains(caps, c) {
			caps = append(caps, c)
		}
	}
	return caps, connect
}

// ConnectOf is the connect of an app's settings: provider names by
// capability.
func ConnectOf(settings map[string]any) map[string][]string {
	out := map[string][]string{}
	raw, _ := settings[Connect].(map[string]any)
	for c, v := range raw {
		list, _ := v.([]any)
		names := []string{}
		for _, item := range list {
			if name, ok := item.(string); ok {
				names = append(names, name)
			}
		}
		if cp, err := manifest.ParseCapability(c); err == nil {
			out[cp.String()] = names
		}
	}
	return out
}

// appProviders are the providers an app is given on a deploy: for what it
// requires, every one found; for what it uses, those its connect names.
func appProviders(ctx context.Context, errW io.Writer, f plan.Finder, am *manifest.Manifest, settings map[string]any,
	fallback map[manifest.Capability][]string) plan.Providers {
	caps, connect := Wanted(am, settings)
	if len(caps) == 0 {
		return nil
	}
	found, reached := plan.Discover(ctx, f, caps, fallback)
	if !reached && f != nil {
		fmt.Fprintf(errW, "Could not look for the providers of %s on the platform; %s gets those of the project, by name only.\n", am.Name, am.Name)
	}
	required := map[string]bool{}
	sure, _ := manifest.Split(am.Requires.Capabilities, settings)
	for _, c := range manifest.NeedCapabilities(sure) {
		required[c.String()] = true
	}
	out := plan.Providers{}
	only := found.Only(connect)
	for _, c := range caps {
		key := c.String()
		if required[key] {
			out[key] = found[key]
		} else {
			out[key] = only[key]
		}
		if out[key] == nil {
			out[key] = []plan.Provider{}
		}
	}
	return out
}

// quiet is a Finder that, once it did not answer, is not asked again.
type quiet struct {
	f    plan.Finder
	down error
}

func (q *quiet) Find(ctx context.Context, c manifest.Capability) ([]plan.Provider, error) {
	if q.down != nil {
		return nil, q.down
	}
	found, err := q.f.Find(ctx, c)
	q.down = err
	return found, err
}
