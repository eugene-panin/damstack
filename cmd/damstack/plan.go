package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/eugene-panin/damstack/internal/ask"
	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/ops"
	"github.com/eugene-panin/damstack/internal/plan"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/setup"
	"github.com/eugene-panin/damstack/internal/sources"
)

// planned is a module of a plan: its manifest, where it is, its release, and
// once its questions are asked, what Add adds.
type planned struct {
	m       *manifest.Manifest
	dir     string
	ref     project.StackRef
	pending *setup.Pending
}

// planFlow is how a plan reaches the person: on the terminal, or on the forms
// of the catalog.
type planFlow struct {
	// show shows the plan, and what the app can connect to, before any
	// question; false stops. grows is whether more than the app joins;
	// connect is the question whether to connect, empty when there is nothing
	// to; connected is the answer when it was asked, nil when it was not.
	show func(lines []string, grows bool, connect string) (ok bool, connected *bool, err error)
	// prepare asks the questions of a module, and for the app, when show did
	// not, connect.
	prepare func(a *planned, lines []string, connect string) (*setup.Pending, *bool, error)
	// choose picks one of several modules that provide a capability.
	choose func(c manifest.Capability, by string, candidates []plan.Module) (string, error)
	// say tells what changed in the plan after the answers.
	say func(lines []string)
}

// errNotAdded is a plan the person said no to.
var errNotAdded = errors.New("nothing was added, as you said")

// addPlan works out what joins the project with the app root: the providers
// of what it requires that the project does not have, theirs in turn. It
// shows the plan, asks the questions of each module in its order, and gives
// them in that order to be added. An app that requires and uses no
// capabilities is all of its plan, and nothing is looked up.
func addPlan(ctx context.Context, p *project.Project, pm *manifest.Manifest, pdir string, root *planned, f planFlow) ([]*planned, error) {
	am := root.m
	if len(am.Requires.Capabilities) == 0 && len(am.Uses) == 0 {
		pending, _, err := f.prepare(root, nil, "")
		if err != nil {
			return nil, err
		}
		root.pending = pending
		return []*planned{root}, nil
	}
	cfg, err := ops.LoadConfig(ctx)
	if err != nil {
		return nil, err
	}
	platform := p.Meta.Stack.Name
	stacks := catalogStacks(cfg, platform)
	catalog := ops.CatalogModules(stacks)
	have := ops.Have(pm, ops.Installed(p, catalog))
	password, err := p.Password()
	if err != nil {
		return nil, err
	}
	var lines []string
	finder, err := ops.PlatformFinder(p, pm, pdir, password)
	if err != nil {
		lines = append(lines, fmt.Sprintf("Could not look for providers on the platform: %v.", err))
	}
	caps := manifest.NeedCapabilities(append(slices.Clone(am.Requires.Capabilities), am.Uses...))
	found, reached := plan.Discover(ctx, finder, caps, have)
	if finder != nil && !reached {
		lines = append(lines, "The platform's Consul does not answer, so damstack could not look for providers there: these are those of "+project.MetaFile+".")
	}
	for _, c := range caps {
		if len(have[c]) == 0 {
			for _, prov := range found[c.String()] {
				have[c] = append(have[c], prov.Name)
			}
		}
	}
	in := plan.Input{Root: plan.FromManifest(am, platform, nil), Platform: platform, Have: have, Catalog: catalog,
		Known: map[string]plan.Module{}, Chosen: map[manifest.Capability]string{}, Choose: f.choose}
	pl, err := plan.Resolve(in)
	if err != nil {
		return nil, err
	}
	lines = append(lines, planLines(am.Name, pl)...)
	connect, offer := usesOffer(am, found, catalog, platform, pl)
	lines = append(lines, offer...)
	ok, connected, err := f.show(lines, len(pl.Modules) > 1, connect)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errNotAdded
	}

	mods := map[string]*planned{am.Name: root}
	for {
		var next []string
		for _, m := range pl.Modules {
			if mods[m.Name] == nil || mods[m.Name].pending == nil {
				next = append(next, m.Name)
			}
		}
		if len(next) == 0 {
			break
		}
		for _, name := range next {
			a := mods[name]
			if a == nil {
				if a, err = fetchPlanned(ctx, stacks, name, pm); err != nil {
					return nil, err
				}
				mods[name] = a
			}
			question := ""
			if a == root && connected == nil {
				question = connect
			}
			pending, c, err := f.prepare(a, lines, question)
			if err != nil {
				return nil, err
			}
			if a == root && c != nil {
				connected = c
			}
			a.pending = pending
			mod := plan.FromManifest(a.m, platform, pending.Settings())
			in.Known[name] = mod
			if a == root {
				in.Root = mod
			}
		}
		before := pl.Names()
		if pl, err = plan.Resolve(in); err != nil {
			return nil, err
		}
		if added := slices.DeleteFunc(pl.Names(), func(n string) bool { return slices.Contains(before, n) }); len(added) > 0 {
			f.say(append([]string{fmt.Sprintf("With these answers, %s joins too:", strings.Join(added, ", "))}, planLines(am.Name, pl)...))
		}
	}
	if connect != "" && connected != nil {
		if err := root.pending.SetConnect(connectFor(am, root.pending.Settings(), found, pl, *connected)); err != nil {
			return nil, err
		}
	}
	order := make([]*planned, len(pl.Modules))
	for i, m := range pl.Modules {
		order[i] = mods[m.Name]
	}
	return order, nil
}

