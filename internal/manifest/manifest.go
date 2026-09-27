// Package manifest reads and checks damstack.yaml, the contract between a
// stack and damstack.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

const (
	File       = "damstack.yaml"
	APIVersion = "damstack/v1"
)

// HostChecks are the checks of the machine a stack may require; each is one
// part of damstack doctor.
var HostChecks = []string{"docker", "ssh-key", "wireguard"}

// Builtin are the commands of damstack itself; a stack command may not take
// their names.
var Builtin = []string{"add", "apply", "deploy", "doctor", "help", "remove", "stack", "stacks", "status", "upgrade", "version"}

type Manifest struct {
	APIVersion  string              `yaml:"apiVersion"`
	Name        string              `yaml:"name"`
	Description string              `yaml:"description"`
	Requires    Requires            `yaml:"requires"`
	Image       string              `yaml:"image"`
	Questions   []Question          `yaml:"questions"`
	Steps       []Step              `yaml:"steps"`
	Commands    map[string][]string `yaml:"commands"`
	Check       []string            `yaml:"check"`
}

type Requires struct {
	Damstack string   `yaml:"damstack"`
	Toolbox  string   `yaml:"toolbox"`
	Host     []string `yaml:"host"`
}

type Question struct {
	Name    string   `yaml:"name"`
	Prompt  string   `yaml:"prompt"`
	Type    string   `yaml:"type"`
	Default any      `yaml:"default"`
	Options []string `yaml:"options"`
	Pattern string   `yaml:"pattern"`
}

type Step struct {
	Name    string   `yaml:"name"`
	Run     []string `yaml:"run"`
	Confirm bool     `yaml:"confirm"`
	Once    bool     `yaml:"once"`
}

// Problem is one thing wrong with a manifest, at a line of it when known.
type Problem struct {
	Line int
	Path string
	Msg  string
}

func (p Problem) String() string {
	if p.Line > 0 {
		return fmt.Sprintf("line %d: %s: %s", p.Line, p.Path, p.Msg)
	}
	return p.Path + ": " + p.Msg
}

// Error lists every problem found, so that one run of lint shows them all.
type Error struct {
	File     string
	Problems []Problem
}

func (e *Error) Error() string {
	lines := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		lines[i] = "  " + p.String()
	}
	return fmt.Sprintf("%s is not a valid damstack stack:\n%s", e.File, strings.Join(lines, "\n"))
}

var (
	nameRe     = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
	questionRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)
	apiRe      = regexp.MustCompile(`^damstack/v([0-9]+)$`)
)

// Load reads the manifest of the stack in dir and checks it, including that
// the files its commands name exist in dir. damstackVersion is the running
// damstack, checked against requires.damstack unless it is not a release.
func Load(dir, damstackVersion string) (*Manifest, error) {
	path := filepath.Join(dir, File)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(path, data, dir, damstackVersion)
}

// Parse checks data as the manifest at path; dir, when not empty, is the
// stack directory the commands are looked up in.
func Parse(path string, data []byte, dir, damstackVersion string) (*Manifest, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, &Error{File: path, Problems: []Problem{{Path: "yaml", Msg: err.Error()}}}
	}
	var m Manifest
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		var typeErr *yaml.TypeError
		if errors.As(err, &typeErr) {
			problems := make([]Problem, len(typeErr.Errors))
			for i, msg := range typeErr.Errors {
				problems[i] = decodeProblem(msg)
			}
			return nil, &Error{File: path, Problems: problems}
		}
		return nil, &Error{File: path, Problems: []Problem{{Path: "yaml", Msg: err.Error()}}}
	}

	c := checker{root: &root}
	c.check(&m, dir, damstackVersion)
	if len(c.problems) > 0 {
		return nil, &Error{File: path, Problems: c.problems}
	}
	return &m, nil
}

var (
	lineRe    = regexp.MustCompile(`^line ([0-9]+): (.*)$`)
	unknownRe = regexp.MustCompile(`^field (\S+) not found in type \S+$`)
)

