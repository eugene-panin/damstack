package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
	return &Engine{Stack: stack, Project: p, Runner: r, Password: password, PasswordFile: "/c/vault-pass", KeyFile: "/h/.ssh/id_ed25519", Out: &bytes.Buffer{},
		Confirm: func(string) (bool, error) { return true, nil }}, r
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
		"ansible-playbook -i /stack/ansible/inventory -e @/work/vault.yml --private-key /h/.ssh/id_ed25519 /stack/ansible/playbooks/site.yml --check",
	}
	if got := r.lines(); !slices.Equal(got, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	env := r.env(1)
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
	e.Confirm = func(q string) (bool, error) { asked = q; return true, nil }
	if err := e.Run(t.Context(), tofuStep("apply"), nil); err != nil {
		t.Fatal(err)
	}
	data := " --no-color --policy /stack/policy --data /work/stack.yaml --data /stack/policy/data"
	want := []string{
		"tofu -chdir=/stack/infra init -input=false -lockfile=readonly -backend-config=path=/work/state/infra.tfstate",
		"tofu -chdir=/stack/infra plan -input=false -detailed-exitcode -out=/work/.damstack/work/tofu/infra/plan",
		"tofu -chdir=/stack/infra show -json /work/.damstack/work/tofu/infra/plan",
		"conftest test" + data + " --namespace terraform /work/.damstack/work/tofu/infra/plan.json",
		"conftest test" + data + " --parser hcl2 --namespace nomad /work/.damstack/work/jobs/module.app.nomad_job.web.nomad.hcl",
		"tofu -chdir=/stack/infra apply -input=false /work/.damstack/work/tofu/infra/plan",
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
	e.Confirm = func(string) (bool, error) { return false, nil }
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
	if lines[0] != "tofu -chdir=/stack/infra init -input=false -lockfile=readonly -backend-config=path=/work/state/mail-infra.tfstate" ||
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
	e.Confirm = func(q string) (bool, error) { asked = q; return false, nil }
	step := manifest.Step{Tofu: &manifest.Tofu{Dir: "infra", Action: "apply"}, Confirm: true}
	if err := e.Run(t.Context(), step, nil); !errors.Is(err, ErrDeclined) {
		t.Fatal(err)
	}
	if asked != "2 to create, 1 to replace, 1 to destroy. Go?" {
		t.Errorf("asked %q", asked)
	}
}
