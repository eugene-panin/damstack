package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eugene-panin/damstack/internal/ops"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/stack"
	"github.com/eugene-panin/damstack/internal/ui/tui"
)

// nomad is the little of a project's Nomad API the screens need.
type nomad struct {
	addr, token string
	http        *http.Client
}

// projectNomad is the Nomad of a project's platform, as its app_env reaches
// it.
func projectNomad(ctx context.Context, p *project.Project) (*nomad, error) {
	m, dir, err := ops.ProjectStack(ctx, p, "")
	if err != nil {
		return nil, err
	}
	password, err := p.Password()
	if err != nil {
		return nil, err
	}
	env, err := ops.AppEnv(p, m, dir, password)
	if err != nil {
		return nil, err
	}
	if env["NOMAD_ADDR"] == "" {
		return nil, fmt.Errorf("the platform of %s gives no Nomad address", p.Meta.Name)
	}
	return &nomad{addr: strings.TrimRight(env["NOMAD_ADDR"], "/"), token: env["NOMAD_TOKEN"], http: ops.CAClient(env["NOMAD_CACERT"])}, nil
}

func (n *nomad) do(ctx context.Context, method, path string, body any, out any) error {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, n.addr+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("X-Nomad-Token", n.token)
	resp, err := n.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("nomad: %s %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	switch out := out.(type) {
	case nil:
		return nil
	case *[]byte:
		*out, err = io.ReadAll(resp.Body)
		return err
	default:
		return json.NewDecoder(resp.Body).Decode(out)
	}
}

type nomadJobSpec struct {
	ID, Type   string
	TaskGroups []struct {
		Name  string
		Count int
		Tasks []struct {
			Name      string
			Resources struct{ MemoryMB, MemoryMaxMB int }
		}
	}
}

type nomadAlloc struct {
	ID, TaskGroup, ClientStatus string
	DeploymentStatus            *struct{ Healthy *bool }
	TaskStates                  map[string]struct{ Restarts int }
}

// appJobs are the jobs of an app: those named after it.
func appJobs(jobs []nomadJob, app string) []nomadJob {
	var out []nomadJob
	for _, j := range jobs {
		if j.ID == app || strings.HasPrefix(j.ID, app+"-") {
			out = append(out, j)
		}
	}
	return out
}

// groups are how an app's task groups run.
func (n *nomad) groups(ctx context.Context, app string) ([]tui.AppGroup, error) {
	var jobs []nomadJob
	if err := n.do(ctx, http.MethodGet, "/v1/jobs", nil, &jobs); err != nil {
		return nil, err
	}
	var out []tui.AppGroup
	for _, j := range appJobs(jobs, app) {
		var spec nomadJobSpec
		if err := n.do(ctx, http.MethodGet, "/v1/job/"+url.PathEscape(j.ID), nil, &spec); err != nil {
			return nil, err
		}
		var allocs []nomadAlloc
		if err := n.do(ctx, http.MethodGet, "/v1/job/"+url.PathEscape(j.ID)+"/allocations", nil, &allocs); err != nil {
			return nil, err
		}
		for _, tg := range spec.TaskGroups {
			g := tui.AppGroup{Job: j.ID, Name: tg.Name, Count: tg.Count}
			for _, t := range tg.Tasks {
				if g.Task == "" {
					g.Task = t.Name
				}
				g.Memory += max(t.Resources.MemoryMaxMB, t.Resources.MemoryMB)
			}
			for _, a := range allocs {
				if a.TaskGroup != tg.Name || a.ClientStatus != "running" {
					continue
				}
				g.Running++
				g.Allocs = append(g.Allocs, a.ID)
				if a.DeploymentStatus == nil || a.DeploymentStatus.Healthy == nil || *a.DeploymentStatus.Healthy {
					g.Healthy++
				}
				for _, ts := range a.TaskStates {
					g.Restarts += ts.Restarts
				}
			}
			g.Batch = (spec.Type == "batch" || spec.Type == "sysbatch") && g.Running == 0
			out = append(out, g)
		}
	}
	n.memory(ctx, out)
	return out, nil
}

