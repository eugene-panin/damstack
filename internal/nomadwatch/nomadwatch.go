// Package nomadwatch waits for a Nomad job to come up after it was submitted:
// a service job until its deployment succeeds, a batch job until the
// allocations of its version finish. Accepted is not up: a job Nomad took can
// still fail to start, and the deploy should say so there, not later.
package nomadwatch

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"
)

// Client is the part of Nomad's HTTP API this package uses.
type Client struct {
	Addr  string
	Token string
	HTTP  *http.Client
	// Poll is how often Wait asks again; zero is two seconds.
	Poll time.Duration
}

// FromEnv is a Client for the Nomad that NOMAD_ADDR, NOMAD_CACERT and
// NOMAD_TOKEN name, nil when env has no NOMAD_ADDR.
func FromEnv(env map[string]string) (*Client, error) {
	addr := env["NOMAD_ADDR"]
	if addr == "" {
		return nil, nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if file := env["NOMAD_CACERT"]; file != "" {
		pem, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("the CA of Nomad: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("the CA of Nomad in %s holds no certificate", file)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &Client{Addr: strings.TrimSuffix(addr, "/"), Token: env["NOMAD_TOKEN"],
		HTTP: &http.Client{Transport: transport, Timeout: 30 * time.Second}}, nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Addr+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("X-Nomad-Token", c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("nomad %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// Progress is how far a job is.
type Progress struct {
	Job string
	// Healthy of Desired allocations are up (finished, for a batch job).
	Healthy, Desired int
	// Detail says what Nomad is doing, such as "deployment is running".
	Detail string
}

type job struct {
	ID        string
	Namespace string
	Type      string
	Version   uint64
}

type deployment struct {
	ID                string
	JobVersion        uint64
	Status            string
	StatusDescription string
	TaskGroups        map[string]struct {
		DesiredTotal    int
		HealthyAllocs   int
		UnhealthyAllocs int
	}
}

type alloc struct {
	ID               string
	TaskGroup        string
	JobVersion       uint64
	ClientStatus     string
	DeploymentStatus *struct{ Healthy *bool }
	TaskStates       map[string]struct {
		State  string
		Failed bool
		Events []struct{ DisplayMessage string }
	}
}

// Wait waits for the job id to come up, calling report as it goes. It returns
// an error saying why when the job does not come up, or when ctx ends.
func (c *Client) Wait(ctx context.Context, id string, report func(Progress)) error {
	j, err := c.find(ctx, id)
	if err != nil {
		return err
	}
	poll := c.Poll
	if poll == 0 {
		poll = 2 * time.Second
	}
	ns := url.Values{"namespace": {j.Namespace}}
	for {
		var done bool
		var err error
		if j.Type == "batch" || j.Type == "sysbatch" {
			done, err = c.batch(ctx, j, ns, report)
		} else {
			done, err = c.service(ctx, j, ns, report)
		}
		if err != nil || done {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for the job %s: %w", id, ctx.Err())
		case <-time.After(poll):
		}
	}
}

// find is the job by its ID, in whatever namespace it is.
func (c *Client) find(ctx context.Context, id string) (job, error) {
	var stubs []struct{ ID, Namespace string }
	if err := c.get(ctx, "/v1/jobs", url.Values{"prefix": {id}, "namespace": {"*"}}, &stubs); err != nil {
		return job{}, err
	}
	i := slices.IndexFunc(stubs, func(s struct{ ID, Namespace string }) bool { return s.ID == id })
	if i < 0 {
		return job{}, fmt.Errorf("nomad has no job %s", id)
	}
	var j job
	err := c.get(ctx, "/v1/job/"+url.PathEscape(id), url.Values{"namespace": {stubs[i].Namespace}}, &j)
	return j, err
}

func (c *Client) service(ctx context.Context, j job, ns url.Values, report func(Progress)) (bool, error) {
	var d *deployment
	if err := c.get(ctx, "/v1/job/"+url.PathEscape(j.ID)+"/deployment", ns, &d); err != nil {
		return false, err
	}
	if d == nil {
		// A system job, or one without updates, has no deployment: its
		// allocations of this version running is up.
		return c.running(ctx, j, ns, report)
	}
	if d.JobVersion < j.Version {
		report(Progress{Job: j.ID, Detail: "waiting for its deployment"})
		return false, nil
	}
	p := Progress{Job: j.ID, Detail: "deployment " + d.Status}
	for _, g := range d.TaskGroups {
		p.Desired += g.DesiredTotal
		p.Healthy += g.HealthyAllocs
	}
	report(p)
	switch d.Status {
	case "successful":
		return true, nil
	case "failed", "cancelled":
		return false, fmt.Errorf("the job %s did not come up: %s%s", j.ID, d.StatusDescription, c.why(ctx, j, ns))
	}
	return false, nil
}

func (c *Client) running(ctx context.Context, j job, ns url.Values, report func(Progress)) (bool, error) {
	allocs, err := c.allocs(ctx, j, ns)
	if err != nil {
		return false, err
	}
	p := Progress{Job: j.ID, Desired: len(allocs), Detail: "allocations starting"}
	for _, a := range allocs {
		switch a.ClientStatus {
		case "running":
			p.Healthy++
		case "failed", "lost":
			return false, fmt.Errorf("the job %s did not come up%s", j.ID, c.why(ctx, j, ns))
		}
	}
	report(p)
	return len(allocs) > 0 && p.Healthy == len(allocs), nil
}

func (c *Client) batch(ctx context.Context, j job, ns url.Values, report func(Progress)) (bool, error) {
	allocs, err := c.allocs(ctx, j, ns)
	if err != nil {
		return false, err
	}
	p := Progress{Job: j.ID, Desired: len(allocs), Detail: "running"}
	finished := 0
	for _, a := range allocs {
		switch a.ClientStatus {
		case "complete":
			p.Healthy++
			finished++
		case "failed", "lost":
			finished++
		}
	}
	if len(allocs) > 0 && finished == len(allocs) {
		p.Detail = "finished"
	}
	report(p)
	if len(allocs) == 0 || finished < len(allocs) {
		return false, nil
	}
	if p.Healthy < len(allocs) {
		return false, fmt.Errorf("the job %s failed%s", j.ID, c.why(ctx, j, ns))
	}
	return true, nil
}

// allocs are the job's allocations of its current version.
func (c *Client) allocs(ctx context.Context, j job, ns url.Values) ([]alloc, error) {
	var all []alloc
	if err := c.get(ctx, "/v1/job/"+url.PathEscape(j.ID)+"/allocations", ns, &all); err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(a alloc) bool { return a.JobVersion != j.Version }), nil
}

// why says, after a colon, what the allocations of the job that failed said
// last: the reason a person looks for first.
func (c *Client) why(ctx context.Context, j job, ns url.Values) string {
	allocs, err := c.allocs(ctx, j, ns)
	if err != nil {
		return ""
	}
	var reasons []string
	for _, a := range allocs {
		unhealthy := a.DeploymentStatus != nil && a.DeploymentStatus.Healthy != nil && !*a.DeploymentStatus.Healthy
		if a.ClientStatus != "failed" && a.ClientStatus != "lost" && !unhealthy {
			continue
		}
		for task, s := range a.TaskStates {
			if n := len(s.Events); n > 0 && (s.Failed || unhealthy) {
				reasons = append(reasons, fmt.Sprintf("%s/%s: %s", a.TaskGroup, task, s.Events[n-1].DisplayMessage))
			}
		}
	}
	if len(reasons) == 0 {
		return ""
	}
	slices.Sort(reasons)
	return ";\n  " + strings.Join(slices.Compact(reasons), "\n  ")
}

func (c *Client) post(ctx context.Context, path string, q url.Values, body, into any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Addr+path+"?"+q.Encode(), bytes.NewReader(data))
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("X-Nomad-Token", c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("nomad %s: %s: %s", path, resp.Status, bytes.TrimSpace(msg))
	}
	if into == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// InPlace is a task group that a new version of a job changes in place in a
// way its running allocations do not take: its sidecar's settings (Consul
// Connect) or its network. Nomad updates the allocations without restarting
// them, so the change takes effect only in new ones.
type InPlace struct {
	Job, Group string
	// Fields are the paths of what changes, such as
	// Service/ConsulConnect/SidecarService/ConsulProxy/TransparentProxy/ExcludeInboundPorts.
	Fields []string
}

// untaken are the parts of a group whose in-place changes running allocations
// do not take.
var untaken = []string{"ConsulConnect", "Network"}

type diffObject struct {
	Type    string
	Name    string
	Fields  []struct{ Type, Name string }
	Objects []diffObject
}

// Plan asks Nomad what submitting the job in jobspec (HCL) would do, changing
// nothing, and says which of its groups would be changed in place in a way
// that needs new allocations.
func (c *Client) Plan(ctx context.Context, jobspec string) ([]InPlace, error) {
	var job map[string]any
	if err := c.post(ctx, "/v1/jobs/parse", nil, map[string]any{"JobHCL": jobspec, "Canonicalize": true}, &job); err != nil {
		return nil, err
	}
	id, _ := job["ID"].(string)
	ns, _ := job["Namespace"].(string)
	var plan struct {
		Annotations struct {
			DesiredTGUpdates map[string]struct{ InPlaceUpdate int }
		}
		Diff struct {
			TaskGroups []diffObject
		}
	}
	if err := c.post(ctx, "/v1/job/"+url.PathEscape(id)+"/plan", url.Values{"namespace": {ns}},
		map[string]any{"Job": job, "Diff": true}, &plan); err != nil {
		return nil, err
	}
	var out []InPlace
	for _, g := range plan.Diff.TaskGroups {
		if plan.Annotations.DesiredTGUpdates[g.Name].InPlaceUpdate == 0 {
			continue
		}
		var fields []string
		var walk func(o diffObject, path []string)
		walk = func(o diffObject, path []string) {
			for _, f := range o.Fields {
				if f.Type == "None" {
					continue
				}
				p := strings.Join(path, "/")
				if len(path) == 0 || path[len(path)-1] != f.Name { // a list is an object holding a field of its name
					p = strings.Join(append(slices.Clone(path), f.Name), "/")
				}
				for _, u := range untaken {
					if slices.Contains(path, u) {
						fields = append(fields, p)
						break
					}
				}
			}
			for _, child := range o.Objects {
				if child.Type != "None" {
					walk(child, append(slices.Clone(path), child.Name))
				}
			}
		}
		for _, child := range g.Objects {
			if child.Type != "None" {
				walk(child, []string{child.Name})
			}
		}
		if len(fields) > 0 {
			out = append(out, InPlace{Job: id, Group: g.Name, Fields: fields})
		}
	}
	return out, nil
}

// Restart replaces the running allocations of a group of a job one at a
// time, each only once the one before it has a replacement running, so the
// group is never all down at once.
func (c *Client) Restart(ctx context.Context, id, group string, report func(Progress)) error {
	j, err := c.find(ctx, id)
	if err != nil {
		return err
	}
	ns := url.Values{"namespace": {j.Namespace}}
	var all []alloc
	if err := c.get(ctx, "/v1/job/"+url.PathEscape(j.ID)+"/allocations", ns, &all); err != nil {
		return err
	}
	var old []alloc
	seen := map[string]bool{}
	for _, a := range all {
		seen[a.ID] = true
		if a.JobVersion == j.Version && a.TaskGroup == group && a.ClientStatus == "running" {
			old = append(old, a)
		}
	}
	poll := c.Poll
	if poll == 0 {
		poll = 2 * time.Second
	}
	for i, a := range old {
		report(Progress{Job: id, Healthy: i, Desired: len(old), Detail: "restarting " + group})
		if err := c.post(ctx, "/v1/allocation/"+url.PathEscape(a.ID)+"/stop", ns, struct{}{}, nil); err != nil {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return fmt.Errorf("restarting %s/%s: %w", id, group, ctx.Err())
			case <-time.After(poll):
			}
			var now []alloc
			if err := c.get(ctx, "/v1/job/"+url.PathEscape(j.ID)+"/allocations", ns, &now); err != nil {
				return err
			}
			up := false
			for _, n := range now {
				if seen[n.ID] || n.TaskGroup != group || n.JobVersion != j.Version {
					continue
				}
				switch n.ClientStatus {
				case "running":
					up = up || allRunning(n)
				case "failed", "lost":
					return fmt.Errorf("restarting %s/%s: the new allocation failed%s", id, group, c.why(ctx, j, ns))
				}
			}
			if up {
				for _, n := range now {
					seen[n.ID] = true
				}
				break
			}
		}
	}
	report(Progress{Job: id, Healthy: len(old), Desired: len(old), Detail: group + " restarted"})
	return nil
}

func allRunning(a alloc) bool {
	if len(a.TaskStates) == 0 {
		return false
	}
	for _, s := range a.TaskStates {
		if s.State != "running" && s.State != "dead" { // a prestart task is dead once done
			return false
		}
	}
	return true
}
