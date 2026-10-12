package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/eugene-panin/damstack/internal/ask"
	"github.com/eugene-panin/damstack/internal/checks"
	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/release"
	"github.com/eugene-panin/damstack/internal/setup"
	"github.com/eugene-panin/damstack/internal/sources"
	"github.com/eugene-panin/damstack/internal/stack"
	"github.com/eugene-panin/damstack/internal/ui/tui"
)

// catalogScreen shows the apps a project can take, and installs, updates or
// opens the one chosen.
func catalogScreen(ctx context.Context, s *streams, p *project.Project) error {
	for {
		again, err := catalogRound(ctx, s, p)
		if err != nil || !again {
			return err
		}
	}
}

// catalogRound shows the catalog once, and does what was chosen; again says
// to show it once more, as after the sources.
func catalogRound(ctx context.Context, s *streams, p *project.Project) (bool, error) {
	cfg, err := loadConfig(ctx)
	if err != nil {
		return false, err
	}
	pm, _, err := projectStack(ctx, p, "")
	if err != nil {
		return false, err
	}
	model := tui.NewCatalog(p.Meta.Name, strings.TrimSpace(p.Meta.Stack.Name+" "+p.Meta.Stack.Tag), catalogApps(cfg, p), tui.CatalogActions{
		Latest: func(a tui.CatalogApp) (string, error) {
			r, err := stack.Latest(ctx, a.URL)
			return r.Tag, err
		},
		Details: func(a tui.CatalogApp) tui.CatalogDetails {
			am, _, _, err := latestApp(ctx, a.URL)
			if err != nil {
				return tui.CatalogDetails{Err: err.Error()}
			}
			d := tui.CatalogDetails{Needs: appNeeds(pm, am)}
			if am.Takes != "" {
				d.Notes = append(d.Notes, "takes about "+am.Takes)
			}
			d.Notes = append(d.Notes, am.Needs...)
			return d
		},
		Open: openURL,
	})
	if _, err := tea.NewProgram(model, tea.WithInput(s.in), tea.WithOutput(s.out), tea.WithContext(ctx)).Run(); err != nil {
		return false, err
	}
	c := model.Choice()
	switch c.Key {
	case "i":
		return false, installApp(ctx, s, p, c.App.URL)
	case "u":
		return false, updateApp(ctx, s, p, c.App.Name)
	case "enter":
		return false, appScreen(ctx, s, p, c.App.Name)
	case "s":
		_, err := sourcesScreen(ctx, s)
		return err == nil, err
	}
	return false, nil
}

// catalogApps are the apps of the library and those added for the project's
// platform, and the project's own, which may be from anywhere.
func catalogApps(cfg *config.Config, p *project.Project) []tui.CatalogApp {
	var out []tui.CatalogApp
	seen := map[string]bool{}
	installed := map[string]string{}
	for _, a := range p.Meta.Apps {
		installed[a.Name] = a.Tag
	}
	found := cfg.AllStacks()
	if cache, err := sources.Load(); err == nil {
		found = append(found, cache.Apps()...)
	}
	for _, st := range found {
		if st.Kind != "app" || seen[st.Name] || (st.Platform != "" && st.Platform != p.Meta.Stack.Name) {
			continue
		}
		seen[st.Name] = true
		out = append(out, tui.CatalogApp{Name: st.Name, Description: st.Description, Source: source(st.URL), URL: st.URL, Installed: installed[st.Name]})
	}
	for _, a := range p.Meta.Apps {
		if seen[a.Name] {
			continue
		}
		app := tui.CatalogApp{Name: a.Name, Source: source(a.URL), URL: a.URL, Installed: a.Tag, Private: true}
		if am, _ := cachedRef(a); am != nil {
			app.Description = am.Description
		}
		out = append(out, app)
	}
	return out
}

// source is a repository as people name it: owner/name on github.com.
func source(url string) string {
	return strings.TrimSuffix(strings.TrimPrefix(url, "https://github.com/"), ".git")
}

// latestApp fetches the newest release of an app.
func latestApp(ctx context.Context, url string) (*manifest.Manifest, string, project.StackRef, error) {
	r, err := stack.Latest(ctx, url)
	if err != nil {
		return nil, "", project.StackRef{}, err
	}
	cache, err := config.CacheDir()
	if err != nil {
		return nil, "", project.StackRef{}, err
	}
	dir, err := stack.Fetch(ctx, cache, url, r)
	if err != nil {
		return nil, "", project.StackRef{}, err
	}
	m, err := manifest.Load(dir, release.Version)
	if err != nil {
		return nil, "", project.StackRef{}, err
	}
	if !m.IsApp() {
		return nil, "", project.StackRef{}, fmt.Errorf("%s is a platform, not an app", m.Name)
	}
	return m, dir, project.StackRef{Name: m.Name, URL: url, Tag: r.Tag, Commit: r.Commit}, nil
}

