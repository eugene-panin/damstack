// Package setup makes a new project from a stack: it asks the questions,
// renders stack.yaml, and generates or asks for the secrets.
package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/eugene-panin/damstack/internal/ask"
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
}

// Create makes the project and returns it with its vault password.
func Create(o Options) (*project.Project, string, error) {
	m := o.Manifest
	if entries, err := os.ReadDir(o.Dir); err == nil && len(entries) > 0 {
		return nil, "", fmt.Errorf("%s is not empty; choose another directory", o.Dir)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, "", err
	}

	questions, asked := map[string]any{}, map[string]any{}
	for k, v := range o.Given {
		if isAsked(m, k) {
			asked[k] = v
		} else {
			questions[k] = v
		}
	}
	answers, err := ask.Questions(o.Prompter, m.Questions, questions)
	if err != nil {
		return nil, "", err
	}

	secrets := map[string]any{}
	files := map[string][]byte{}
	for _, s := range m.Secrets {
		if s.When != "" && answers[s.When] != true {
			continue
		}
		if s.Generate != "" {
			g, err := secret.Generate(s.Generate, s.Bytes, o.Name)
			if err != nil {
				return nil, "", fmt.Errorf("secret %s: %w", s.Name, err)
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
				return nil, "", fmt.Errorf("secret %s: must be text", s.Name)
			}
			secrets[s.Name] = text
		case o.Placeholders:
			secrets[s.Name] = "placeholder-" + s.Name
		case o.Prompter != nil:
			if secrets[s.Name], err = o.Prompter.Secret(s.Ask); err != nil {
				return nil, "", err
			}
		default:
			return nil, "", fmt.Errorf("secret %s: not given", s.Name)
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

func isAsked(m *manifest.Manifest, name string) bool {
	for _, s := range m.Secrets {
		if s.Name == name && s.Ask != "" {
			return true
		}
	}
	return false
}
