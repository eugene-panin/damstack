// Package project keeps a damstack project: a directory with the stack.yaml
// to edit and the encrypted vault.yml, and in .damstack what damstack
// deployed from it.
package project

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/vault"
)

const (
	ConfigFile = "stack.yaml"
	VaultFile  = "vault.yml"
	MetaFile   = ".damstack/project.yaml"
	// WorkDir holds what a run leaves for the next one to use or remove, such
	// as plans and collections; it is not kept in git.
	WorkDir     = ".damstack/work"
	KnownHosts  = ".damstack/known_hosts"
	historyFile = ".damstack/history.jsonl"
)

// StackRef is the stack a project is deployed with, at a release.
type StackRef struct {
	Name   string `yaml:"name"`
	URL    string `yaml:"url"`
	Tag    string `yaml:"tag"`
	Commit string `yaml:"commit"`
}

type Meta struct {
	Name    string     `yaml:"name"`
	Stack   StackRef   `yaml:"stack"`
	Apps    []StackRef `yaml:"apps,omitempty"`
	Created time.Time  `yaml:"created"`
}

func (m Meta) App(name string) (StackRef, bool) {
	for _, a := range m.Apps {
		if a.Name == name {
			return a, true
		}
	}
	return StackRef{}, false
}

type Project struct {
	Dir  string
	Meta Meta
}

// Find walks up from dir to the first project. It returns nil and no error
// when there is none.
func Find(dir string) (*Project, error) {
	for {
		p, err := Open(dir)
		if !errors.Is(err, fs.ErrNotExist) {
			return p, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, nil
		}
		dir = parent
	}
}

func Open(dir string) (*Project, error) {
	path := filepath.Join(dir, MetaFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p := &Project{Dir: dir}
	if err := yaml.Unmarshal(data, &p.Meta); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// Create makes the project in dir, which must not exist or be empty, with
// files by their paths in it.
func Create(dir string, meta Meta, files map[string][]byte) (*Project, error) {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	case len(entries) > 0:
		return nil, fmt.Errorf("%s is not empty; choose another directory", dir)
	}
	meta.Created = meta.Created.UTC().Truncate(time.Second)
	data, err := yaml.Marshal(meta)
	if err != nil {
		return nil, err
	}
	all := map[string][]byte{
		".gitignore": []byte("/" + WorkDir + "/\n"),
		MetaFile:     data,
	}
	for name, content := range files {
		all[name] = content
	}
	for name, content := range all {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return nil, err
		}
	}
	return &Project{Dir: dir, Meta: meta}, nil
}

// PasswordPath is where the vault password of a project is, outside the
// project, so that the project can go to git.
func PasswordPath(name string) (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "projects", name, "vault-pass"), nil
}

// NewPassword makes the vault password of a new project; an existing one is
// never replaced.
func NewPassword(name string) (string, error) {
	path, err := PasswordPath(name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	b := make([]byte, 32)
	rand.Read(b)
	password := base64.RawURLEncoding.EncodeToString(b)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("a vault password for %s is already there, at %s: %w", name, path, err)
	}
	if _, err := f.WriteString(password + "\n"); err != nil {
		f.Close()
		return "", err
	}
	return password, f.Close()
}

func (p *Project) PasswordPath() (string, error) { return PasswordPath(p.Meta.Name) }

func (p *Project) Password() (string, error) {
	path, err := p.PasswordPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("no vault password at %s: copy it there from where you keep it; the secrets of %s cannot be read without it", path, p.Meta.Name)
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Secrets reads vault.yml; a project without one has no secrets.
func (p *Project) Secrets(password string) (map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(p.Dir, VaultFile))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := vault.Decrypt(data, password)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", VaultFile, err)
	}
	secrets := map[string]any{}
	if err := yaml.Unmarshal(plain, &secrets); err != nil {
		return nil, fmt.Errorf("%s: %w", VaultFile, err)
	}
	return secrets, nil
}

func (p *Project) SaveSecrets(password string, secrets map[string]any) error {
	data, err := EncryptSecrets(password, secrets)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(p.Dir, VaultFile), data, 0o644)
}

// EncryptSecrets is the vault.yml holding secrets.
func EncryptSecrets(password string, secrets map[string]any) ([]byte, error) {
	var plain bytes.Buffer
	enc := yaml.NewEncoder(&plain)
	enc.SetIndent(2)
	if err := enc.Encode(secrets); err != nil {
		return nil, err
	}
	return vault.Encrypt(plain.Bytes(), password)
}

