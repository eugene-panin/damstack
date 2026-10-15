// Package setup makes a new project from a stack: it asks the questions,
// renders stack.yaml, and generates or asks for the secrets.
package setup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/eugene-panin/damstack/internal/ask"
	"github.com/eugene-panin/damstack/internal/checks"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/secret"
)

type Options struct {
	Manifest *manifest.Manifest
	// Stack is the stack's directory on this machine.
	Stack string
	Ref   project.StackRef
	Name  string
	Dir   string
	// Given are answers and asked secrets by name, such as from a file; the
	// rest is asked with Prompter, or, when it is nil, taken from defaults.
	Given    map[string]any
	Prompter *ask.Prompter
	// Placeholders stand in for asked secrets that are not given, for a
	// project that only proves the stack.
	Placeholders bool
	// Password is the vault password; empty makes a new one in the damstack
	// config for the project.
	Password string
	// Review sees the answers before anything is written, and stops the set
	// up with an error.
	Review func(answers map[string]any) error
}

// Create makes the project and returns it with its vault password.
func Create(o Options) (*project.Project, string, error) {
	m := o.Manifest
	if entries, err := os.ReadDir(o.Dir); err == nil && len(entries) > 0 {
		return nil, "", fmt.Errorf("%s is not empty; choose another directory", o.Dir)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, "", err
	}

	answers, secrets, files, err := collect(m, o.Name, o.Given, o.Prompter, o.Placeholders)
	if err != nil {
		return nil, "", err
	}
	if o.Review != nil {
		if err := o.Review(answers); err != nil {
			return nil, "", err
		}
	}

	config, err := m.RenderConfig(o.Stack, o.Name, answers)
	if err != nil {
		return nil, "", err
	}
	files[project.ConfigFile] = config

	password := o.Password
	if password == "" {
		if password, err = project.NewPassword(o.Name); err != nil {
			return nil, "", err
		}
	}
	vault, err := project.EncryptSecrets(password, secrets)
	if err == nil {
		files[project.VaultFile] = vault
	}
	var p *project.Project
	if err == nil {
		p, err = project.Create(o.Dir, project.Meta{Name: o.Name, Stack: o.Ref, Created: time.Now()}, files)
	}
	if err != nil {
		if o.Password == "" {
			if path, perr := project.PasswordPath(o.Name); perr == nil {
				os.Remove(path)
			}
		}
		return nil, "", err
	}
	return p, password, nil
}

// collect asks the questions and gets the secrets of a stack: generated, or
// given, or asked. files are the certificates of generated CAs.
func collect(m *manifest.Manifest, name string, given map[string]any, p *ask.Prompter, placeholders bool) (map[string]any, map[string]any, map[string][]byte, error) {
	questions, asked := map[string]any{}, map[string]any{}
	for k, v := range given {
		if isAsked(m, k) {
			asked[k] = v
		} else {
			questions[k] = v
		}
	}
	var check ask.CheckFunc
	if !placeholders {
		check = func(specs []manifest.CheckSpec, v any, answers map[string]any) []checks.Result {
			return checks.Default().Run(context.Background(), specs, v, answers)
		}
	}
	answers, err := ask.Questions(p, m.Questions, questions, DefaultFuncs(), check)
	if err != nil {
		return nil, nil, nil, err
	}

	secrets := map[string]any{}
	files := map[string][]byte{}
	heading := false
	for _, s := range m.Secrets {
		if !manifest.Holds(s.When, answers) {
			continue
		}
		if s.Generate != "" {
			g, err := secret.Generate(s.Generate, s.Bytes, name)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("secret %s: %w", s.Name, err)
			}
			secrets[s.Name] = g.Value
			if s.Cert != "" {
				files[s.Cert] = g.Cert
			}
			continue
		}
		switch v, ok := asked[s.Name]; {
		case ok:
			text, isText := v.(string)
			if !isText || text == "" {
				return nil, nil, nil, fmt.Errorf("secret %s: must be text", s.Name)
			}
			if failed := ask.Report(p, check, s.Checks, text, answers); failed != "" {
				return nil, nil, nil, fmt.Errorf("secret %s: %s", s.Name, failed)
			}
			secrets[s.Name] = text
		case placeholders:
			secrets[s.Name] = "placeholder-" + s.Name
		case s.Optional && (p == nil || p.NoTerminal):
			// An optional key not given is left unset: the app works without it.
		case p != nil && p.NoTerminal:
			return nil, nil, nil, fmt.Errorf("secret %s: not given, and %v: put it in the file of --answers", s.Name, ask.ErrNoTerminal)
		case p != nil:
			if !heading {
				fmt.Fprintln(p.Out, "\n── Secrets, not shown as you type them")
				heading = true
			}
			prompt := s.Ask
			if s.Optional {
				prompt += " (optional: Enter leaves it unset)"
			}
			for {
				value, err := p.Secret(prompt)
				if err != nil {
					return nil, nil, nil, err
				}
				if value == "" && s.Optional {
					break
				}
				if ask.Report(p, check, s.Checks, value, answers) == "" {
					secrets[s.Name] = value
					break
				}
			}
		default:
			return nil, nil, nil, fmt.Errorf("secret %s: not given", s.Name)
		}
	}
	return answers, secrets, files, nil
}

