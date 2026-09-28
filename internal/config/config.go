// Package config keeps what damstack knows between runs, in
// ~/.config/damstack/config.yaml: the stacks added to it and the projects
// deployed with it.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"gopkg.in/yaml.v3"
)

// Stack is a stack damstack can deploy, by the address of its repository.
type Stack struct {
	Name    string `yaml:"name"`
	URL     string `yaml:"url"`
	Builtin bool   `yaml:"-"`
}

type Project struct {
	Name  string `yaml:"name"`
	Stack string `yaml:"stack"`
	Path  string `yaml:"path"`
}

type Config struct {
	Stacks   []Stack   `yaml:"stacks,omitempty"`
	Projects []Project `yaml:"projects,omitempty"`

	path string
}

// Builtin are the stacks every damstack knows, maintained with it.
var Builtin = []Stack{
	{Name: "hashistack", URL: "https://github.com/eugene-panin/damstack-hashistack", Builtin: true},
}

// Dir is $XDG_CONFIG_HOME/damstack, or ~/.config/damstack, on every system.
func Dir() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "damstack"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "damstack"), nil
}

// CacheDir is $XDG_CACHE_HOME/damstack, or ~/.cache/damstack.
func CacheDir() (string, error) {
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, "damstack"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "damstack"), nil
}

// Load reads the config; a missing file is an empty config.
func Load() (*Config, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	c := &Config{path: filepath.Join(dir, "config.yaml")}
	data, err := os.ReadFile(c.path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("%s: %w", c.path, err)
	}
	return c, nil
}

// Save writes the config through a temporary file, so that a failed write
// never leaves half of it.
func (c *Config) Save() error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

func (c *Config) Path() string { return c.path }

// AllStacks are the builtin stacks, then the added ones.
func (c *Config) AllStacks() []Stack {
	return append(slices.Clone(Builtin), c.Stacks...)
}

func (c *Config) Stack(name string) (Stack, bool) {
	for _, s := range c.AllStacks() {
		if s.Name == name {
			return s, true
		}
	}
	return Stack{}, false
}

// AddStack records a stack and reports whether it was new; a name taken by
// another stack is refused.
func (c *Config) AddStack(s Stack) (bool, error) {
	if existing, ok := c.Stack(s.Name); ok {
		if existing.URL == s.URL {
			return false, nil
		}
		return false, fmt.Errorf("a stack named %s is already there, from %s; add this one under another name with --name", s.Name, existing.URL)
	}
	c.Stacks = append(c.Stacks, s)
	return true, nil
}