func decodeProblem(msg string) Problem {
	p := Problem{Path: "yaml", Msg: msg}
	if m := lineRe.FindStringSubmatch(msg); m != nil {
		p.Line, _ = strconv.Atoi(m[1])
		p.Msg = m[2]
	}
	if m := unknownRe.FindStringSubmatch(p.Msg); m != nil {
		p.Msg = "unknown field " + m[1]
	}
	return p
}

type checker struct {
	root     *yaml.Node
	problems []Problem
}

func (c *checker) add(path, format string, args ...any) {
	c.problems = append(c.problems, Problem{Line: lineOf(c.root, path), Path: path, Msg: fmt.Sprintf(format, args...)})
}

func (c *checker) check(m *Manifest, dir, damstackVersion string) {
	switch match := apiRe.FindStringSubmatch(m.APIVersion); {
	case m.APIVersion == APIVersion:
	case match != nil && match[1] != "1":
		c.add("apiVersion", "%s is newer than this damstack knows (%s); update damstack", m.APIVersion, APIVersion)
	default:
		c.add("apiVersion", "must be %s", APIVersion)
	}

	if !nameRe.MatchString(m.Name) {
		c.add("name", "must be 2 to 31 lowercase letters, digits and hyphens, starting with a letter")
	}
	if strings.TrimSpace(m.Description) == "" {
		c.add("description", "is required")
	}

	if m.Requires.Damstack != "" {
		if ok, err := Satisfies(damstackVersion, m.Requires.Damstack); err != nil {
			c.add("requires.damstack", "%v", err)
		} else if !ok {
			c.add("requires.damstack", "the stack needs damstack %s, this is %s; update damstack", m.Requires.Damstack, damstackVersion)
		}
	}
	if m.Requires.Toolbox != "" {
		if _, err := parseConstraint(m.Requires.Toolbox); err != nil {
			c.add("requires.toolbox", "%v", err)
		}
	}
	for i, h := range m.Requires.Host {
		if !slices.Contains(HostChecks, h) {
			c.add(fmt.Sprintf("requires.host[%d]", i), "%q is not a check damstack knows; one of %s", h, strings.Join(HostChecks, ", "))
		}
	}

	c.checkQuestions(m.Questions)

	if len(m.Steps) == 0 {
		c.add("steps", "at least one step is required")
	}
	seen := map[string]bool{}
	for i, s := range m.Steps {
		path := fmt.Sprintf("steps[%d]", i)
		if !nameRe.MatchString(s.Name) {
			c.add(path+".name", "must be 2 to 31 lowercase letters, digits and hyphens, starting with a letter")
		}
		if seen[s.Name] {
			c.add(path+".name", "%q is already a step", s.Name)
		}
		seen[s.Name] = true
		c.checkRun(path+".run", s.Run, dir)
	}

	for _, name := range sortedKeys(m.Commands) {
		path := "commands." + name
		if !nameRe.MatchString(name) {
			c.add(path, "the name must be 2 to 31 lowercase letters, digits and hyphens, starting with a letter")
		}
		if slices.Contains(Builtin, name) {
			c.add(path, "%q is a command of damstack itself", name)
		}
		c.checkRun(path, m.Commands[name], dir)
	}

	if len(m.Check) == 0 {
		c.add("check", "is required: a command that proves the stack works without a server")
	} else {
		c.checkRun("check", m.Check, dir)
	}
}