// catalogStacks are the stacks of the library and of the sources, for apps
// that may go on platform.
func catalogStacks(cfg *config.Config, platform string) []config.Stack {
	found := cfg.AllStacks()
	if cache, err := sources.Load(); err == nil {
		found = append(found, cache.Apps()...)
	}
	return slices.DeleteFunc(found, func(st config.Stack) bool { return st.Platform != "" && st.Platform != platform })
}

// fetchPlanned fetches the newest release of a module the plan brings, by
// its name in the catalog.
func fetchPlanned(ctx context.Context, stacks []config.Stack, name string, pm *manifest.Manifest) (*planned, error) {
	i := slices.IndexFunc(stacks, func(st config.Stack) bool { return st.Name == name })
	if i < 0 {
		return nil, fmt.Errorf("%s is not in the catalog", name)
	}
	m, dir, ref, err := latestApp(ctx, project.StackRef{Name: name, URL: stacks[i].URL, Path: stacks[i].Path})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if err := fits(pm, m); err != nil {
		return nil, err
	}
	return &planned{m: m, dir: dir, ref: ref}, nil
}

// fits is nil when the app am can go on the platform pm.
func fits(pm, am *manifest.Manifest) error {
	if !am.IsApp() {
		return fmt.Errorf("%s is a platform, not an app; damstack deploy sets up a project on it", am.Name)
	}
	for _, need := range am.Requires.Provides {
		if !manifest.Has(pm.Provides, need) {
			return fmt.Errorf("%s needs %s, which the platform %s of this project does not provide", am.Name, need, pm.Name)
		}
	}
	if _, _, ok := am.Target(pm.Provides); !ok {
		return fmt.Errorf("%s has no way to run on %s, which provides %s", am.Name, pm.Name, strings.Join(pm.Provides, ", "))
	}
	return nil
}

// planLines say what joins the project with root, in order, and what may by
// the answers.
func planLines(root string, pl plan.Plan) []string {
	var out []string
	if len(pl.Modules) > 1 {
		out = append(out, root+" needs what the project does not have yet; these join it, in this order:")
		var b bytes.Buffer
		w := tabwriter.NewWriter(&b, 0, 4, 3, ' ', 0)
		for _, m := range pl.Modules {
			fmt.Fprintf(w, "  %s\t%s\tnew\n", m.Name, strings.Join(pl.For[m.Name], ", "))
		}
		w.Flush()
		out = append(out, strings.Split(strings.TrimRight(b.String(), "\n"), "\n")...)
	}
	for _, m := range pl.Maybe {
		out = append(out, fmt.Sprintf("%s needs %s too when %s: settled after its questions.", m.Module, m.Need.Capability, m.Need.When))
	}
	return out
}

// usesOffer is what an app that uses capabilities can connect to: the
// question, empty when nothing provides them, and the lines that show them.
func usesOffer(am *manifest.Manifest, found plan.Providers, catalog []plan.Module, platform string, pl plan.Plan) (string, []string) {
	var rows, missing []string
	for _, c := range manifest.NeedCapabilities(am.Uses) {
		names := providerNames(c, found, pl)
		for _, prov := range found[c.String()] {
			rows = append(rows, fmt.Sprintf("  %s\t%s\t%s", prov.Name, c, reach(prov)))
		}
		for _, name := range names {
			if !slices.ContainsFunc(found[c.String()], func(p plan.Provider) bool { return p.Name == name }) {
				rows = append(rows, fmt.Sprintf("  %s\t%s\tjoins with this plan", name, c))
			}
		}
		if len(names) > 0 {
			continue
		}
		for _, m := range catalog {
			if slices.Contains(m.Provides, c) && (m.Platform == "" || m.Platform == platform) {
				missing = append(missing, fmt.Sprintf("%s (%s)", m.Name, c))
			}
		}
	}
	var out []string
	question := ""
	if len(rows) > 0 {
		out = append(out, am.Name+" can use what these give:")
		var b bytes.Buffer
		w := tabwriter.NewWriter(&b, 0, 4, 3, ' ', 0)
		for _, r := range rows {
			fmt.Fprintln(w, r)
		}
		w.Flush()
		out = append(out, strings.Split(strings.TrimRight(b.String(), "\n"), "\n")...)
		question = "Connect them?"
	}
	if len(missing) > 0 {
		out = append(out, fmt.Sprintf("%s can use more; you can add: %s", am.Name, strings.Join(missing, ", ")))
	}
	return question, out
}

