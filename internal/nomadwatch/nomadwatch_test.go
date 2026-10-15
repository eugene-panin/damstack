package nomadwatch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeNomad answers the job, its deployment and its allocations; each call to
// the deployment or allocations moves to the next of the answers given.
type fakeNomad struct {
	mu          sync.Mutex
	job         map[string]any
	deployments []any
	allocs      [][]map[string]any
}

func (f *fakeNomad) next(list *[]any) any {
	v := (*list)[0]
	if len(*list) > 1 {
		*list = (*list)[1:]
	}
	return v
}

func (f *fakeNomad) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("X-Nomad-Token") != "tok" {
			t.Errorf("no token on %s", r.URL)
		}
		var v any
		switch {
		case r.URL.Path == "/v1/jobs":
			v = []map[string]any{{"ID": f.job["ID"].(string) + "-other", "Namespace": "default"}, {"ID": f.job["ID"], "Namespace": "default"}}
		case strings.HasSuffix(r.URL.Path, "/deployment"):
			v = f.next(&f.deployments)
		case strings.HasSuffix(r.URL.Path, "/allocations"):
			v = f.allocs[0]
			if len(f.allocs) > 1 {
				f.allocs = f.allocs[1:]
			}
		default:
			v = f.job
		}
		json.NewEncoder(w).Encode(v)
	})
}

func client(t *testing.T, f *fakeNomad) *Client {
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	return &Client{Addr: srv.URL, Token: "tok", HTTP: srv.Client(), Poll: time.Millisecond}
}

func deploy(version int, status string, healthy int) map[string]any {
	return map[string]any{"JobVersion": version, "Status": status, "StatusDescription": "Deployment " + status,
		"TaskGroups": map[string]any{"web": map[string]any{"DesiredTotal": 2, "HealthyAllocs": healthy}}}
}

func TestAServiceJobIsUpWhenItsDeploymentSucceeds(t *testing.T) {
	f := &fakeNomad{
		job:         map[string]any{"ID": "web", "Namespace": "default", "Type": "service", "Version": 4},
		deployments: []any{deploy(3, "successful", 2), deploy(4, "running", 1), deploy(4, "successful", 2)},
	}
	var seen []Progress
	if err := client(t, f).Wait(context.Background(), "web", func(p Progress) { seen = append(seen, p) }); err != nil {
		t.Fatal(err)
	}
	last := seen[len(seen)-1]
	if seen[0].Detail != "waiting for its deployment" || last.Healthy != 2 || last.Desired != 2 {
		t.Errorf("got %+v", seen)
	}
}

func TestAFailedDeploymentSaysWhy(t *testing.T) {
	f := &fakeNomad{
		job:         map[string]any{"ID": "web", "Namespace": "default", "Type": "service", "Version": 4},
		deployments: []any{deploy(4, "failed", 1)},
		allocs: [][]map[string]any{{
			{"ID": "a1", "TaskGroup": "web", "JobVersion": 4, "ClientStatus": "running", "DeploymentStatus": map[string]any{"Healthy": false},
				"TaskStates": map[string]any{"app": map[string]any{"Failed": false, "Events": []map[string]any{{"DisplayMessage": "Task started"}, {"DisplayMessage": "Exit Code: 1"}}}}},
			{"ID": "a0", "TaskGroup": "web", "JobVersion": 3, "ClientStatus": "failed",
				"TaskStates": map[string]any{"app": map[string]any{"Failed": true, "Events": []map[string]any{{"DisplayMessage": "an old version's failure"}}}}},
		}},
	}
	err := client(t, f).Wait(context.Background(), "web", func(Progress) {})
	if err == nil || !strings.Contains(err.Error(), "Deployment failed") || !strings.Contains(err.Error(), "web/app: Exit Code: 1") ||
		strings.Contains(err.Error(), "old version") {
		t.Fatalf("got %v", err)
	}
}

func TestABatchJobIsDoneWhenItsAllocationsComplete(t *testing.T) {
	f := &fakeNomad{
		job: map[string]any{"ID": "db", "Namespace": "default", "Type": "batch", "Version": 2},
		allocs: [][]map[string]any{
			{},
			{{"JobVersion": 2, "ClientStatus": "running"}},
			{{"JobVersion": 2, "ClientStatus": "complete"}, {"JobVersion": 1, "ClientStatus": "failed"}},
		},
	}
	if err := client(t, f).Wait(context.Background(), "db", func(Progress) {}); err != nil {
		t.Fatal(err)
	}

	f.allocs = [][]map[string]any{{{"TaskGroup": "db", "JobVersion": 2, "ClientStatus": "failed",
		"TaskStates": map[string]any{"run": map[string]any{"Failed": true, "Events": []map[string]any{{"DisplayMessage": "no role sauron_ui"}}}}}}}
	if err := client(t, f).Wait(context.Background(), "db", func(Progress) {}); err == nil || !strings.Contains(err.Error(), "db/run: no role sauron_ui") {
		t.Fatalf("got %v", err)
	}
}