// memory fills in what the running allocations of each group use, asked of
// them all at once.
func (n *nomad) memory(ctx context.Context, groups []tui.AppGroup) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := range groups {
		for _, id := range groups[i].Allocs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var st struct {
					ResourceUsage struct{ MemoryStats struct{ RSS, Usage uint64 } }
				}
				if n.do(ctx, http.MethodGet, "/v1/client/allocation/"+id+"/stats", nil, &st) != nil {
					return
				}
				used := st.ResourceUsage.MemoryStats.RSS
				if used == 0 {
					used = st.ResourceUsage.MemoryStats.Usage
				}
				mu.Lock()
				groups[i].MemoryUsed += int(used / (1 << 20))
				mu.Unlock()
			}()
		}
	}
	wg.Wait()
}

// logs are the last lines a group's task wrote.
func (n *nomad) logs(ctx context.Context, g tui.AppGroup, stderr bool) []string {
	if len(g.Allocs) == 0 {
		return []string{"no allocation of " + g.Name + " runs"}
	}
	stream := "stdout"
	if stderr {
		stream = "stderr"
	}
	q := url.Values{"task": {g.Task}, "type": {stream}, "plain": {"true"}, "origin": {"end"}, "offset": {"16000"}}
	var data []byte
	if err := n.do(ctx, http.MethodGet, "/v1/client/fs/logs/"+g.Allocs[0]+"?"+q.Encode(), nil, &data); err != nil {
		return []string{err.Error()}
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > 1 {
		lines = lines[1:] // the first is cut where the offset fell
	}
	if len(lines) == 1 && lines[0] == "" {
		return []string{"nothing on " + stream + " yet"}
	}
	return lines
}

// restart restarts a group's running allocations, one at a time.
func (n *nomad) restart(ctx context.Context, g tui.AppGroup) error {
	for i, id := range g.Allocs {
		if i > 0 {
			time.Sleep(5 * time.Second)
		}
		if err := n.do(ctx, http.MethodPost, "/v1/client/allocation/"+id+"/restart", map[string]any{}, nil); err != nil {
			return err
		}
	}
	return nil
}

// appScreen shows an app of a project, and does what its keys choose.
func appScreen(ctx context.Context, s *streams, p *project.Project, name string) error {
	ref, ok := p.Meta.App(name)
	if !ok {
		return fmt.Errorf("%s is no app of %s", name, p.Meta.Name)
	}
	am, _, err := ops.RefStack(ctx, ref)
	if err != nil {
		return err
	}
	n, nerr := projectNomad(ctx, p)
	var links []tui.Link
	for _, l := range ops.DoneLines(p, am, name) {
		for _, raw := range ops.URLRe.FindAllString(l, -1) {
			u := strings.TrimRight(raw, ".")
			if !slices.ContainsFunc(links, func(x tui.Link) bool { return x.URL == u }) {
				links = append(links, tui.Link{URL: u})
			}
		}
	}
	var latest string
	var once sync.Once
	model := tui.NewApp(p.Meta.Name, name, tui.AppActions{
		Look: func() tui.AppView {
			once.Do(func() {
				if r, err := stack.Latest(ctx, ref.URL); err == nil {
					latest = r.Tag
				}
			})
			v := tui.AppView{Source: source(ref.URL), Version: ref.Tag, Latest: latest, Links: links}
			if nerr != nil {
				v.Err = nerr.Error()
				return v
			}
			if v.Groups, err = n.groups(ctx, name); err != nil {
				v.Err = "no answer from Nomad · tunnel off?"
			}
			return v
		},
		Logs: func(g tui.AppGroup, stderr bool) []string {
			if nerr != nil {
				return []string{nerr.Error()}
			}
			return n.logs(ctx, g, stderr)
		},
		Restart: func(g tui.AppGroup) error {
			if nerr != nil {
				return nerr
			}
			return n.restart(ctx, g)
		},
		Check: check,
		Open:  openURL,
	})
	if err := s.show(ctx, model); err != nil {
		return err
	}
	switch model.Choice() {
	case "u":
		return updateApp(ctx, s, p, name)
	case "e":
		deploy, err := settingsScreen(ctx, s, p, "apps", name)
		if err != nil || !deploy {
			return err
		}
		pm, pdir, err := ops.ProjectStack(ctx, p, "")
		if err != nil {
			return err
		}
		return deployParts(ctx, s, p, pm, pdir, name)
	case "k":
		return appKeys(ctx, s, p, name)
	}
	return nil
}
