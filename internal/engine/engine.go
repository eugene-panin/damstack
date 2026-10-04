// Package engine runs the steps and commands of a stack on a project: the
// playbooks, OpenTofu with its policies, and the programs the stack names.
package engine

import (
	"bytes"
	"context"
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
	"strings"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
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
	// Confirm asks before a step marked confirm changes anything.
	Confirm func(question string) (bool, error)
	Out     io.Writer
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
	if err := os.MkdirAll(filepath.Join(e.Project.Dir, project.WorkDir), 0o755); err != nil {
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
	return env, nil
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
	cmd = append(cmd, filepath.Join(e.Stack, a.Playbook))
	return e.Runner.Run(ctx, toolbox.Cmd{Args: append(cmd, args...), Env: env})
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
	err := e.init(ctx, env, chdir, "-backend-config=path="+filepath.Join(e.Project.Dir, "state", name+".tfstate"))
	return chdir, data, err
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
	err = e.Runner.Run(ctx, toolbox.Cmd{Env: env, Args: append([]string{"tofu", chdir, "plan", "-input=false",
		"-detailed-exitcode", "-out=" + plan}, args...)})
	var exit *toolbox.ExitError
	switch {
	case err == nil:
		if t.Action == "apply" {
			fmt.Fprintln(e.Out, "Nothing to change.")
			return e.outputs(ctx, t, env, chdir)
		}
		return nil
	case !errors.As(err, &exit) || exit.Code != 2:
		return err
	}
	planJSON := ""
	if t.Policy != nil || (t.Action == "apply" && s.Confirm && e.Brief) {
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
	if s.Confirm {
		question := "Apply the changes above?"
		if e.Brief {
			counts, err := e.counts(planJSON)
			if err != nil {
				return err
			}
			question = counts + ". Go?"
		}
		ok, err := e.Confirm(question)
		if err != nil {
			return err
		}
		if !ok {
			return ErrDeclined
		}
	}
	if err := e.Runner.Run(ctx, toolbox.Cmd{Env: env, Args: []string{"tofu", chdir, "apply", "-input=false", plan}}); err != nil {
		return err
	}
	if err := os.Remove(plan); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return e.outputs(ctx, t, env, chdir)
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
	cmd := append([]string{"tofu", chdir, "init", "-input=false", "-lockfile=readonly"}, args...)
	if e.Runner.Run(ctx, toolbox.Cmd{Env: env, Quiet: true, Silent: true, Args: cmd}) == nil {
		return nil
	}
	fmt.Fprintln(e.Out, "OpenTofu could not fetch its modules or providers; trying once more.")
	return e.Runner.Run(ctx, toolbox.Cmd{Env: env, Quiet: true, Args: cmd})
}

// policy checks a plan against the stack's policies, with stack.yaml as their
// data, and so the Nomad jobs the plan would submit.
// showPlan writes a plan as JSON next to it, and returns its path.
func (e *Engine) showPlan(ctx context.Context, env map[string]string, chdir, plan string) (string, error) {
	planJSON := plan + ".json"
	out, err := os.Create(planJSON)
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

// counts says in words what a plan changes: 12 to create, 1 to destroy.
func (e *Engine) counts(planJSON string) (string, error) {
	data, err := os.ReadFile(planJSON)
	if err != nil {
		return "", err
	}
	var plan struct {
		ResourceChanges []struct {
			Change struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(data, &plan); err != nil {
		return "", fmt.Errorf("the plan: %w", err)
	}
	n := map[string]int{}
	for _, rc := range plan.ResourceChanges {
		switch a := strings.Join(rc.Change.Actions, ","); a {
		case "create", "update", "delete":
			n[a]++
		case "delete,create", "create,delete":
			n["replace"]++
		}
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
	fmt.Fprintln(e.Out, "\nChecking the plan against the policies of the stack.")
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
	fmt.Fprintf(e.Out, "Moved %s into %s.\n", k.File, project.VaultFile)
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
