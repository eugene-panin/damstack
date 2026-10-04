package engine

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/toolbox"
)

// Check proves the stack on a project set up from its test answers, without
// a server: the playbooks parse, the OpenTofu directories validate, the
// policies pass their tests, then check.steps run.
func (e *Engine) Check(ctx context.Context) error {
	steps := slices.Clone(e.Manifest.Steps)
	for _, name := range slices.Sorted(maps.Keys(e.Manifest.Commands)) {
		steps = append(steps, e.Manifest.Commands[name])
	}
	for _, target := range slices.Sorted(maps.Keys(e.Manifest.Targets)) {
		t := e.Manifest.Targets[target]
		steps = append(steps, t.Steps...)
		for _, name := range slices.Sorted(maps.Keys(t.Commands)) {
			steps = append(steps, t.Commands[name])
		}
	}
	config, err := e.Project.Config()
	if err != nil {
		return err
	}
	secrets, err := e.Project.Secrets(e.Password)
	if err != nil {
		return err
	}

	seen := map[string]bool{}
	for _, s := range steps {
		env, err := e.env(s, config, secrets)
		if err != nil {
			return err
		}
		switch {
		case s.Ansible != nil && !seen["ansible "+s.Ansible.Playbook]:
			seen["ansible "+s.Ansible.Playbook] = true
			e.say("the playbook %s parses", s.Ansible.Playbook)
			if err := e.ansible(ctx, s.Ansible, env, []string{"--syntax-check"}); err != nil {
				return err
			}
		case s.Tofu != nil && !seen["tofu "+s.Tofu.Dir]:
			seen["tofu "+s.Tofu.Dir] = true
			e.say("OpenTofu in %s validates", s.Tofu.Dir)
			if err := e.validate(ctx, s.Tofu.Dir, env); err != nil {
				return err
			}
		}
		if s.Tofu != nil && s.Tofu.Policy != nil && !seen["policy "+s.Tofu.Policy.Dir] {
			seen["policy "+s.Tofu.Policy.Dir] = true
			e.say("the policies in %s pass their tests", s.Tofu.Policy.Dir)
			cmd := append([]string{"conftest", "verify"}, e.policyFlags(s.Tofu.Policy)...)
			if err := e.Runner.Run(ctx, toolbox.Cmd{Args: cmd}); err != nil {
				return err
			}
		}
	}
	for _, s := range e.Manifest.Check.Steps {
		e.say("%s", s.Name)
		if err := e.Run(ctx, s, nil); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) validate(ctx context.Context, dir string, env map[string]string) error {
	data := filepath.Join(e.work(), "tofu", e.unit(dir))
	if err := os.MkdirAll(data, 0o755); err != nil {
		return err
	}
	env["TF_DATA_DIR"] = data
	env["TF_IN_AUTOMATION"] = "1"
	env["TF_INPUT"] = "0"
	env["TF_VAR_project"] = e.Project.Dir
	env["TF_CLI_ARGS"] = "-no-color"
	chdir := "-chdir=" + filepath.Join(e.Stack, dir)
	if err := e.init(ctx, env, chdir, "-backend=false"); err != nil {
		return err
	}
	return e.Runner.Run(ctx, toolbox.Cmd{Env: env, Args: []string{"tofu", chdir, "validate"}})
}

func (e *Engine) policyFlags(p *manifest.Policy) []string {
	policy := filepath.Join(e.Stack, p.Dir)
	flags := []string{"--no-color", "--policy", policy, "--data", filepath.Join(e.Project.Dir, project.ConfigFile)}
	if info, err := os.Stat(filepath.Join(e.Stack, p.Dir, "data")); err == nil && info.IsDir() {
		flags = append(flags, "--data", filepath.Join(policy, "data"))
	}
	return flags
}

func (e *Engine) say(format string, args ...any) {
	fmt.Fprintf(e.Out, "\n== "+format+"\n", args...)
}
