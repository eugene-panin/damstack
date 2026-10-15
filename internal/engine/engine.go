// Package engine runs the steps and commands of a stack on a project: the
// playbooks, OpenTofu with its policies, and the programs the stack names.
package engine

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eugene-panin/damstack/internal/ansibleevents"
	"github.com/eugene-panin/damstack/internal/event"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/nomadwatch"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/tofujson"
	"github.com/eugene-panin/damstack/internal/toolbox"
)

// Runner runs a command with the toolbox; toolbox.Runner is one.
type Runner interface {
	Run(ctx context.Context, c toolbox.Cmd) error
}

type Engine struct {
	Manifest *manifest.Manifest
	// Stack is the stack's directory on this machine.
	Stack    string
	Project  *project.Project
	Runner   Runner
	Password string
	// PasswordFile holds Password, for Ansible; KeyFile is the private SSH
	// key of the playbooks, empty when an ssh-agent holds it.
	PasswordFile string
	KeyFile      string
	// Color is whether the output goes to a terminal.
	Color bool
	// App is the name of the app the manifest is of, empty for the platform:
	// its templates see .app, its settings in stack.yaml, and its OpenTofu
	// state and work files are its own.
	App string
	// BaseEnv is under the environment of every step, such as the app_env
	// of the platform for an app.
	BaseEnv map[string]string
	// Brief is whether the output of the tools goes to a log rather than the
	// terminal: a change is then confirmed by its counts.
	Brief bool
	// Approve asks whether to apply a plan: before a step marked confirm
	// changes anything, and before any whose plan needs allocations replaced.
	Approve func(event.Plan) (event.Approval, error)
	// Events gets what the engine says while it works; nil drops it.
	Events event.Sink
	// State, when set, is where the OpenTofu state of the steps is shared
	// (the platform): it is pulled into the project's state file before a step
	// and pushed back after an apply. Nil keeps the state in the project only.
	State StateStore
}

// StateStore keeps the OpenTofu state of a unit outside the project, such as
// remote.Store on the platform's Consul.
type StateStore interface {
	Pull(unit, path string) error
	Push(unit, path string) error
}

// ErrDeclined is returned when the person said no to a confirm.
var ErrDeclined = errors.New("stopped: you said no")

func (e *Engine) work() string       { return filepath.Join(e.Project.Dir, project.WorkDir) }
func (e *Engine) knownHosts() string { return filepath.Join(e.Project.Dir, project.KnownHosts) }

// Run runs one step, or a command with args after its own.
func (e *Engine) Run(ctx context.Context, s manifest.Step, args []string) error {
	config, err := e.Project.Config()
	if err != nil {
		return err
	}
	secrets, err := e.Project.Secrets(e.Password)
	if err != nil {
		return err
	}
	env, err := e.env(s, config, secrets)
	if err != nil {
		return err
	}
	if _, err := e.Project.Work(); err != nil {
		return err
	}

	switch {
	case s.Ansible != nil:
		err = e.ansible(ctx, s.Ansible, env, args)
	case s.Tofu != nil:
		err = e.tofu(ctx, s, env, args)
	default:
		program := s.Run[0]
		if strings.Contains(program, "/") {
			program = filepath.Join(e.Stack, program)
		}
		err = e.Runner.Run(ctx, toolbox.Cmd{Args: append(append([]string{program}, s.Run[1:]...), args...), Env: env})
	}
	if err != nil {
		return err
	}
	if s.Keep != nil {
		return e.keep(s.Keep)
	}
	return nil
}

func (e *Engine) env(s manifest.Step, config, secrets map[string]any) (map[string]string, error) {
	data := map[string]any{"config": config, "dir": e.Dirs()}
	if e.App != "" {
		apps, _ := config["apps"].(map[string]any)
		data["app"] = apps[e.App]
	}
	env := maps.Clone(e.BaseEnv)
	if env == nil {
		env = map[string]string{}
	}
	for key, text := range s.Env {
		var err error
		if env[key], err = manifest.RenderData(key, text, data, secrets); err != nil {
			return nil, fmt.Errorf("env %s: %w", key, err)
		}
	}
	if e.App != "" && e.Manifest != nil {
		maps.Copy(env, e.appSecrets(secrets))
	}
	return env, nil
}