type AppOptions struct {
	Manifest *manifest.Manifest
	// Stack is the app's directory on this machine.
	Stack        string
	Ref          project.StackRef
	Project      *project.Project
	Password     string
	Given        map[string]any
	Prompter     *ask.Prompter
	Placeholders bool
}

// AddApp adds an app to a project: its settings under apps of stack.yaml,
// its secrets in vault.yml, and its release in the project's record.
func AddApp(o AppOptions) (err error) {
	m, p := o.Manifest, o.Project
	if _, ok := p.Meta.App(m.Name); ok {
		return fmt.Errorf("%s is already an app of %s; damstack deploy deploys it", m.Name, p.Meta.Name)
	}
	existing, err := p.Secrets(o.Password)
	if err != nil {
		return err
	}
	answers, secrets, files, err := collect(m, p.Meta.Name, o.Given, o.Prompter, o.Placeholders)
	if err != nil {
		return err
	}
	for name := range secrets {
		if _, ok := existing[name]; ok {
			return fmt.Errorf("the project has a secret %s already; the app %s cannot take its name", name, m.Name)
		}
	}
	block := []byte("{}\n")
	if m.Config != "" {
		if block, err = m.RenderConfig(o.Stack, p.Meta.Name, answers); err != nil {
			return err
		}
	}
	for name := range files {
		if _, err := os.Stat(filepath.Join(p.Dir, filepath.FromSlash(name))); err == nil {
			return fmt.Errorf("%s is in the project already; the app %s cannot write it", name, m.Name)
		}
	}
	config := filepath.Join(p.Dir, project.ConfigFile)
	before, err := os.ReadFile(config)
	if err != nil {
		return err
	}
	var undo []func()
	defer func() {
		if err != nil {
			for i := len(undo) - 1; i >= 0; i-- {
				undo[i]()
			}
		}
	}()
	for name, content := range files {
		path := filepath.Join(p.Dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		undo = append(undo, func() { os.Remove(path) })
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return err
		}
	}
	kept := maps.Clone(existing)
	for name, v := range secrets {
		existing[name] = v
	}
	if err := p.SaveSecrets(o.Password, existing); err != nil {
		return err
	}
	undo = append(undo, func() { p.SaveSecrets(o.Password, kept) })
	undo = append(undo, func() { os.WriteFile(config, before, 0o644) })
	if err := p.SetApp(m.Name, block); err != nil {
		return err
	}
	apps := p.Meta.Apps
	undo = append(undo, func() { p.Meta.Apps = apps })
	p.Meta.Apps = append(slices.Clip(p.Meta.Apps), o.Ref)
	return p.SaveMeta()
}

func isAsked(m *manifest.Manifest, name string) bool {
	for _, s := range m.Secrets {
		if s.Name == name && s.Ask != "" {
			return true
		}
	}
	return false
}
