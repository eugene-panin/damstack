package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/eugene-panin/damstack/internal/backup"
	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/release"
	"github.com/eugene-panin/damstack/internal/ui/tui"
)

// homeScreen is damstack without a command on a terminal: the home screen,
// and whatever its keys choose, until the person quits.
func homeScreen(ctx context.Context, s *streams, cfg *config.Config, ready bool) error {
	tui.UseProfile(colorprofile.Detect(s.out, os.Environ()))
	note := ""
	for {
		projects := map[string]*project.Project{}
		var rows []tui.ProjectRow
		for _, entry := range cfg.Projects {
			p, err := project.Open(entry.Path)
			if err != nil {
				continue
			}
			projects[p.Meta.Name] = p
			rows = append(rows, projectRow(p))
		}
		model := tui.NewHome(release.Version, ready, rows, cfg.Current, time.Now(), func(name string) tui.ProjectState {
			return lookAt(ctx, projects[name])
		})
		model.SetNote(note)
		if _, err := tea.NewProgram(model, tea.WithInput(s.in), tea.WithOutput(s.out), tea.WithContext(ctx)).Run(); err != nil {
			return err
		}
		act := model.Action()
		if act.Key == "" {
			return nil
		}
		note = ""
		if err := homeAction(ctx, s, projects[act.Project], act.Key); err != nil {
			note = err.Error()
		}
		var err error
		if cfg, err = loadConfig(ctx); err != nil {
			return err
		}
	}
}