func TestWaitingEndsWithItsContext(t *testing.T) {
	f := &fakeNomad{
		job:         map[string]any{"ID": "web", "Namespace": "default", "Type": "service", "Version": 4},
		deployments: []any{deploy(4, "running", 0)},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := client(t, f).Wait(ctx, "web", func(Progress) {}); err == nil {
		t.Fatal("a wait must end with its context")
	}
}

// TestAgainstARealNomad waits for jobs that are up on a real Nomad: a service
// job and a batch job, named in DAMSTACK_TEST_NOMAD_JOBS, with the Nomad of
// DAMSTACK_TEST_NOMAD_ADDR (and _CACERT, _TOKEN).
func TestAgainstARealNomad(t *testing.T) {
	addr := os.Getenv("DAMSTACK_TEST_NOMAD_ADDR")
	if addr == "" {
		t.Skip("set DAMSTACK_TEST_NOMAD_ADDR to run against a real Nomad")
	}
	c, err := FromEnv(map[string]string{"NOMAD_ADDR": addr, "NOMAD_CACERT": os.Getenv("DAMSTACK_TEST_NOMAD_CACERT"),
		"NOMAD_TOKEN": os.Getenv("DAMSTACK_TEST_NOMAD_TOKEN")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, id := range strings.Fields(os.Getenv("DAMSTACK_TEST_NOMAD_JOBS")) {
		var last Progress
		if err := c.Wait(ctx, id, func(p Progress) { last = p }); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		t.Logf("%s: %d/%d, %s", id, last.Healthy, last.Desired, last.Detail)
	}
}

// TestPlanAgainstARealNomad asks a real Nomad for the plan of a job whose
// sidecar changes: DAMSTACK_TEST_NOMAD_PLAN names a file with its HCL.
func TestPlanAgainstARealNomad(t *testing.T) {
	addr, file := os.Getenv("DAMSTACK_TEST_NOMAD_ADDR"), os.Getenv("DAMSTACK_TEST_NOMAD_PLAN")
	if addr == "" || file == "" {
		t.Skip("set DAMSTACK_TEST_NOMAD_ADDR and DAMSTACK_TEST_NOMAD_PLAN to run against a real Nomad")
	}
	c, err := FromEnv(map[string]string{"NOMAD_ADDR": addr, "NOMAD_CACERT": os.Getenv("DAMSTACK_TEST_NOMAD_CACERT"),
		"NOMAD_TOKEN": os.Getenv("DAMSTACK_TEST_NOMAD_TOKEN")})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Plan(context.Background(), string(hcl))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", got)
	if len(got) == 0 {
		t.Error("a changed sidecar must need new allocations")
	}
}

func TestARestartReplacesOneAllocationAtATime(t *testing.T) {
	var mu sync.Mutex
	stopped := []string{}
	allocs := []map[string]any{
		{"ID": "a1", "TaskGroup": "fetcher", "JobVersion": 3, "ClientStatus": "running"},
		{"ID": "a2", "TaskGroup": "fetcher", "JobVersion": 3, "ClientStatus": "running"},
		{"ID": "b1", "TaskGroup": "console", "JobVersion": 3, "ClientStatus": "running"},
		{"ID": "v2", "TaskGroup": "fetcher", "JobVersion": 2, "ClientStatus": "failed"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/v1/jobs":
			w.Write([]byte(`[{"ID":"sauron","Namespace":"default"}]`))
		case strings.HasSuffix(r.URL.Path, "/stop"):
			id := strings.Split(r.URL.Path, "/")[3]
			stopped = append(stopped, id)
			for _, a := range allocs {
				if a["ID"] == id {
					a["ClientStatus"] = "complete"
				}
			}
			// Nomad places a replacement, which comes up.
			allocs = append(allocs, map[string]any{"ID": id + "-new", "TaskGroup": "fetcher", "JobVersion": 3, "ClientStatus": "running",
				"TaskStates": map[string]any{"fetcher": map[string]any{"State": "running"}, "seeds": map[string]any{"State": "dead"}}})
			w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/allocations"):
			json.NewEncoder(w).Encode(allocs)
		default:
			w.Write([]byte(`{"ID":"sauron","Namespace":"default","Type":"service","Version":3}`))
		}
	}))
	defer srv.Close()
	c := &Client{Addr: srv.URL, HTTP: srv.Client(), Poll: time.Millisecond}
	if err := c.Restart(context.Background(), "sauron", "fetcher", func(Progress) {}); err != nil {
		t.Fatal(err)
	}
	if len(stopped) != 2 || stopped[0] != "a1" || stopped[1] != "a2" {
		t.Errorf("both fetcher allocations, one after the other, and nothing else: %v", stopped)
	}
}
