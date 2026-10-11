package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/eugene-panin/damstack/internal/event"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/toolbox"
)

// fakeRunner keeps the commands it is given; it shows the directories of the
// stack and the project as /stack and /work.
type fakeRunner struct {
	cmds    []toolbox.Cmd
	reply   func(c toolbox.Cmd) error
	stack   string
	project string
}

func (f *fakeRunner) short(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, f.project, "/work"), f.stack, "/stack")
}

func (f *fakeRunner) env(i int) map[string]string {
	env := map[string]string{}
	for k, v := range f.cmds[i].Env {
		env[k] = f.short(v)
	}
	return env
}

func (f *fakeRunner) Run(_ context.Context, c toolbox.Cmd) error {
	f.cmds = append(f.cmds, c)
	if f.reply != nil {
		return f.reply(c)
	}
	return nil
}

func (f *fakeRunner) lines() []string {
	var out []string
	for _, c := range f.cmds {
		out = append(out, f.short(strings.Join(c.Args, " ")))
	}
	return out
}

const password = "pw"

func setup(t *testing.T) (*Engine, *fakeRunner) {
	t.Helper()
	stack := t.TempDir()
	for _, f := range []string{"ansible/ansible.cfg", "ansible/playbooks/site.yml", "policy/data/rules.yaml"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(stack, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stack, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := project.Create(t.TempDir(), project.Meta{Name: "demo"}, map[string][]byte{
		project.ConfigFile: []byte("network: {cidr: 10.77.0.0/24}\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.SaveSecrets(password, map[string]any{"token": "t0k"}); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{stack: stack, project: p.Dir}
	return &Engine{Stack: stack, Project: p, Runner: r, Password: password, PasswordFile: "/c/vault-pass", KeyFile: "/h/.ssh/id_ed25519", Events: &recorder{},
		Approve: func(event.Plan) (event.Approval, error) { return event.Approval{Apply: true}, nil }}, r
}

func TestAnsible(t *testing.T) {
	e, r := setup(t)
	step := manifest.Step{
		Ansible: &manifest.Ansible{Playbook: "ansible/playbooks/site.yml", Inventory: "ansible/inventory", Requirements: "ansible/requirements.yml"},
		Env:     map[string]string{"NOMAD_ADDR": "https://{{ firstHost .config.network.cidr }}:4646", "NOMAD_CACERT": "{{ .dir.project }}/ca.pem"},
	}
	if err := e.Run(t.Context(), step, []string{"--check"}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ansible-galaxy collection install -r /stack/ansible/requirements.yml -p /work/.damstack/work/collections",
		"ansible-playbook -i /stack/ansible/inventory -e @/work/vault.yml --private-key /h/.ssh/id_ed25519 /stack/ansible/playbooks/site.yml --check --list-tasks",
		"ansible-playbook -i /stack/ansible/inventory -e @/work/vault.yml --private-key /h/.ssh/id_ed25519 /stack/ansible/playbooks/site.yml --check",
	}
	if got := r.lines(); !slices.Equal(got, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	env := r.env(2)
	if env["ANSIBLE_CONFIG"] != "/stack/ansible/ansible.cfg" || env["NOMAD_ADDR"] != "https://10.77.0.1:4646" || env["NOMAD_CACERT"] != "/work/ca.pem" ||
		!strings.Contains(env["ANSIBLE_SSH_ARGS"], "UserKnownHostsFile=/work/.damstack/known_hosts") || !r.cmds[0].Quiet {
		t.Errorf("env %v", env)
	}
}

func TestRunWithSecretAndKeep(t *testing.T) {
	e, r := setup(t)
	r.reply = func(toolbox.Cmd) error {
		return os.WriteFile(filepath.Join(e.Project.Dir, ".damstack/work/init.json"), []byte(`{"root_token":"r00t","keys":["k1"]}`), 0o600)
	}
	step := manifest.Step{
		Run:  []string{"bin/init", "--fast"},
		Env:  map[string]string{"TOKEN": `{{ secret "token" }}`},
		Keep: &manifest.Keep{File: ".damstack/work/init.json", Secrets: map[string]string{"vault_root_token": "root_token", "unseal": "keys"}},
	}
	if err := e.Run(t.Context(), step, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if got := r.lines(); !slices.Equal(got, []string{"/stack/bin/init --fast x"}) || r.cmds[0].Env["TOKEN"] != "t0k" {
		t.Errorf("got %v, env %v", got, r.cmds[0].Env)
	}
	secrets, err := e.Project.Secrets(password)
	if err != nil || secrets["vault_root_token"] != "r00t" || secrets["token"] != "t0k" || len(secrets["unseal"].([]any)) != 1 {
		t.Errorf("secrets %v, %v", secrets, err)
	}
	if _, err := os.Stat(filepath.Join(e.Project.Dir, ".damstack/work/init.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the kept file is still there: %v", err)
	}
}

const planJSON = `{"resource_changes":[
 {"address":"module.app.nomad_job.web","type":"nomad_job","change":{"after":{"jobspec":"job \"web\" {}"}}},
 {"address":"cloudflare_dns_record.a","type":"cloudflare_dns_record","change":{"after":{"name":"a"}}}]}`

func tofuStep(action string) manifest.Step {
	return manifest.Step{Tofu: &manifest.Tofu{Dir: "infra", Action: action, Policy: &manifest.Policy{Dir: "policy", NomadJobs: true}}, Confirm: true}
}

func planReply(planExit int) func(c toolbox.Cmd) error {
	return func(c toolbox.Cmd) error {
		switch {
		case slices.Contains(c.Args, "plan"):
			if planExit != 0 {
				return &toolbox.ExitError{Name: "tofu", Code: planExit}
			}
		case slices.Contains(c.Args, "show"):
			c.Stdout.Write([]byte(planJSON))
		}
		return nil
	}
}

func TestTofuApply(t *testing.T) {
	e, r := setup(t)
	r.reply = planReply(2)
	var asked string
	e.Approve = func(p event.Plan) (event.Approval, error) {
		asked = p.Question
		return event.Approval{Apply: true}, nil
	}
	if err := e.Run(t.Context(), tofuStep("apply"), nil); err != nil {
		t.Fatal(err)
	}
	data := " --no-color --policy /stack/policy --data /work/stack.yaml --data /stack/policy/data"
	want := []string{
		"tofu -chdir=/stack/infra init -input=false -reconfigure -lockfile=readonly -backend-config=path=/work/state/infra.tfstate",
		"tofu -chdir=/stack/infra plan -input=false -detailed-exitcode -out=/work/.damstack/work/tofu/infra/plan",
		"tofu -chdir=/stack/infra show -json /work/.damstack/work/tofu/infra/plan",
		"conftest test" + data + " --namespace terraform /work/.damstack/work/tofu/infra/plan.json",
		"conftest test" + data + " --parser hcl2 --namespace nomad /work/.damstack/work/jobs/module.app.nomad_job.web.nomad.hcl",
		"tofu -chdir=/stack/infra apply -input=false -json /work/.damstack/work/tofu/infra/plan",
	}
	if got := r.lines(); !slices.Equal(got, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	job, err := os.ReadFile(filepath.Join(e.Project.Dir, ".damstack/work/jobs/module.app.nomad_job.web.nomad.hcl"))
	if err != nil || string(job) != `job "web" {}` || asked == "" {
		t.Errorf("job %q, %v; asked %q", job, err, asked)
	}
	if env := r.env(0); env["TF_DATA_DIR"] != "/work/.damstack/work/tofu/infra" || env["TF_VAR_project"] != "/work" {
		t.Errorf("env %v", env)
	}
}

func TestTofuApplyDeclined(t *testing.T) {
	e, r := setup(t)
	r.reply = planReply(2)
	e.Approve = func(event.Plan) (event.Approval, error) { return event.Approval{}, nil }
	if err := e.Run(t.Context(), tofuStep("apply"), nil); !errors.Is(err, ErrDeclined) {
		t.Errorf("got %v", err)
	}
	for _, line := range r.lines() {
		if strings.Contains(line, " apply ") {
			t.Errorf("applied after a no: %s", line)
		}
	}
}

func TestPolicyDenies(t *testing.T) {
	e, r := setup(t)
	plan := planReply(2)
	r.reply = func(c toolbox.Cmd) error {
		if slices.Contains(c.Args, "conftest") {
			return &toolbox.ExitError{Name: "conftest", Code: 1}
		}
		return plan(c)
	}
	if err := e.Run(t.Context(), tofuStep("apply"), nil); !errors.Is(err, ErrPolicy) {
		t.Errorf("got %v", err)
	}
	for _, line := range r.lines() {
		if strings.Contains(line, " apply ") {
			t.Errorf("applied a plan the policy denied: %s", line)
		}
	}
	if r.cmds[0].Env["TF_CLI_ARGS"] != "-no-color" {
		t.Errorf("colors without a terminal: %v", r.cmds[0].Env)
	}
}

func TestTofuNothingToChange(t *testing.T) {
	e, r := setup(t)
	r.reply = planReply(0)
	if err := e.Run(t.Context(), tofuStep("apply"), nil); err != nil {
		t.Fatal(err)
	}
	if n := len(r.cmds); n != 2 {
		t.Errorf("ran %d commands, want init and plan:\n%s", n, strings.Join(r.lines(), "\n"))
	}
}

func TestTofuPlanFails(t *testing.T) {
	e, r := setup(t)
	r.reply = planReply(1)
	var exit *toolbox.ExitError
	if err := e.Run(t.Context(), tofuStep("plan"), nil); !errors.As(err, &exit) || exit.Code != 1 {
		t.Errorf("got %v", err)
	}
}

func TestAppTofuKeepsItsOwnStateAndWritesOutputs(t *testing.T) {
	e, r := setup(t)
	if err := e.Project.SetApp("mail", []byte("hostname: mail.example.org\n")); err != nil {
		t.Fatal(err)
	}
	e.App = "mail"
	plan := planReply(2)
	r.reply = func(c toolbox.Cmd) error {
		if slices.Contains(c.Args, "output") {
			c.Stdout.Write([]byte(`{"mail.example.org":[{"type":"A"}]}`))
			return nil
		}
		return plan(c)
	}
	step := manifest.Step{
		Tofu: &manifest.Tofu{Dir: "infra", Action: "apply", Outputs: map[string]string{"dns_records": "dns/mail.json"}},
		Env:  map[string]string{"TF_VAR_hostname": "{{ .app.hostname }}"},
	}
	if err := e.Run(t.Context(), step, nil); err != nil {
		t.Fatal(err)
	}
	lines := r.lines()
	if lines[0] != "tofu -chdir=/stack/infra init -input=false -reconfigure -lockfile=readonly -backend-config=path=/work/state/mail-infra.tfstate" ||
		lines[len(lines)-1] != "tofu -chdir=/stack/infra output -json dns_records" {
		t.Errorf("got\n%s", strings.Join(lines, "\n"))
	}
	if env := r.env(0); env["TF_DATA_DIR"] != "/work/.damstack/work/tofu/mail-infra" || env["TF_VAR_hostname"] != "mail.example.org" {
		t.Errorf("env %v", env)
	}
	if got, err := os.ReadFile(filepath.Join(e.Project.Dir, "dns/mail.json")); err != nil || !strings.Contains(string(got), "mail.example.org") {
		t.Errorf("dns/mail.json %q, %v", got, err)
	}
}

func TestInitIsTriedTwice(t *testing.T) {
	e, r := setup(t)
	inits := 0
	r.reply = func(c toolbox.Cmd) error {
		if slices.Contains(c.Args, "init") {
			inits++
			if inits == 1 {
				return &toolbox.ExitError{Name: "tofu", Code: 1}
			}
		}
		return nil
	}
	if err := e.Run(t.Context(), manifest.Step{Tofu: &manifest.Tofu{Dir: "infra", Action: "output"}}, nil); err != nil {
		t.Fatal(err)
	}
	if inits != 2 || !r.cmds[0].Silent || r.cmds[1].Silent {
		t.Errorf("%d inits, silent %v then %v", inits, r.cmds[0].Silent, r.cmds[1].Silent)
	}
	r.reply = func(c toolbox.Cmd) error {
		if slices.Contains(c.Args, "init") {
			return &toolbox.ExitError{Name: "tofu", Code: 1}
		}
		return nil
	}
	if err := e.Run(t.Context(), manifest.Step{Tofu: &manifest.Tofu{Dir: "infra", Action: "output"}}, nil); err == nil {
		t.Error("an init that fails twice went on")
	}
}

func TestBriefConfirmsByCounts(t *testing.T) {
	e, r := setup(t)
	e.Brief = true
	r.reply = func(c toolbox.Cmd) error {
		switch {
		case slices.Contains(c.Args, "plan"):
			return &toolbox.ExitError{Name: "tofu", Code: 2}
		case slices.Contains(c.Args, "show"):
			c.Stdout.Write([]byte(`{"resource_changes":[
				{"change":{"actions":["create"]}},{"change":{"actions":["create"]}},
				{"change":{"actions":["delete","create"]}},{"change":{"actions":["no-op"]}},
				{"change":{"actions":["delete"]}}]}`))
		}
		return nil
	}
	var asked string
	e.Approve = func(p event.Plan) (event.Approval, error) { asked = p.Question; return event.Approval{}, nil }
	step := manifest.Step{Tofu: &manifest.Tofu{Dir: "infra", Action: "apply"}, Confirm: true}
	if err := e.Run(t.Context(), step, nil); !errors.Is(err, ErrDeclined) {
		t.Fatal(err)
	}
	if asked != "2 to create, 1 to replace, 1 to destroy. Go?" {
		t.Errorf("asked %q", asked)
	}
}

// fakeStore keeps the calls of a StateStore.
type fakeStore struct {
	calls   []string
	pushErr error
}

func (s *fakeStore) Pull(unit, path string) error {
	s.calls = append(s.calls, "pull "+unit+" "+filepath.Base(path))
	return nil
}

func (s *fakeStore) Push(unit, path string) error {
	s.calls = append(s.calls, "push "+unit+" "+filepath.Base(path))
	return s.pushErr
}

func TestSharedStateIsPulledBeforeAndPushedAfterAnApply(t *testing.T) {
	e, r := setup(t)
	store := &fakeStore{}
	e.State = store
	r.reply = planReply(2)
	if err := e.Run(t.Context(), tofuStep("apply"), nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"pull infra infra.tfstate", "push infra infra.tfstate"}; !slices.Equal(store.calls, want) {
		t.Errorf("got %v, want %v", store.calls, want)
	}

	store.calls = nil
	r.reply = planReply(0)
	if err := e.Run(t.Context(), tofuStep("apply"), nil); err != nil {
		t.Fatal(err)
	}
	if want := []string{"pull infra infra.tfstate"}; !slices.Equal(store.calls, want) {
		t.Errorf("nothing applied must push nothing: got %v", store.calls)
	}
}

func TestAFailedApplyStillPushesWhatItChanged(t *testing.T) {
	e, r := setup(t)
	store := &fakeStore{}
	e.State = store
	plan := planReply(2)
	r.reply = func(c toolbox.Cmd) error {
		if slices.Contains(c.Args, "apply") {
			return &toolbox.ExitError{Name: "tofu", Code: 1}
		}
		return plan(c)
	}
	var exit *toolbox.ExitError
	if err := e.Run(t.Context(), tofuStep("apply"), nil); !errors.As(err, &exit) {
		t.Fatalf("the apply's error must come through, got %v", err)
	}
	if store.calls[len(store.calls)-1] != "push infra infra.tfstate" {
		t.Errorf("a half-done apply's state must reach the platform too: %v", store.calls)
	}

	store.pushErr = errors.New("consul down")
	r.reply = planReply(2)
	if err := e.Run(t.Context(), tofuStep("apply"), nil); err == nil || !strings.Contains(err.Error(), "writing the state to the platform") {
		t.Fatalf("a failed push must fail the step, got %v", err)
	}
}

// recorder keeps the events an engine emits.
type recorder struct{ events []event.Event }

func (r *recorder) Emit(e event.Event) { r.events = append(r.events, e) }

func TestTheEngineSaysWhatItDoesAsEvents(t *testing.T) {
	e, r := setup(t)
	r.reply = planReply(0)
	if err := e.Run(t.Context(), tofuStep("apply"), nil); err != nil {
		t.Fatal(err)
	}
	got := e.Events.(*recorder).events
	if !slices.Contains(got, event.Event(event.Note{Text: "Nothing to change."})) {
		t.Errorf("got %#v", got)
	}

	r.reply = planReply(2)
	e.Events = &recorder{}
	if err := e.Run(t.Context(), tofuStep("apply"), nil); err != nil {
		t.Fatal(err)
	}
	if got := e.Events.(*recorder).events; !slices.Contains(got, event.Event(event.Note{Text: "Checking the plan against the policies of the stack.", Break: true})) {
		t.Errorf("got %#v", got)
	}
}

func TestAnApplyReportsEachResourceAgainstThePlan(t *testing.T) {
	e, r := setup(t)
	plan := `{"resource_changes":[
 {"address":"nomad_job.web","change":{"actions":["update"]}},
 {"address":"consul_config_entry.defaults[0]","change":{"actions":["create"]}},
 {"address":"random_password.p","change":{"actions":["no-op"]}}]}`
	var log strings.Builder
	r.reply = func(c toolbox.Cmd) error {
		switch {
		case slices.Contains(c.Args, "plan"):
			return &toolbox.ExitError{Name: "tofu", Code: 2}
		case slices.Contains(c.Args, "show"):
			c.Stdout.Write([]byte(plan))
		case slices.Contains(c.Args, "apply"):
			w := c.Filter(&log)
			w.Write([]byte(`{"@message":"nomad_job.web: Modifying...","type":"apply_start","hook":{"resource":{"addr":"nomad_job.web"},"action":"update"}}
{"@message":"nomad_job.web: Modifications complete after 3s","type":"apply_complete","hook":{"resource":{"addr":"nomad_job.web"},"action":"update","elapsed_seconds":3}}
`))
			w.(io.Closer).Close()
		}
		return nil
	}
	e.Manifest = nil
	step := manifest.Step{Tofu: &manifest.Tofu{Dir: "infra", Action: "apply"}}
	if err := e.Run(t.Context(), step, nil); err != nil {
		t.Fatal(err)
	}
	var tasks []event.Task
	for _, ev := range e.Events.(*recorder).events {
		if task, ok := ev.(event.Task); ok {
			tasks = append(tasks, task)
		}
	}
	want := []event.Task{
		{Name: "nomad_job.web", Status: event.TaskRunning, Done: 0, Total: 2},
		{Name: "nomad_job.web", Status: event.TaskChanged, Done: 1, Total: 2, Took: 3 * time.Second},
	}
	if !slices.Equal(tasks, want) {
		t.Errorf("got %+v\nwant %+v", tasks, want)
	}
	if log.String() != "nomad_job.web: Modifying...\nnomad_job.web: Modifications complete after 3s\n" {
		t.Errorf("the log must keep OpenTofu's text, got %q", log.String())
	}
}

func TestAPlayReportsItsTasksAndRemembersHowManyItHad(t *testing.T) {
	e, r := setup(t)
	r.reply = func(c toolbox.Cmd) error {
		switch {
		case slices.Contains(c.Args, "--list-tasks"):
			c.Stdout.Write([]byte("  play #1 (stack): Provision\tTAGS: []\n    tasks:\n      Install Consul\tTAGS: []\n      Start Consul\tTAGS: []\n"))
		case c.Args[0] == "ansible-playbook":
			f, _ := os.Create(c.Env["DAMSTACK_EVENTS"])
			defer f.Close()
			for _, l := range []string{`{"type":"start","name":"Install Consul"}`, `{"type":"changed","name":"Install Consul"}`,
				`{"type":"start","name":"consul : include"}`, `{"type":"ok","name":"consul : include"}`,
				`{"type":"start","name":"Start Consul"}`, `{"type":"skipped","name":"Start Consul"}`} {
				f.WriteString(l + "\n")
			}
			if c.Env["ANSIBLE_CALLBACKS_ENABLED"] != "damstack_events" || !strings.HasSuffix(c.Env["ANSIBLE_CALLBACK_PLUGINS"], "callbacks") {
				t.Errorf("the plugin is not enabled: %v", c.Env)
			}
		}
		return nil
	}
	step := manifest.Step{Ansible: &manifest.Ansible{Playbook: "ansible/playbooks/site.yml"}}
	if err := e.Run(t.Context(), step, nil); err != nil {
		t.Fatal(err)
	}
	var done []string
	for _, ev := range e.Events.(*recorder).events {
		if task, ok := ev.(event.Task); ok && task.Status != event.TaskRunning {
			done = append(done, fmt.Sprintf("%s %d/%d", task.Name, task.Done, task.Total))
		}
	}
	// Two tasks were listed; the include made three: the count grows.
	if want := []string{"Install Consul 1/2", "consul : include 2/2", "Start Consul 3/3"}; !slices.Equal(done, want) {
		t.Errorf("got %v, want %v", done, want)
	}

	// The next run counts to the three it had, without listing.
	r.cmds = nil
	e.Events = &recorder{}
	if err := e.Run(t.Context(), step, nil); err != nil {
		t.Fatal(err)
	}
	for _, line := range r.lines() {
		if strings.Contains(line, "--list-tasks") {
			t.Errorf("listed again although the last run's count is known: %s", line)
		}
	}
	evs := e.Events.(*recorder).events
	if plan, ok := evs[0].(event.TaskPlan); !ok || !slices.Equal(plan.Names, []string{"Install Consul", "consul : include", "Start Consul"}) {
		t.Errorf("the second run must expect the tasks of the first: %+v", evs[0])
	}
	first := evs[1].(event.Task)
	if first.Total != 3 || !first.Estimate {
		t.Errorf("the second run must count to 3, an estimate: %+v", first)
	}
}

// nomadAnswering is a Nomad that has the job web, version 1, whose
// deployment is in status.
func nomadAnswering(t *testing.T, status string) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/jobs":
			w.Write([]byte(`[{"ID":"web","Namespace":"default"}]`))
		case strings.HasSuffix(r.URL.Path, "/deployment"):
			fmt.Fprintf(w, `{"JobVersion":1,"Status":%q,"StatusDescription":"Deployment %s","TaskGroups":{"web":{"DesiredTotal":1,"HealthyAllocs":1}}}`, status, status)
		case strings.HasSuffix(r.URL.Path, "/allocations"):
			w.Write([]byte(`[]`))
		default:
			w.Write([]byte(`{"ID":"web","Namespace":"default","Type":"service","Version":1}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestAnApplyWaitsForTheJobsItSubmittedToComeUp(t *testing.T) {
	for _, tc := range []struct {
		status string
		ok     bool
	}{{"successful", true}, {"failed", false}} {
		e, r := setup(t)
		r.reply = func(c toolbox.Cmd) error {
			switch {
			case slices.Contains(c.Args, "plan"):
				return &toolbox.ExitError{Name: "tofu", Code: 2}
			case slices.Contains(c.Args, "show"):
				c.Stdout.Write([]byte(`{"resource_changes":[{"address":"nomad_job.web","change":{"actions":["update"]}}]}`))
			case slices.Contains(c.Args, "apply"):
				w := c.Filter(io.Discard)
				w.Write([]byte(`{"@message":"done","type":"apply_complete","hook":{"resource":{"addr":"nomad_job.web","resource_type":"nomad_job"},"action":"update","id_value":"web"}}` + "\n"))
				w.(io.Closer).Close()
			}
			return nil
		}
		step := manifest.Step{Tofu: &manifest.Tofu{Dir: "infra", Action: "apply"},
			Env: map[string]string{"NOMAD_ADDR": nomadAnswering(t, tc.status)}}
		err := e.Run(t.Context(), step, nil)
		if tc.ok != (err == nil) {
			t.Fatalf("deployment %s: got %v", tc.status, err)
		}
		var last event.Task
		for _, ev := range e.Events.(*recorder).events {
			if task, ok := ev.(event.Task); ok && task.Name == "nomad job web" {
				last = task
			}
		}
		if want := map[bool]event.TaskStatus{true: event.TaskOK, false: event.TaskFailed}[tc.ok]; last.Status != want {
			t.Errorf("deployment %s: the job's last task is %+v", tc.status, last)
		}
	}
}

func TestAPlanThatNomadWouldApplyInPlaceAsksToRestart(t *testing.T) {
	nomad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/jobs/parse":
			w.Write([]byte(`{"ID":"web","Namespace":"default"}`))
		case strings.HasSuffix(r.URL.Path, "/plan"):
			w.Write([]byte(`{"Annotations":{"DesiredTGUpdates":{"web":{"InPlaceUpdate":1}}},
"Diff":{"TaskGroups":[{"Name":"web","Type":"Edited","Objects":[{"Type":"Edited","Name":"Service","Objects":[
 {"Type":"Edited","Name":"ConsulConnect","Fields":[{"Type":"Edited","Name":"LocalServicePort"}]}]}]}]}}`))
		}
	}))
	defer nomad.Close()
	e, r := setup(t)
	r.reply = func(c toolbox.Cmd) error {
		switch {
		case slices.Contains(c.Args, "plan"):
			return &toolbox.ExitError{Name: "tofu", Code: 2}
		case slices.Contains(c.Args, "show"):
			c.Stdout.Write([]byte(`{"resource_changes":[{"address":"nomad_job.web","type":"nomad_job","change":{"actions":["update"],"after":{"jobspec":"job \"web\" {}"}}}]}`))
		}
		return nil
	}
	var asked event.Plan
	e.Approve = func(p event.Plan) (event.Approval, error) { asked = p; return event.Approval{}, nil }
	// Not marked confirm: the restart alone makes it ask.
	step := manifest.Step{Tofu: &manifest.Tofu{Dir: "infra", Action: "apply"}, Env: map[string]string{"NOMAD_ADDR": nomad.URL}}
	if err := e.Run(t.Context(), step, nil); !errors.Is(err, ErrDeclined) {
		t.Fatalf("got %v", err)
	}
	if len(asked.Restarts) != 1 || asked.Restarts[0].Group != "web" || asked.Restarts[0].Addr != "nomad_job.web" ||
		asked.Restarts[0].Fields[0] != "Service/ConsulConnect/LocalServicePort" {
		t.Errorf("got %+v", asked.Restarts)
	}
}
