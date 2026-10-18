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

	"github.com/eugene-panin/damstack/internal/atomicfile"
	"github.com/eugene-panin/damstack/internal/manifest"
)

// Stack is a stack damstack can deploy, by the address of its repository.
type Stack struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
	// Path is the directory of the stack in the repository, empty for its
	// root.
	Path string `yaml:"path,omitempty"`
	// Kind is app for an app, domain for a domain, empty for a platform.
	Kind string `yaml:"kind,omitempty"`
	// Description says what the stack sets up, in a line; Platform is the
	// platform an app of the library runs on.
	Description string `yaml:"description,omitempty"`
	Platform    string `yaml:"platform,omitempty"`
	// Provides, Requires and Uses are the capabilities of the stack as its
	// manifest has them (provides, requires.capabilities and uses), so that a
	// plan finds providers without fetching every stack; Recommended puts it
	// first where several provide one.
	Provides    []string        `yaml:"provides,omitempty"`
	Requires    []manifest.Need `yaml:"requires,omitempty"`
	Uses        []manifest.Need `yaml:"uses,omitempty"`
	Recommended bool            `yaml:"recommended,omitempty"`
	Builtin     bool            `yaml:"-"`
}

// OnPlatform is whether the stack goes on a platform: an app or a domain.
func (s Stack) OnPlatform() bool { return manifest.OnPlatform(s.Kind) }

type Project struct {
	Name  string `yaml:"name"`
	Stack string `yaml:"stack"`
	Path  string `yaml:"path"`
}

type Config struct {
	Stacks   []Stack   `yaml:"stacks,omitempty"`
	Projects []Project `yaml:"projects,omitempty"`
	// Sources are where apps come from besides the library: owner/name of a
	// repository, an address, or an owner, all of whose damstack-* ones.
	Sources []string `yaml:"sources,omitempty"`
	// Current is the project commands work on when none is named.
	Current string `yaml:"current,omitempty"`
	// Library is the library of damstack, fetched; Builtin when not set.
	Library []Stack `yaml:"-"`

	path string
}

// RegistryURL is the repository of the stacks of the library, each in a
// directory of it.
const RegistryURL = "https://github.com/damstack-dev/damstack-registry"

// Builtin is the library built into damstack, for when the library cannot
// be fetched and was never fetched before.
var Builtin = []Stack{
	{Name: "hashi", URL: RegistryURL, Path: "platforms/hashi", Builtin: true,
		Description: "Nomad, Consul and Vault on one server, admin pages behind WireGuard"},
	{Name: "mail", URL: RegistryURL, Path: "apps/mail", Kind: "app", Platform: "hashi", Builtin: true,
		Description: "your own mail server, by Stalwart"},
	{Name: "odoo", URL: RegistryURL, Path: "apps/odoo", Kind: "app", Platform: "hashi", Builtin: true,
		Description: "Odoo ERP with its PostgreSQL and your addons"},
	{Name: "postgres", URL: RegistryURL, Path: "apps/postgres", Kind: "app", Platform: "hashi", Builtin: true,
		Description: "a shared PostgreSQL other apps use in postgres.mode external"},
	{Name: "nats", URL: RegistryURL, Path: "apps/nats", Kind: "app", Platform: "hashi", Builtin: true,
		Description: "a shared NATS (JetStream) bus other apps publish and subscribe on"},
	{Name: "seaweedfs", URL: RegistryURL, Path: "apps/seaweedfs", Kind: "app", Platform: "hashi", Builtin: true,
		Description: "a shared SeaweedFS object lake with an S3 API"},
	{Name: "loki", URL: RegistryURL, Path: "apps/loki", Kind: "app", Platform: "hashi", Builtin: true,
		Description: "a shared Loki log store"},
	{Name: "mimir", URL: RegistryURL, Path: "apps/mimir", Kind: "app", Platform: "hashi", Builtin: true,
		Description: "a shared Mimir metric store"},
	{Name: "grafana", URL: RegistryURL, Path: "apps/grafana", Kind: "app", Platform: "hashi", Builtin: true,
		Description: "Grafana for the platform, with Mimir and Loki data sources"},
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
	return atomicfile.Write(c.path, data, 0o644)
}

func (c *Config) Path() string { return c.path }

// AllStacks are the builtin stacks, then the added ones.
func (c *Config) AllStacks() []Stack {
	library := c.Library
	if library == nil {
		library = Builtin
	}
	return append(slices.Clone(library), c.Stacks...)
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
		if existing.URL == s.URL && existing.Path == s.Path {
			return false, nil
		}
		return false, fmt.Errorf("a stack named %s is already there, from %s; add this one under another name with --name", s.Name, existing.URL)
	}
	c.Stacks = append(c.Stacks, s)
	return true, nil
}

func (c *Config) Project(name string) (Project, bool) {
	for _, p := range c.Projects {
		if p.Name == name {
			return p, true
		}
	}
	return Project{}, false
}

// ProjectsDir is where new projects go: $DAMSTACK_HOME, or ~/.damstack.
func ProjectsDir() (string, error) {
	if dir := os.Getenv("DAMSTACK_HOME"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".damstack"), nil
}