// homeAction does what a key of the home screen chose, for a project.
func homeAction(ctx context.Context, s *streams, p *project.Project, key string) error {
	switch key {
	case "d":
		m, dir, err := projectStack(ctx, p, "")
		if err != nil {
			return err
		}
		return runSteps(ctx, s, p, m, dir, "", false)
	case "k":
		return appKeys(ctx, s, p, "")
	case "!":
		return secretsScreen(ctx, s, p)
	case "u":
		return linksScreen(ctx, s, p)
	case "e", "s":
		name := map[string]string{"e": "edit", "s": "ssh"}[key]
		cmd := exec.CommandContext(ctx, self(), name, p.Meta.Name)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	return nil
}

// projectRow is what the home screen lists of a project without asking its
// server: its stack, server, apps and backups.
func projectRow(p *project.Project) tui.ProjectRow {
	r := tui.ProjectRow{Name: p.Meta.Name, Stack: strings.TrimSpace(p.Meta.Stack.Name + " " + p.Meta.Stack.Tag)}
	config, _ := p.Config()
	if server, ok := config["server"].(map[string]any); ok {
		r.Server, _ = server["address"].(string)
	}
	for _, a := range p.Meta.Apps {
		r.Apps = append(r.Apps, a.Name)
	}
	if b, ok := config["backup"].(map[string]any); ok {
		r.Backup, _ = b["schedule"].(string)
		st, _ := backup.LoadState(p.Dir)
		r.BackupOK = st.Snapshots > 0 && time.Since(st.Latest) < backup.Stale
	}
	return r
}

// lookAt says how a project is: what its history and records say, and how
// its apps run, as its Nomad says.
func lookAt(ctx context.Context, p *project.Project) tui.ProjectState {
	var st tui.ProjectState
	if p == nil {
		return st
	}
	if entries, err := p.History(); err == nil {
		restored := false
		var last *project.Entry
		for i := range entries {
			e := &entries[i]
			if e.Command == "restore" && e.Result == project.OK {
				restored = true
			}
			if e.Command == "deploy" {
				last = e
			}
		}
		switch {
		case last == nil:
			st.Notes = append(st.Notes, "deploy  not yet")
		case last.Result == project.Failed:
			st.Notes = append(st.Notes, "deploy  "+last.Step+" failed "+ago(last.Time))
			st.Needs = append(st.Needs, tui.Need{Text: "the last deploy failed at " + last.Step, Urgent: true, Key: "d"})
		default:
			st.Notes = append(st.Notes, "deploy  last "+ago(last.Time))
		}
		if !restored {
			st.Needs = append(st.Needs, tui.Need{Text: "backups never restored", Urgent: true})
		}
	}
	if r, err := p.Records(); err == nil {
		seen := 0
		for _, rec := range r.Secrets {
			if !rec.Seen.IsZero() {
				seen++
			}
		}
		if seen > 0 {
			st.Needs = append(st.Needs, tui.Need{Text: plural(seen, "secret") + " seen in plain text", Urgent: true, Key: "!"})
		}
	}
	if password, err := p.Password(); err == nil {
		if platformPending(p, password) || appKeysPending(p) {
			st.Needs = append(st.Needs, tui.Need{Text: "changes wait for a deploy", Key: "d"})
		}
		set, all := keysSet(p, password)
		if all > 0 {
			st.Notes = append(st.Notes, fmt.Sprintf("keys    %d of %d set", set, all))
		}
	}
	if b, err := backup.LoadState(p.Dir); err == nil {
		if m, _ := cachedStack(p); m != nil && m.Backup != nil && staleWarning(b) != "" {
			st.Needs = append(st.Needs, tui.Need{Text: "the backups here are old, or none"})
		}
	}
	st.State, st.Text, st.Apps = appStates(ctx, p)
	return st
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// keysSet is how many of the keys a project's apps ask for are set.
func keysSet(p *project.Project, password string) (set, all int) {
	secrets, err := p.Secrets(password)
	if err != nil {
		return 0, 0
	}
	for _, a := range askedSecrets(p) {
		if a.app == "" {
			continue
		}
		all++
		if v, _ := secrets[a.spec.Name].(string); v != "" {
			set++
		}
	}
	return set, all
}

// appKeysPending is whether a key of an app was set after the app's last
// deploy.
func appKeysPending(p *project.Project) bool {
	r, err := p.Records()
	if err != nil {
		return false
	}
	for _, a := range askedSecrets(p) {
		if a.app == "" {
			continue
		}
		deployed := lastStepDone(p, func(step string) bool { return strings.HasPrefix(step, a.app+"/") })
		if rec, ok := r.Secrets[a.spec.Name]; ok && rec.Set.After(deployed) {
			return true
		}
	}
	return false
}

// nomadJob is what Nomad's list of jobs says of one.
type nomadJob struct {
	ID, Type, Status string
	JobSummary       struct {
		Summary map[string]struct {
			Running, Starting, Queued, Failed, Complete, Lost int
		}
	}
}

// appStates asks the project's Nomad how its apps run: an app runs well when
// every group of its jobs has an allocation running, or, for a batch job,
// finished.
func appStates(ctx context.Context, p *project.Project) (tui.State, string, []tui.AppState) {
	var apps []tui.AppState
	for _, a := range p.Meta.Apps {
		apps = append(apps, tui.AppState{Name: a.Name})
	}
	m, dir, err := projectStack(ctx, p, "")
	if err != nil {
		return tui.Unknown, "stack not fetched", apps
	}
	password, err := p.Password()
	if err != nil {
		return tui.Unknown, "unknown", apps
	}
	env, err := appEnv(p, m, dir, password)
	if err != nil || env["NOMAD_ADDR"] == "" {
		return tui.Unknown, "unknown", apps
	}
	jobs, err := listJobs(ctx, env)
	if err != nil {
		return tui.Unknown, "no answer · tunnel off?", apps
	}
	bad := 0
	for i := range apps {
		ok, groups := 0, 0
		for _, j := range jobs {
			if j.ID != apps[i].Name && !strings.HasPrefix(j.ID, apps[i].Name+"-") {
				continue
			}
			for _, g := range j.JobSummary.Summary {
				if j.Type == "batch" && g.Running == 0 && g.Complete > 0 {
					continue
				}
				groups++
				if g.Running > 0 {
					ok++
				}
			}
		}
		switch {
		case groups == 0:
			apps[i].State, apps[i].Text = tui.Warning, "not running"
		case ok == groups:
			apps[i].State = tui.Healthy
		case ok == 0:
			apps[i].State = tui.Down
		default:
			apps[i].State = tui.Warning
		}
		if groups > 1 {
			apps[i].Text = fmt.Sprintf("%d/%d", ok, groups)
		}
		if apps[i].State != tui.Healthy {
			bad++
		}
	}
	if bad > 0 {
		return tui.Warning, plural(bad, "warning"), apps
	}
	return tui.Healthy, "all healthy", apps
}

func listJobs(ctx context.Context, env map[string]string) ([]nomadJob, error) {
	c := caClient(env["NOMAD_CACERT"])
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(env["NOMAD_ADDR"], "/")+"/v1/jobs", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Nomad-Token", env["NOMAD_TOKEN"])
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nomad: %s", resp.Status)
	}
	var jobs []nomadJob
	return jobs, json.NewDecoder(resp.Body).Decode(&jobs)
}

// linksScreen shows every address a project serves.
func linksScreen(ctx context.Context, s *streams, p *project.Project) error {
	m, _, err := projectStack(ctx, p, "")
	if err != nil {
		return err
	}
	config, _ := p.Config()
	infra, _ := config["infra_domain"].(string)
	var links []tui.Link
	for _, l := range runDone(p, m).Links {
		u, _ := url.Parse(l.URL)
		tunnel := infra != "" && u != nil && strings.HasSuffix(u.Hostname(), "."+infra)
		links = append(links, tui.Link{URL: l.URL, Admin: l.Admin, Tunnel: tunnel, Token: l.Token})
	}
	model := tui.NewLinks(p.Meta.Name, links, tui.LinkActions{
		Open: openURL,
		Copy: copyText,
		CopyToken: func(name string) error {
			value, err := projectToken(p, m, name)
			if err != nil {
				return err
			}
			return copyText(value)
		},
		Check: check,
		Tunnel: func() bool {
			if m.Server == nil || m.Server.Tunnel == "" {
				return true
			}
			address, err := manifest.Render("server.tunnel", m.Server.Tunnel, config, nil)
			if err != nil {
				return false
			}
			conn, err := net.DialTimeout("tcp", net.JoinHostPort(strings.TrimSpace(address), "22"), 2*time.Second)
			if err != nil {
				return false
			}
			conn.Close()
			return true
		},
	})
	_, err = tea.NewProgram(model, tea.WithInput(s.in), tea.WithOutput(s.out), tea.WithContext(ctx)).Run()
	return err
}
