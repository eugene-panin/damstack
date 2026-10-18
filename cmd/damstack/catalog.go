package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/eugene-panin/damstack/internal/ask"
	"github.com/eugene-panin/damstack/internal/checks"
	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/ops"
	"github.com/eugene-panin/damstack/internal/plan"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/setup"
	"github.com/eugene-panin/damstack/internal/sources"
	"github.com/eugene-panin/damstack/internal/stack"
	"github.com/eugene-panin/damstack/internal/ui/tui"
)

// catalogScreen shows the apps a project can take, and installs, updates or
// opens the one chosen.
func catalogScreen(ctx context.Context, s *cli, p *project.Project) error {
	for {
		again, err := catalogRound(ctx, s, p)
		if err != nil || !again {
			return err
		}
	}
}

// catalogRound shows the catalog once, and does what was chosen; again says
// to show it once more, as after the sources.
func catalogRound(ctx context.Context, s *cli, p *project.Project) (bool, error) {
	cfg, err := ops.LoadConfig(ctx)
	if err != nil {
		return false, err
	}
	pm, _, err := ops.ProjectStack(ctx, p, "")
	if err != nil {
		return false, err
	}
	model := tui.NewCatalog(p.Meta.Name, strings.TrimSpace(p.Meta.Stack.Name+" "+p.Meta.Stack.Tag), catalogApps(cfg, p), tui.CatalogActions{
		Latest: func(a tui.CatalogApp) (string, error) {
			r, err := stack.Latest(ctx, a.URL, ops.TagPrefix(project.StackRef{Name: a.Name, Path: a.Path, Tag: a.Installed}))
			return r.Tag, err
		},
		Details: func(a tui.CatalogApp) tui.CatalogDetails {
			am, _, _, err := latestApp(ctx, catalogRef(a))
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
	if err := s.show(ctx, model); err != nil {
		return false, err
	}
	c := model.Choice()
	switch c.Key {
	case "i":
		return false, installApp(ctx, s, p, catalogRef(c.App))
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
// platform, and the project's own, which may be from anywhere; one the
// project has is shown from where it has it.
func catalogApps(cfg *config.Config, p *project.Project) []tui.CatalogApp {
	var out []tui.CatalogApp
	seen := map[string]bool{}
	installed := map[string]project.StackRef{}
	for _, a := range p.Meta.Apps {
		installed[a.Name] = a
	}
	found := cfg.AllStacks()
	if cache, err := sources.Load(); err == nil {
		found = append(found, cache.Apps()...)
	}
	for _, st := range found {
		if !st.OnPlatform() || seen[st.Name] || (st.Platform != "" && st.Platform != p.Meta.Stack.Name) {
			continue
		}
		seen[st.Name] = true
		url, path := st.URL, st.Path
		a, ok := installed[st.Name]
		if ok {
			url, path = a.URL, a.Path
		}
		out = append(out, tui.CatalogApp{Name: st.Name, Description: st.Description, Source: source(url), URL: url, Path: path, Installed: a.Tag})
	}
	for _, a := range p.Meta.Apps {
		if seen[a.Name] {
			continue
		}
		app := tui.CatalogApp{Name: a.Name, Source: source(a.URL), URL: a.URL, Path: a.Path, Installed: a.Tag, Private: true}
		if am, _ := ops.CachedRef(a); am != nil {
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

// catalogRef is what names an app of the catalog: its repository, and its
// directory in it.
func catalogRef(a tui.CatalogApp) project.StackRef {
	return project.StackRef{Name: a.Name, URL: a.URL, Path: a.Path}
}

// latestApp fetches the newest release of an app.
func latestApp(ctx context.Context, want project.StackRef) (*manifest.Manifest, string, project.StackRef, error) {
	m, dir, ref, err := ops.LatestRef(ctx, want)
	if err != nil {
		return nil, "", project.StackRef{}, err
	}
	if !m.IsApp() {
		return nil, "", project.StackRef{}, fmt.Errorf("%s is a platform, not an app", m.Name)
	}
	return m, dir, ref, nil
}

// appNeeds are what an app needs of the platform, each met or not.
func appNeeds(pm, am *manifest.Manifest) []tui.FormNeed {
	var out []tui.FormNeed
	for _, need := range am.Requires.Provides {
		out = append(out, tui.FormNeed{Text: pm.Name + " provides " + need, Met: manifest.Has(pm.Provides, need)})
	}
	_, _, ok := am.Target(pm.Provides)
	out = append(out, tui.FormNeed{Text: "a way to run on " + pm.Name, Met: ok})
	return out
}

// installApp installs an app from its newest release with what its plan
// brings: the questions and keys of each on a form, then a deploy of them
// alone.
func installApp(ctx context.Context, s *cli, p *project.Project, want project.StackRef) error {
	pm, pdir, err := ops.ProjectStack(ctx, p, "")
	if err != nil {
		return err
	}
	am, adir, ref, err := latestApp(ctx, want)
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
	var notes []string
	order, err := addPlan(ctx, p, pm, pdir, &planned{m: am, dir: adir, ref: ref}, planFlow{
		show: func(lines []string, _ bool, _ string) (bool, *bool, error) {
			notes = lines
			return true, nil, nil
		},
		prepare: func(a *planned, _ []string, connect string) (*setup.Pending, *bool, error) {
			return appForm(ctx, s, p, pm, a, password, notes, connect)
		},
		choose: func(_ manifest.Capability, _ string, candidates []plan.Module) (string, error) {
			return candidates[0].Name, nil
		},
		say: func(lines []string) { notes = lines },
	})
	if errors.Is(err, errFormLeft) {
		return nil
	}
	if err != nil {
		return err
	}
	names := make([]string, len(order))
	for i, a := range order {
		if err := a.pending.Add(); err != nil {
			return err
		}
		names[i] = a.m.Name
	}
	return deployParts(ctx, s, p, pm, pdir, names...)
}

// errFormLeft is a form the person left without installing.
var errFormLeft = errors.New("left the form")

// connectQuestion is the question of the form whether to connect an app to
// the providers it can use; its name is none a manifest can give.
const connectQuestion = "damstack-connect"

// appForm asks the questions and keys of an app of a plan on a form, notes
// beside them, and with connect, whether to connect it.
func appForm(ctx context.Context, s *cli, p *project.Project, pm *manifest.Manifest, a *planned, password string, notes []string, connect string) (*setup.Pending, *bool, error) {
	am, ref := a.m, a.ref
	side := append(slices.Clone(notes), "nothing changes until you confirm the review")
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
	questions := am.Questions
	if connect != "" {
		questions = append(slices.Clone(questions), manifest.Question{Name: connectQuestion, Type: "bool", Default: true,
			Prompt: connect, Section: "Connect"})
	}
	qs, secrets := formQuestions(questions), formSecrets(am.Secrets)
	form := tui.NewForm(fmt.Sprintf("Install %s %s on %s", am.Name, ref.Tag, p.Meta.Name), am.Name, appNeeds(pm, am), side,
		qs.form, secrets.form, tui.FormActions{
			Parse: func(q tui.FormQuestion, raw string) (any, error) { return ask.Parse(qs.of[q.Name], raw) },
			Holds: manifest.Holds,
			Default: func(q tui.FormQuestion, answers map[string]any) string {
				return defaultText(qs.of[q.Name], answers)
			},
			Check: func(q tui.FormQuestion, v any, answers map[string]any) error {
				return run(qs.of[q.Name].Checks, v, answers)
			},
			Secret: func(sec tui.FormSecret, v string, answers map[string]any) error {
				return run(secrets.of[sec.Name].Checks, v, answers)
			},
			Open: openURL,
		})
	if err := s.show(ctx, form); err != nil {
		return nil, nil, err
	}
	answers, keys, ok := form.Result()
	if !ok {
		return nil, nil, errFormLeft
	}
	var connected *bool
	if v, asked := answers[connectQuestion].(bool); asked {
		connected = &v
	}
	delete(answers, connectQuestion)
	given := answers
	for k, v := range keys {
		given[k] = v
	}
	pending, err := setup.PrepareApp(setup.AppOptions{Manifest: am, Stack: a.dir, Ref: ref, Project: p, Password: password, Given: given})
	return pending, connected, err
}

// formItems are a manifest's questions or secrets as the form shows them,
// and each by its name.
type formItems[M, F any] struct {
	form []F
	of   map[string]M
}

func formQuestions(qs []manifest.Question) formItems[manifest.Question, tui.FormQuestion] {
	out := formItems[manifest.Question, tui.FormQuestion]{of: map[string]manifest.Question{}}
	for _, q := range qs {
		out.form = append(out.form, tui.FormQuestion{Name: q.Name, Prompt: q.Prompt, Type: q.Type, Options: q.Options,
			Section: q.Section, Note: q.Note, Advanced: q.Advanced, When: q.When})
		out.of[q.Name] = q
	}
	return out
}

func formSecrets(secrets []manifest.Secret) formItems[manifest.Secret, tui.FormSecret] {
	out := formItems[manifest.Secret, tui.FormSecret]{of: map[string]manifest.Secret{}}
	for _, s := range secrets {
		out.form = append(out.form, tui.FormSecret{Name: s.Name, Ask: s.Ask, When: s.When, Optional: s.Optional, NewAt: s.NewAt, For: s.For})
		out.of[s.Name] = s
	}
	return out
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
func updateApp(ctx context.Context, s *cli, p *project.Project, name string) error {
	pm, pdir, err := ops.ProjectStack(ctx, p, "")
	if err != nil {
		return err
	}
	i := slices.IndexFunc(p.Meta.Apps, func(a project.StackRef) bool { return a.Name == name })
	if i < 0 {
		return fmt.Errorf("%s is no app of %s", name, p.Meta.Name)
	}
	_, _, ref, err := latestApp(ctx, p.Meta.Apps[i])
	if err != nil {
		return err
	}
	p.Meta.Apps[i].Tag, p.Meta.Apps[i].Commit = ref.Tag, ref.Commit
	if err := p.SaveMeta(); err != nil {
		return err
	}
	return deployParts(ctx, s, p, pm, pdir, name)
}

// deployParts deploys the named parts of a project, and the steps of the
// platform and the domains that come after the apps, such as the DNS records.
func deployParts(ctx context.Context, s *cli, p *project.Project, pm *manifest.Manifest, pdir string, parts ...string) error {
	only := map[string]bool{}
	for _, part := range parts {
		only[part] = true
	}
	return runSteps(ctx, s, p, pm, pdir, ops.Options{Only: only, AfterApps: true}, false)
}