// Config reads stack.yaml.
func (p *Project) Config() (map[string]any, error) {
	path := filepath.Join(p.Dir, ConfigFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	config := map[string]any{}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return config, nil
}

// Entry is one line of the history: a step of a deploy, or a command.
type Entry struct {
	Time     time.Time `json:"time"`
	Command  string    `json:"command"`
	Step     string    `json:"step,omitempty"`
	Result   string    `json:"result"`
	Seconds  float64   `json:"seconds,omitempty"`
	Stack    string    `json:"stack"`
	Commit   string    `json:"commit"`
	Damstack string    `json:"damstack"`
	Error    string    `json:"error,omitempty"`
}

const (
	OK      = "ok"
	Failed  = "failed"
	Skipped = "skipped"
)

func (p *Project) Record(e Entry) error {
	e.Time = e.Time.UTC().Truncate(time.Millisecond)
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(p.Dir, historyFile), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (p *Project) History() ([]Entry, error) {
	f, err := os.Open(filepath.Join(p.Dir, historyFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var entries []Entry
	scanner := bufio.NewScanner(f)
	for n := 1; scanner.Scan(); n++ {
		var e Entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", historyFile, n, err)
		}
		entries = append(entries, e)
	}
	return entries, scanner.Err()
}

// Done reports whether a step of a deploy ever finished, here or before the
// project came to damstack: an import entry records a step done elsewhere.
func (p *Project) Done(step string) (bool, error) {
	entries, err := p.History()
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if (e.Command == "deploy" || e.Command == "import") && e.Step == step && e.Result == OK {
			return true, nil
		}
	}
	return false, nil
}

// SaveMeta records a new release of the stack.
func (p *Project) SaveMeta() error {
	data, err := yaml.Marshal(p.Meta)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(p.Dir, MetaFile), data, 0o644)
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// SetApp puts block, the YAML settings of an app, under apps.<name> of
// stack.yaml. Only the apps section is written again; the rest of the file,
// its comments and blank lines, stays as it is.
func (p *Project) SetApp(name string, block []byte) error {
	path := filepath.Join(p.Dir, ConfigFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("%s is not a mapping", path)
	}
	var app yaml.Node
	if err := yaml.Unmarshal(block, &app); err != nil {
		return fmt.Errorf("the settings of %s: %w", name, err)
	}
	if app.Kind != yaml.DocumentNode || len(app.Content) == 0 || app.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("the settings of %s are not a mapping", name)
	}
	value := app.Content[0]
	value.HeadComment = joinComments(app.HeadComment, value.HeadComment)

	lines := strings.SplitAfter(string(data), "\n")
	root := doc.Content[0]
	key, apps := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "apps"}, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	start, end := len(lines), len(lines)
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "apps" {
			continue
		}
		key, start = root.Content[i], root.Content[i].Line-1
		if v := root.Content[i+1]; v.Kind == yaml.MappingNode {
			apps = v
		}
		if i+2 < len(root.Content) {
			end = root.Content[i+2].Line - 1
			for end > start+1 && (strings.TrimSpace(lines[end-1]) == "" || strings.HasPrefix(lines[end-1], "#")) {
				end--
			}
		}
		break
	}
	apps.Style &^= yaml.FlowStyle
	replaced := false
	for i := 0; i+1 < len(apps.Content); i += 2 {
		if apps.Content[i].Value == name {
			apps.Content[i+1], replaced = value, true
		}
	}
	if !replaced {
		apps.Content = append(apps.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, value)
	}

	section := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "apps", LineComment: key.LineComment}, apps,
	}}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(section); err != nil {
		return err
	}
	head := strings.Join(lines[:start], "")
	if start == len(lines) && head != "" && !strings.HasSuffix(head, "\n\n") {
		head = strings.TrimRight(head, "\n") + "\n\n"
	}
	return writeFile(path, []byte(head+out.String()+strings.Join(lines[end:], "")), 0o644)
}

func joinComments(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n" + b
}

// List is the list of names at a dotted path of stack.yaml, such as
// network.clients.
func List(config map[string]any, path string) ([]string, error) {
	var v any = config
	for _, key := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s is not in stack.yaml", path)
		}
		v = m[key]
	}
	items, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s in stack.yaml is not a list", path)
	}
	names := make([]string, len(items))
	for i, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s in stack.yaml holds %v, not a name", path, item)
		}
		names[i] = s
	}
	return names, nil
}

// SetList writes the list of names at a dotted path of stack.yaml, keeping
// the rest of the file, its comments too, as it is.
func (p *Project) SetList(path string, names []string) error {
	file := filepath.Join(p.Dir, ConfigFile)
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return fmt.Errorf("%s is empty", file)
	}
	node := doc.Content[0]
	for _, key := range strings.Split(path, ".") {
		var next *yaml.Node
		if node.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(node.Content); i += 2 {
				if node.Content[i].Value == key {
					next = node.Content[i+1]
				}
			}
		}
		if next == nil {
			return fmt.Errorf("%s is not in stack.yaml", path)
		}
		node = next
	}
	if node.Kind != yaml.SequenceNode {
		return fmt.Errorf("%s in stack.yaml is not a list", path)
	}
	lines := strings.SplitAfter(string(data), "\n")
	if node.Style&yaml.FlowStyle != 0 {
		line := lines[node.Line-1]
		start := node.Column - 1
		end := strings.Index(line[start:], "]")
		if end < 0 {
			return fmt.Errorf("%s in stack.yaml spans lines; edit it by hand", path)
		}
		lines[node.Line-1] = line[:start] + "[" + strings.Join(names, ", ") + "]" + line[start+end+1:]
	} else {
		first, last := node.Content[0], node.Content[len(node.Content)-1]
		indent := strings.Repeat(" ", first.Column-3)
		var block []string
		for _, name := range names {
			block = append(block, indent+"- "+name+"\n")
		}
		lines = append(lines[:first.Line-1], append(block, lines[last.Line:]...)...)
	}
	return writeFile(file, []byte(strings.Join(lines, "")), 0o644)
}