// AllServices is the service an app's secret goes to when its For names none.
const AllServices = "*"

// appSecrets is how an app's stack gets the secrets its services take as
// environment variables (those with env in its manifest that are set):
//
//   - TF_VAR_app_secret_envs: for each, its variable and its services; no
//     values, so the stack may template jobs from it;
//   - TF_VAR_app_secrets: the values, for a variable the stack declares
//     ephemeral and writes to Vault write-only, so they reach neither
//     stack.yaml nor the state;
//   - TF_VAR_app_secret_versions: for each service, a version of its secrets
//     (an HMAC of them, keyed by the project's password): it changes, and the
//     service restarts, only when one of its own secrets does.
//
// A stack that declares none of these variables is not given them.
func (e *Engine) appSecrets(secrets map[string]any) map[string]string {
	type envOf struct {
		Env string   `json:"env"`
		For []string `json:"for"`
	}
	envs, values := map[string]envOf{}, map[string]string{}
	byService := map[string][]string{}
	for _, s := range e.Manifest.Secrets {
		value, _ := secrets[s.Name].(string)
		if s.Env == "" || value == "" {
			continue
		}
		services := s.For
		if len(services) == 0 {
			services = []string{AllServices}
		}
		envs[s.Name] = envOf{Env: s.Env, For: services}
		values[s.Name] = value
		for _, svc := range services {
			byService[svc] = append(byService[svc], s.Name+"="+value)
		}
	}
	versions := map[string]int64{}
	for svc, pairs := range byService {
		slices.Sort(pairs)
		mac := hmac.New(sha256.New, []byte(e.Password))
		mac.Write([]byte(strings.Join(pairs, "\n")))
		versions[svc] = int64(binary.BigEndian.Uint32(mac.Sum(nil)) >> 1)
	}
	out := map[string]string{}
	for key, v := range map[string]any{
		"TF_VAR_app_secret_envs": envs, "TF_VAR_app_secrets": values, "TF_VAR_app_secret_versions": versions,
	} {
		data, _ := json.Marshal(v)
		out[key] = string(data)
	}
	return out
}

// unit is the name of an OpenTofu directory in the project: its state file
// and work directory, prefixed with the app it is of.
func (e *Engine) unit(dir string) string {
	if e.App != "" {
		return e.App + "-" + filepath.Base(dir)
	}
	return filepath.Base(dir)
}

func (e *Engine) ansible(ctx context.Context, a *manifest.Ansible, env map[string]string, args []string) error {
	env["ANSIBLE_COLLECTIONS_PATH"] = filepath.Join(e.work(), "collections")
	env["ANSIBLE_VAULT_PASSWORD_FILE"] = e.PasswordFile
	env["ANSIBLE_SSH_ARGS"] = "-F /dev/null -C -o ControlMaster=auto -o ControlPersist=60s " +
		"-o UserKnownHostsFile=" + e.knownHosts() + " -o StrictHostKeyChecking=accept-new"
	if cfg := e.ansibleConfig(a.Playbook); cfg != "" {
		env["ANSIBLE_CONFIG"] = filepath.Join(e.Stack, cfg)
	}
	if a.Requirements != "" {
		err := e.Runner.Run(ctx, toolbox.Cmd{Env: env, Quiet: true, Args: []string{
			"ansible-galaxy", "collection", "install", "-r", filepath.Join(e.Stack, a.Requirements),
			"-p", filepath.Join(e.work(), "collections")}})
		if err != nil {
			return err
		}
	}
	cmd := []string{"ansible-playbook"}
	if a.Inventory != "" {
		cmd = append(cmd, "-i", filepath.Join(e.Stack, a.Inventory))
	}
	if vault := filepath.Join(e.Project.Dir, project.VaultFile); exists(vault) {
		cmd = append(cmd, "-e", "@"+vault)
	}
	if e.KeyFile != "" {
		cmd = append(cmd, "--private-key", e.KeyFile)
	}
	cmd = append(append(cmd, filepath.Join(e.Stack, a.Playbook)), args...)
	return e.play(ctx, cmd, env, e.unit(strings.TrimSuffix(a.Playbook, filepath.Ext(a.Playbook))))
}