// appNeeds are what an app needs of the platform, each met or not.
func appNeeds(pm, am *manifest.Manifest) []tui.FormNeed {
	var out []tui.FormNeed
	for _, need := range am.Requires.Provides {
		out = append(out, tui.FormNeed{Text: pm.Name + " provides " + need, Met: slices.Contains(pm.Provides, need)})
	}
	_, _, ok := am.Target(pm.Provides)
	out = append(out, tui.FormNeed{Text: "a way to run on " + pm.Name, Met: ok})
	return out
}

// installApp installs an app from its newest release: its questions and keys
// on the form, then a deploy of it alone.
func installApp(ctx context.Context, s *streams, p *project.Project, url string) error {
	pm, pdir, err := projectStack(ctx, p, "")
	if err != nil {
		return err
	}
	am, adir, ref, err := latestApp(ctx, url)
	if err != nil {
		return err
	}
	if _, ok := p.Meta.App(am.Name); ok {
		return fmt.Errorf("%s is an app of %s already", am.Name, p.Meta.Name)
	}
	password, err := p.Password()
	if err != nil {
		return err
	}
	side := []string{"nothing changes until you confirm the review"}
	if am.Takes != "" {
		side = append([]string{"takes about " + am.Takes}, side...)
	}
	run := func(specs []manifest.CheckSpec, v any, answers map[string]any) error {
		if failed := ask.Report(nil, func(specs []manifest.CheckSpec, v any, answers map[string]any) []checks.Result {
			return checks.Default().Run(ctx, specs, v, answers)
		}, specs, v, answers); failed != "" {
			return fmt.Errorf("%s", failed)
		}
		return nil
	}
	form := tui.NewForm(fmt.Sprintf("Install %s %s on %s", am.Name, ref.Tag, p.Meta.Name), am.Name, appNeeds(pm, am), side,
		am.Questions, am.Secrets, tui.FormActions{
			Default: func(q manifest.Question, answers map[string]any) string {
				return defaultText(q, answers)
			},
			Check: func(q manifest.Question, v any, answers map[string]any) error { return run(q.Checks, v, answers) },
			Secret: func(sec manifest.Secret, v string, answers map[string]any) error {
				return run(sec.Checks, v, answers)
			},
			Open: openURL,
		})
	if _, err := tea.NewProgram(form, tea.WithInput(s.in), tea.WithOutput(s.out), tea.WithContext(ctx)).Run(); err != nil {
		return err
	}
	answers, keys, ok := form.Result()
	if !ok {
		return nil
	}
	given := answers
	for k, v := range keys {
		given[k] = v
	}
	if err := setup.AddApp(setup.AppOptions{Manifest: am, Stack: adir, Ref: ref, Project: p, Password: password, Given: given}); err != nil {
		return err
	}
	return deployParts(ctx, s, p, pm, pdir, am.Name)
}

// defaultText is a question's default as the form shows it.
func defaultText(q manifest.Question, answers map[string]any) string {
	d := q.Default
	if text, ok := d.(string); ok && strings.Contains(text, "{{") {
		r, err := manifest.RenderDefault(q.Name, text, answers, setup.DefaultFuncs())
		if err != nil {
			return ""
		}
		d = r
	}
	switch v := d.(type) {
	case nil:
		return ""
	case bool:
		if v {
			return "yes"
		}
		return "no"
	case []any:
		parts := make([]string, len(v))
		for i, x := range v {
			parts[i] = fmt.Sprint(x)
		}
		return strings.Join(parts, ", ")
	}
	return fmt.Sprint(d)
}

// updateApp takes an app to its newest release, and deploys it alone.
func updateApp(ctx context.Context, s *streams, p *project.Project, name string) error {
	pm, pdir, err := projectStack(ctx, p, "")
	if err != nil {
		return err
	}
	i := slices.IndexFunc(p.Meta.Apps, func(a project.StackRef) bool { return a.Name == name })
	if i < 0 {
		return fmt.Errorf("%s is no app of %s", name, p.Meta.Name)
	}
	_, _, ref, err := latestApp(ctx, p.Meta.Apps[i].URL)
	if err != nil {
		return err
	}
	p.Meta.Apps[i].Tag, p.Meta.Apps[i].Commit = ref.Tag, ref.Commit
	if err := p.SaveMeta(); err != nil {
		return err
	}
	return deployParts(ctx, s, p, pm, pdir, name)
}

// deployParts deploys the named parts of a project, and the platform's steps
// that come after the apps, such as its DNS records.
func deployParts(ctx context.Context, s *streams, p *project.Project, pm *manifest.Manifest, pdir string, parts ...string) error {
	s.only = map[string]bool{}
	defer func() { s.only = nil }()
	for _, part := range parts {
		s.only[part] = true
	}
	for _, step := range pm.Steps {
		if step.AfterApps {
			s.only[step.Name] = true
		}
	}
	return runSteps(ctx, s, p, pm, pdir, "", false)
}