// reach is how a provider is reached, as Consul says.
func reach(p plan.Provider) string {
	if p.Address == "" {
		return "on the project"
	}
	s := p.Address
	if p.Port > 0 {
		s += ":" + strconv.Itoa(p.Port)
	}
	return s + p.Meta["path"]
}

// providerNames are the providers of c found on the platform or joining with
// the plan.
func providerNames(c manifest.Capability, found plan.Providers, pl plan.Plan) []string {
	var names []string
	for _, prov := range found[c.String()] {
		names = append(names, prov.Name)
	}
	for _, m := range pl.Modules {
		if slices.Contains(m.Provides, c) && !slices.Contains(names, m.Name) {
			names = append(names, m.Name)
		}
	}
	return names
}

// connectFor is the connect of an app: for each capability it uses under its
// settings, the providers there are when yes, none when no.
func connectFor(am *manifest.Manifest, settings map[string]any, found plan.Providers, pl plan.Plan, yes bool) map[string][]string {
	out := map[string][]string{}
	sure, _ := manifest.Split(am.Uses, settings)
	for _, c := range manifest.NeedCapabilities(sure) {
		out[c.String()] = []string{}
		if yes {
			if names := providerNames(c, found, pl); names != nil {
				out[c.String()] = names
			}
		}
	}
	return out
}

// terminalFlow is a plan on the terminal: the questions with the prompter,
// answers from given, and with yes no question but the modules' own. The
// answers of a module the plan brings are given under its name.
func terminalFlow(s *cli, p *project.Project, password, root string, given map[string]any, yes bool) planFlow {
	return planFlow{
		show: func(lines []string, grows bool, connect string) (bool, *bool, error) {
			if len(lines) > 0 {
				fmt.Fprintln(s.out, strings.Join(lines, "\n"))
				fmt.Fprintln(s.out)
			}
			if grows && !yes && !s.prompt.NoTerminal {
				if ok, err := s.prompt.Confirm("Go?", true); err != nil || !ok {
					return false, nil, err
				}
			}
			switch {
			case connect == "" || (s.prompt.NoTerminal && !yes):
				return true, nil, nil
			case yes:
				ok := true
				return true, &ok, nil
			}
			ok, err := s.prompt.Confirm(connect, true)
			return true, &ok, err
		},
		prepare: func(a *planned, _ []string, _ string) (*setup.Pending, *bool, error) {
			own := map[string]any{}
			if a.m.Name == root {
				for k, v := range given {
					if _, sub := v.(map[string]any); !sub || isSetting(a.m, k) {
						own[k] = v
					}
				}
			} else {
				own, _ = given[a.m.Name].(map[string]any)
				if !s.prompt.NoTerminal {
					fmt.Fprintf(s.prompt.Out, "\n── %s: %s\n", a.m.Name, a.m.Description)
				}
			}
			pending, err := setup.PrepareApp(setup.AppOptions{Manifest: a.m, Stack: a.dir, Ref: a.ref, Project: p,
				Password: password, Given: own, Prompter: s.prompt})
			return pending, nil, err
		},
		choose: func(c manifest.Capability, by string, candidates []plan.Module) (string, error) {
			names := make([]string, len(candidates))
			for i, m := range candidates {
				names[i] = m.Name
				fmt.Fprintf(s.prompt.Out, "  %s: %s\n", m.Name, m.Description)
			}
			prompter := s.prompt
			if yes {
				prompter = nil
			}
			answers, err := ask.Questions(prompter, []manifest.Question{{Name: "provider", Type: "enum", Options: names, Default: names[0],
				Prompt: fmt.Sprintf("%s needs %s; which of these gives it?", by, c)}}, nil, nil, nil)
			if err != nil {
				return "", err
			}
			name, _ := answers["provider"].(string)
			return name, nil
		},
		say: func(lines []string) { fmt.Fprintln(s.out, "\n"+strings.Join(lines, "\n")) },
	}
}

// isSetting is whether name is a question or a secret of m.
func isSetting(m *manifest.Manifest, name string) bool {
	return slices.ContainsFunc(m.Questions, func(q manifest.Question) bool { return q.Name == name }) ||
		slices.ContainsFunc(m.Secrets, func(sec manifest.Secret) bool { return sec.Name == name })
}