// play runs a playbook and reports each of its tasks as a Task event, counted
// to an estimate: how many tasks the same play had the last time, or before
// that the tasks ansible-playbook can list ahead (roles included at run time
// are not among them, so the count grows as the play goes).
func (e *Engine) play(ctx context.Context, cmd []string, env map[string]string, name string) error {
	dir := filepath.Join(e.work(), "progress")
	if err := os.MkdirAll(filepath.Join(dir, "callbacks"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "callbacks", ansibleevents.PluginName+".py"), ansibleevents.Plugin, 0o644); err != nil {
		return err
	}
	// The last run's events are what this one is expected to go through.
	events, previous := filepath.Join(dir, name+".jsonl"), filepath.Join(dir, name+".prev.jsonl")
	if err := os.Rename(events, previous); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if names := ansibleevents.Names(previous); len(names) > 0 {
		e.emit(event.TaskPlan{Names: names})
	}
	env["ANSIBLE_CALLBACK_PLUGINS"] = filepath.Join(dir, "callbacks")
	env["ANSIBLE_CALLBACKS_ENABLED"] = ansibleevents.PluginName
	env["DAMSTACK_EVENTS"] = events

	countFile := filepath.Join(dir, name+".tasks")
	total := 0
	if data, err := os.ReadFile(countFile); err == nil {
		total, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	} else {
		var list bytes.Buffer
		if e.Runner.Run(ctx, toolbox.Cmd{Env: env, Stdout: &list, Args: append(slices.Clone(cmd), "--list-tasks")}) == nil {
			total = ansibleevents.Count(list.String())
		}
	}

	following, stop := context.WithCancel(ctx)
	defer stop()
	done := 0
	var wg sync.WaitGroup
	wg.Go(func() {
		_ = ansibleevents.Follow(following, events, func(t ansibleevents.Task) {
			task := event.Task{Name: t.Name, Estimate: true}
			switch t.Status {
			case ansibleevents.Started:
				task.Status = event.TaskRunning
			case ansibleevents.OK:
				done++
				task.Status = event.TaskOK
			case ansibleevents.Changed:
				done++
				task.Status = event.TaskChanged
			case ansibleevents.Failed:
				done++
				task.Status = event.TaskFailed
			case ansibleevents.Skipped:
				done++
				task.Status = event.TaskSkipped
			}
			task.Done, task.Total = done, max(total, done)
			e.emit(task)
		})
	})
	err := e.Runner.Run(ctx, toolbox.Cmd{Args: cmd, Env: env})
	stop()
	wg.Wait()
	if err == nil && done > 0 {
		// A whole play: next time, its count is the estimate.
		_ = os.WriteFile(countFile, []byte(strconv.Itoa(done)+"\n"), 0o644)
	}
	return err
}

// ansibleConfig is the ansible.cfg nearest the playbook, in its directory or
// one above it within the stack, as a path in the stack.
func (e *Engine) ansibleConfig(playbook string) string {
	dir := filepath.Dir(filepath.FromSlash(playbook))
	for {
		candidate := filepath.Join(dir, "ansible.cfg")
		if _, err := os.Stat(filepath.Join(e.Stack, candidate)); err == nil {
			return filepath.ToSlash(candidate)
		}
		if dir == "." {
			return ""
		}
		dir = filepath.Dir(dir)
	}
}