func (c *checker) checkQuestions(questions []Question) {
	seen := map[string]bool{}
	for i, q := range questions {
		path := fmt.Sprintf("questions[%d]", i)
		if !questionRe.MatchString(q.Name) {
			c.add(path+".name", "must be lowercase letters, digits and underscores, starting with a letter")
		}
		if seen[q.Name] {
			c.add(path+".name", "%q is already a question", q.Name)
		}
		seen[q.Name] = true
		if strings.TrimSpace(q.Prompt) == "" {
			c.add(path+".prompt", "is required")
		}
		switch q.Type {
		case "", "string":
			if q.Pattern != "" {
				if _, err := regexp.Compile(q.Pattern); err != nil {
					c.add(path+".pattern", "%v", err)
				}
			}
			if q.Default != nil {
				if _, ok := q.Default.(string); !ok {
					c.add(path+".default", "must be a string")
				}
			}
		case "bool":
			if q.Default != nil {
				if _, ok := q.Default.(bool); !ok {
					c.add(path+".default", "must be true or false")
				}
			}
		case "list":
			if q.Default != nil {
				if _, ok := q.Default.([]any); !ok {
					c.add(path+".default", "must be a list")
				}
			}
		case "enum":
			if len(q.Options) == 0 {
				c.add(path+".options", "an enum needs options")
			}
			if d, ok := q.Default.(string); q.Default != nil && (!ok || !slices.Contains(q.Options, d)) {
				c.add(path+".default", "must be one of the options")
			}
		default:
			c.add(path+".type", "%q is not a type; one of string, bool, list, enum", q.Type)
		}
	}
}

// checkRun checks a command: not empty, and a program given as a path inside
// the stack exists there.
func (c *checker) checkRun(path string, run []string, dir string) {
	if len(run) == 0 || strings.TrimSpace(run[0]) == "" {
		c.add(path, "the command is empty")
		return
	}
	program := run[0]
	if !strings.Contains(program, "/") || dir == "" {
		return
	}
	if filepath.IsAbs(program) {
		c.add(path, "%s is an absolute path; name the program, or a path inside the stack", program)
		return
	}
	if !filepath.IsLocal(program) {
		c.add(path, "%s leaves the stack directory", program)
		return
	}
	info, err := os.Stat(filepath.Join(dir, program))
	switch {
	case err != nil:
		c.add(path, "%s does not exist in the stack", program)
	case info.Mode()&0o111 == 0:
		c.add(path, "%s is not executable", program)
	}
}

// Satisfies reports whether version meets constraint, a comma-separated list
// of >=, >, <=, <, = and a semantic version, such as ">=0.3.0, <1.0.0". A
// version that is not a release, such as dev, satisfies any constraint.
func Satisfies(version, constraint string) (bool, error) {
	parts, err := parseConstraint(constraint)
	if err != nil {
		return false, err
	}
	v := "v" + strings.TrimPrefix(version, "v")
	if !semver.IsValid(v) {
		return true, nil
	}
	for _, p := range parts {
		cmp := semver.Compare(v, p.version)
		ok := map[string]bool{">=": cmp >= 0, ">": cmp > 0, "<=": cmp <= 0, "<": cmp < 0, "=": cmp == 0}[p.op]
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

type bound struct{ op, version string }

func parseConstraint(constraint string) ([]bound, error) {
	var parts []bound
	for _, raw := range strings.Split(constraint, ",") {
		raw = strings.TrimSpace(raw)
		op := ""
		for _, candidate := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(raw, candidate) {
				op = candidate
				break
			}
		}
		version := "v" + strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(raw, op)), "v")
		if op == "" || !semver.IsValid(version) {
			return nil, fmt.Errorf("%q is not a version constraint such as >=0.3.0", raw)
		}
		parts = append(parts, bound{op, version})
	}
	return parts, nil
}

// lineOf finds the line of a path such as steps[2].run in the document; 0
// when it is not there.
func lineOf(root *yaml.Node, path string) int {
	node := root
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	line := node.Line
	for _, part := range strings.Split(path, ".") {
		key, index := part, -1
		if open := strings.Index(part, "["); open >= 0 && strings.HasSuffix(part, "]") {
			key = part[:open]
			index, _ = strconv.Atoi(part[open+1 : len(part)-1])
		}
		next := child(node, key)
		if next == nil {
			return line
		}
		line, node = next.Line, next
		if index >= 0 {
			if node.Kind != yaml.SequenceNode || index >= len(node.Content) {
				return line
			}
			node = node.Content[index]
			line = node.Line
		}
	}
	return line
}

func child(node *yaml.Node, key string) *yaml.Node {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