// prepareTofu sets up the environment of an OpenTofu directory and inits it,
// and returns its -chdir and its work directory.
func (e *Engine) prepareTofu(ctx context.Context, t *manifest.Tofu, env map[string]string) (string, string, error) {
	name := e.unit(t.Dir)
	data := filepath.Join(e.work(), "tofu", name)
	env["TF_DATA_DIR"] = data
	env["TF_IN_AUTOMATION"] = "1"
	env["TF_INPUT"] = "0"
	env["TF_VAR_project"] = e.Project.Dir
	if !e.Color {
		env["TF_CLI_ARGS"] = "-no-color"
	}
	chdir := "-chdir=" + filepath.Join(e.Stack, t.Dir)
	for _, dir := range []string{filepath.Join(e.Project.Dir, "state"), data} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", "", err
		}
	}
	state := e.statePath(t)
	if e.State != nil {
		if err := e.State.Pull(name, state); err != nil {
			return "", "", err
		}
	}
	err := e.init(ctx, env, chdir, "-backend-config=path="+state)
	return chdir, data, err
}

// statePath is the state file of an OpenTofu directory in the project.
func (e *Engine) statePath(t *manifest.Tofu) string {
	return filepath.Join(e.Project.Dir, "state", e.unit(t.Dir)+".tfstate")
}

// Drift says what a step of OpenTofu would change now, without changing
// anything: the counts of its plan, empty when nothing but outputs differs.
func (e *Engine) Drift(ctx context.Context, s manifest.Step) (string, error) {
	config, err := e.Project.Config()
	if err != nil {
		return "", err
	}
	secrets, err := e.Project.Secrets(e.Password)
	if err != nil {
		return "", err
	}
	env, err := e.env(s, config, secrets)
	if err != nil {
		return "", err
	}
	chdir, data, err := e.prepareTofu(ctx, s.Tofu, env)
	if err != nil {
		return "", err
	}
	plan := filepath.Join(data, "drift")
	defer os.Remove(plan)
	defer os.Remove(plan + ".json")
	err = e.Runner.Run(ctx, toolbox.Cmd{Env: env, Args: []string{"tofu", chdir, "plan", "-input=false", "-detailed-exitcode", "-out=" + plan}})
	var exit *toolbox.ExitError
	switch {
	case err == nil:
		return "", nil
	case !errors.As(err, &exit) || exit.Code != 2:
		return "", err
	}
	planJSON, err := e.showPlan(ctx, env, chdir, plan)
	if err != nil {
		return "", err
	}
	counts, err := e.counts(planJSON)
	if err != nil || counts == onlyOutputs {
		return "", err
	}
	return counts, nil
}

func (e *Engine) tofu(ctx context.Context, s manifest.Step, env map[string]string, args []string) error {
	t := s.Tofu
	chdir, data, err := e.prepareTofu(ctx, t, env)
	if err != nil {
		return err
	}
	if t.Action == "output" {
		return e.Runner.Run(ctx, toolbox.Cmd{Env: env, Args: append([]string{"tofu", chdir, "output"}, args...)})
	}

	plan := filepath.Join(data, "plan")
	defer os.Remove(plan)
	defer os.Remove(plan + ".json")
	err = e.Runner.Run(ctx, toolbox.Cmd{Env: env, Args: append([]string{"tofu", chdir, "plan", "-input=false",
		"-detailed-exitcode", "-out=" + plan}, args...)})
	var exit *toolbox.ExitError
	switch {
	case err == nil:
		if t.Action == "apply" {
			e.note("Nothing to change.", false)
			return e.outputs(ctx, t, env, chdir)
		}
		return nil
	case !errors.As(err, &exit) || exit.Code != 2:
		return err
	}
	planJSON := ""
	// An apply reads its plan too: the number of resources it changes is what
	// its progress counts to.
	if t.Policy != nil || t.Action == "apply" {
		if planJSON, err = e.showPlan(ctx, env, chdir, plan); err != nil {
			return err
		}
	}
	if t.Policy != nil {
		if err := e.policy(ctx, t, env, chdir, planJSON); err != nil {
			return err
		}
	}
	if t.Action != "apply" {
		return nil
	}
	pl, err := e.planOf(ctx, env, planJSON, t.Policy != nil)
	if err != nil {
		return err
	}
	// A step marked confirm asks; any step asks when its plan needs
	// allocations replaced, as Nomad would not.
	approval := event.Approval{Apply: true}
	if s.Confirm || len(pl.Restarts) > 0 {
		if approval, err = e.Approve(pl); err != nil {
			return err
		}
		if !approval.Apply {
			return ErrDeclined
		}
	}
	total := 0
	if n, err := changes(planJSON); err == nil {
		for _, c := range n {
			total += c
		}
	}
	if names := planned(planJSON); len(names) > 0 {
		e.emit(event.TaskPlan{Names: names})
	}
	var jobs []string
	err = e.Runner.Run(ctx, toolbox.Cmd{Env: env, Args: []string{"tofu", chdir, "apply", "-input=false", "-json", plan},
		Filter: e.applyProgress(total, &jobs)})
	// A failed apply may have changed things too: its state is pushed either way.
	if e.State != nil {
		if perr := e.State.Push(e.unit(t.Dir), e.statePath(t)); perr != nil {
			return errors.Join(err, fmt.Errorf("writing the state to the platform: %w", perr))
		}
	}
	if err != nil {
		return err
	}
	if err := e.waitJobs(ctx, env, jobs); err != nil {
		return err
	}
	if approval.Restart {
		if err := e.restart(ctx, env, pl.Restarts); err != nil {
			return err
		}
	}
	return e.outputs(ctx, t, env, chdir)
}

// JobsTimeout is how long a step waits for the Nomad jobs it submitted to come
// up, past Nomad's own deadlines.
const JobsTimeout = 15 * time.Minute

// waitJobs waits for the Nomad jobs an apply submitted to come up, not just to
// be accepted, reporting each as a Task. A step whose environment names no
// Nomad submits none to wait for.
func (e *Engine) waitJobs(ctx context.Context, env map[string]string, jobs []string) error {
	if len(jobs) == 0 {
		return nil
	}
	nomad, err := nomadwatch.FromEnv(env)
	if err != nil || nomad == nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, JobsTimeout)
	defer cancel()
	for i, id := range jobs {
		name := "nomad job " + id
		err := nomad.Wait(ctx, id, func(p nomadwatch.Progress) {
			e.emit(event.Task{Name: name, Status: event.TaskRunning, Done: i, Total: len(jobs),
				Detail: fmt.Sprintf("%s, %d of %d up", p.Detail, p.Healthy, p.Desired)})
		})
		if err != nil {
			e.emit(event.Task{Name: name, Status: event.TaskFailed, Done: i + 1, Total: len(jobs)})
			return err
		}
		e.emit(event.Task{Name: name, Status: event.TaskOK, Done: i + 1, Total: len(jobs), Detail: "up"})
	}
	return nil
}

// outputs writes the outputs a step names, as JSON, to their files.
func (e *Engine) outputs(ctx context.Context, t *manifest.Tofu, env map[string]string, chdir string) error {
	for _, name := range slices.Sorted(maps.Keys(t.Outputs)) {
		file := filepath.Join(e.Project.Dir, filepath.FromSlash(t.Outputs[name]))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
		var out bytes.Buffer
		if err := e.Runner.Run(ctx, toolbox.Cmd{Env: env, Stdout: &out, Args: []string{"tofu", chdir, "output", "-json", name}}); err != nil {
			return err
		}
		if err := os.WriteFile(file, out.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// init runs tofu init, twice when the first fails: it fetches modules and
// providers from registries, which fail now and then.
func (e *Engine) init(ctx context.Context, env map[string]string, chdir string, args ...string) error {
	cmd := append([]string{"tofu", chdir, "init", "-input=false", "-reconfigure", "-lockfile=readonly"}, args...)
	if e.Runner.Run(ctx, toolbox.Cmd{Env: env, Quiet: true, Silent: true, Args: cmd}) == nil {
		return nil
	}
	e.note("OpenTofu could not fetch its modules or providers; trying once more.", false)
	return e.Runner.Run(ctx, toolbox.Cmd{Env: env, Quiet: true, Args: cmd})
}

// policy checks a plan against the stack's policies, with stack.yaml as their
// data, and so the Nomad jobs the plan would submit.
// showPlan writes a plan as JSON next to it, and returns its path.
func (e *Engine) showPlan(ctx context.Context, env map[string]string, chdir, plan string) (string, error) {
	planJSON := plan + ".json"
	out, err := os.OpenFile(planJSON, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	err = e.Runner.Run(ctx, toolbox.Cmd{Env: env, Stdout: out, Args: []string{"tofu", chdir, "show", "-json", plan}})
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return planJSON, err
}

const onlyOutputs = "Only outputs change"

// resourceChange is a resource a plan changes: its address, and create,
// update, replace or delete.
type resourceChange struct{ addr, action string }

// resourceChanges are the resources a plan changes, in its order.
func resourceChanges(planJSON string) ([]resourceChange, error) {
	data, err := os.ReadFile(planJSON)
	if err != nil {
		return nil, err
	}
	var plan struct {
		ResourceChanges []struct {
			Address string `json:"address"`
			Change  struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("the plan: %w", err)
	}
	var out []resourceChange
	for _, rc := range plan.ResourceChanges {
		switch a := strings.Join(rc.Change.Actions, ","); a {
		case "create", "update", "delete":
			out = append(out, resourceChange{rc.Address, a})
		case "delete,create", "create,delete":
			out = append(out, resourceChange{rc.Address, "replace"})
		}
	}
	return out, nil
}

// changes counts the resources a plan changes, by what it does to them:
// create, update, replace and delete.
func changes(planJSON string) (map[string]int, error) {
	rcs, err := resourceChanges(planJSON)
	if err != nil {
		return nil, err
	}
	n := map[string]int{}
	for _, rc := range rcs {
		n[rc.action]++
	}
	return n, nil
}

// planned are the addresses of the resources a plan changes; none when it
// cannot be read.
func planned(planJSON string) []string {
	rcs, _ := resourceChanges(planJSON)
	names := make([]string, 0, len(rcs))
	for _, rc := range rcs {
		names = append(names, rc.addr)
	}
	return names
}

// counts says in words what a plan changes: 12 to create, 1 to destroy.
func (e *Engine) counts(planJSON string) (string, error) {
	n, err := changes(planJSON)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, k := range []struct{ action, words string }{
		{"create", "to create"}, {"update", "to change"}, {"replace", "to replace"}, {"delete", "to destroy"},
	} {
		if n[k.action] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n[k.action], k.words))
		}
	}
	if len(parts) == 0 {
		return onlyOutputs, nil
	}
	return strings.Join(parts, ", "), nil
}

func (e *Engine) policy(ctx context.Context, t *manifest.Tofu, env map[string]string, chdir, planJSON string) error {
	flags := e.policyFlags(t.Policy)
	e.note("Checking the plan against the policies of the stack.", true)
	cmd := append(append([]string{"conftest", "test"}, flags...), "--namespace", "terraform", planJSON)
	if err := e.Runner.Run(ctx, toolbox.Cmd{Env: env, Args: cmd}); err != nil {
		return policyError(err)
	}
	if !t.Policy.NomadJobs {
		return nil
	}
	jobs, err := e.nomadJobs(planJSON)
	if err != nil || len(jobs) == 0 {
		return err
	}
	cmd = append(append([]string{"conftest", "test"}, flags...), append([]string{"--parser", "hcl2", "--namespace", "nomad"}, jobs...)...)
	return policyError(e.Runner.Run(ctx, toolbox.Cmd{Env: env, Args: cmd}))
}

// ErrPolicy is a plan that breaks a policy of the stack.
var ErrPolicy = errors.New("the plan breaks a rule of the stack, shown above; nothing was changed")

func policyError(err error) error {
	var exit *toolbox.ExitError
	if errors.As(err, &exit) && exit.Code == 1 {
		return ErrPolicy
	}
	return err
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// nomadJobs writes the job specs of the nomad_job resources a plan creates or
// changes, and returns their paths.
func (e *Engine) nomadJobs(planJSON string) ([]string, error) {
	data, err := os.ReadFile(planJSON)
	if err != nil {
		return nil, err
	}
	var plan struct {
		ResourceChanges []struct {
			Address string `json:"address"`
			Type    string `json:"type"`
			Change  struct {
				After map[string]any `json:"after"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("the plan: %w", err)
	}
	dir := filepath.Join(e.work(), "jobs")
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var jobs []string
	for _, rc := range plan.ResourceChanges {
		spec, _ := rc.Change.After["jobspec"].(string)
		if rc.Type != "nomad_job" || spec == "" {
			continue
		}
		job := filepath.Join(dir, unsafeName.ReplaceAllString(rc.Address, "_")+".nomad.hcl")
		if err := os.WriteFile(job, []byte(spec), 0o644); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	slices.Sort(jobs)
	return jobs, nil
}

// keep moves the values a step left in a file into the secrets.
func (e *Engine) keep(k *manifest.Keep) error {
	file := filepath.Join(e.Project.Dir, filepath.FromSlash(k.File))
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var values map[string]any
	if err := json.Unmarshal(data, &values); err != nil {
		return fmt.Errorf("%s: %w", k.File, err)
	}
	secrets, err := e.Project.Secrets(e.Password)
	if err != nil {
		return err
	}
	for name, key := range k.Secrets {
		v, ok := values[key]
		if !ok {
			return fmt.Errorf("%s has no %s for the secret %s", k.File, key, name)
		}
		secrets[name] = v
	}
	if err := e.Project.SaveSecrets(e.Password, secrets); err != nil {
		return err
	}
	e.note(fmt.Sprintf("Moved %s into %s.", k.File, project.VaultFile), false)
	return os.Remove(file)
}

// Dirs are the directories of the project and the stack, which templates
// see as .dir.project and .dir.stack.
func (e *Engine) Dirs() map[string]string {
	return map[string]string{"project": e.Project.Dir, "stack": e.Stack}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func (e *Engine) emit(ev event.Event) {
	if e.Events != nil {
		e.Events.Emit(ev)
	}
}

func (e *Engine) note(text string, apart bool) {
	e.emit(event.Note{Text: text, Break: apart})
}

// applyProgress reads an apply's -json output: OpenTofu's text goes on to the
// log or the terminal, and each resource it changes is a Task event, counted
// to total, the resources the plan changes. The Nomad jobs it submits are
// added to jobs, to wait for after.
func (e *Engine) applyProgress(total int, jobs *[]string) func(io.Writer) io.Writer {
	return func(w io.Writer) io.Writer {
		done := 0
		return tofujson.NewWriter(w, func(r tofujson.Resource) {
			t := event.Task{Name: r.Addr, Took: r.Elapsed}
			switch r.Status {
			case tofujson.Started:
				t.Status = event.TaskRunning
			case tofujson.Done:
				done++
				t.Status = event.TaskChanged
				if r.Type == "nomad_job" && r.Action != "delete" && r.ID != "" {
					*jobs = append(*jobs, r.ID)
				}
			case tofujson.Failed:
				done++
				t.Status = event.TaskFailed
			}
			// A data source read during the apply is not in the plan's count.
			t.Done, t.Total = done, max(total, done)
			e.emit(t)
		})
	}
}

// planOf is a plan as it is approved: what it changes, the question plain
// lines ask, and the Nomad groups it would change in place in a way that
// needs new allocations.
func (e *Engine) planOf(ctx context.Context, env map[string]string, planJSON string, policies bool) (event.Plan, error) {
	rcs, err := resourceChanges(planJSON)
	if err != nil {
		return event.Plan{}, err
	}
	pl := event.Plan{Policies: policies, Question: "Apply the changes above?"}
	for _, rc := range rcs {
		pl.Changes = append(pl.Changes, event.Change{Addr: rc.addr, Action: rc.action})
		switch rc.action {
		case "create":
			pl.Create++
		case "update":
			pl.Update++
		case "replace":
			pl.Replace++
		case "delete":
			pl.Delete++
		}
	}
	if e.Brief {
		counts, err := e.counts(planJSON)
		if err != nil {
			return pl, err
		}
		pl.Question = counts + ". Go?"
	}
	pl.Restarts = e.restartsOf(ctx, env, planJSON, rcs)
	return pl, nil
}

// restartsOf asks Nomad, changing nothing, which groups of the jobs a plan
// updates would be changed in place in a way that needs new allocations. Not
// knowing is no reason to stop: it says so and goes on.
func (e *Engine) restartsOf(ctx context.Context, env map[string]string, planJSON string, rcs []resourceChange) []event.Restart {
	updated := map[string]bool{}
	for _, rc := range rcs {
		if rc.action == "update" && strings.Contains(rc.addr, "nomad_job.") {
			updated[rc.addr] = true
		}
	}
	if len(updated) == 0 {
		return nil
	}
	nomad, err := nomadwatch.FromEnv(env)
	if err != nil || nomad == nil {
		return nil
	}
	data, err := os.ReadFile(planJSON)
	if err != nil {
		return nil
	}
	var plan struct {
		ResourceChanges []struct {
			Address string `json:"address"`
			Change  struct {
				After map[string]any `json:"after"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if json.Unmarshal(data, &plan) != nil {
		return nil
	}
	var out []event.Restart
	for _, rc := range plan.ResourceChanges {
		spec, _ := rc.Change.After["jobspec"].(string)
		if !updated[rc.Address] || spec == "" {
			continue
		}
		groups, err := nomad.Plan(ctx, spec)
		if err != nil {
			e.note(fmt.Sprintf("Nomad could not plan %s, so whether it needs new allocations is not known: %v", rc.Address, err), false)
			continue
		}
		for _, g := range groups {
			out = append(out, event.Restart{Addr: rc.Address, Job: g.Job, Group: g.Group, Fields: g.Fields})
		}
	}
	return out
}

// restart replaces the allocations of the groups a plan changed in place, one
// group and one allocation at a time, reporting each group as a Task.
func (e *Engine) restart(ctx context.Context, env map[string]string, restarts []event.Restart) error {
	nomad, err := nomadwatch.FromEnv(env)
	if err != nil || nomad == nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, JobsTimeout)
	defer cancel()
	for i, r := range restarts {
		name := "restart " + r.Job + "/" + r.Group
		err := nomad.Restart(ctx, r.Job, r.Group, func(p nomadwatch.Progress) {
			e.emit(event.Task{Name: name, Status: event.TaskRunning, Done: i, Total: len(restarts),
				Detail: fmt.Sprintf("%d of %d replaced", p.Healthy, p.Desired)})
		})
		if err != nil {
			e.emit(event.Task{Name: name, Status: event.TaskFailed, Done: i + 1, Total: len(restarts)})
			return err
		}
		e.emit(event.Task{Name: name, Status: event.TaskChanged, Done: i + 1, Total: len(restarts), Detail: "replaced"})
	}
	return nil
}
