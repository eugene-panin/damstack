// Package manifest reads and checks damstack.yaml, the contract between a
// stack and damstack.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/template"

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
var Builtin = []string{"add", "app", "apply", "backup", "completion", "deploy", "doctor", "edit", "help", "history", "remove", "stack", "stacks", "status", "token", "upgrade", "use", "version"}

type Manifest struct {
	APIVersion  string `yaml:"apiVersion"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	// Needs are what the person must have before starting, and Takes about
	// how long a first deploy takes; deploy says both first.
	Needs []string `yaml:"needs"`
	Takes string   `yaml:"takes"`
	// Summary is shown before a project is set up: a label and a template
	// over the answers each.
	Summary []SummaryLine `yaml:"summary"`
	// Done is said when a deploy ends, as templates over .config, and .app
	// for an app: the addresses it serves and how to log in.
	Done []string `yaml:"done"`
	// Tokens are the secrets damstack token gives, by a short name.
	Tokens map[string]string `yaml:"tokens"`
	// Kind is platform, the default, or app: an app runs on the platform of
	// a project, and is added to it with damstack app add.
	Kind string `yaml:"kind"`
	// Provides are what a platform gives the apps on it, such as nomad.
	Provides []string `yaml:"provides"`
	// AppEnv is the environment of every step of the apps on a platform, as
	// templates over its stack.yaml and secrets: how they reach what it
	// provides, such as NOMAD_ADDR and NOMAD_TOKEN.
	AppEnv    map[string]string `yaml:"app_env"`
	Requires  Requires          `yaml:"requires"`
	Image     string            `yaml:"image"`
	Questions []Question        `yaml:"questions"`
	Secrets   []Secret          `yaml:"secrets"`
	Config    string            `yaml:"config"`
	Server    *Server           `yaml:"server"`
	Backup    *Backup           `yaml:"backup"`
	Steps     []Step            `yaml:"steps"`
	Commands  map[string]Step   `yaml:"commands"`
	// Targets are the ways an app runs, by what a platform provides: the
	// first, by name, the platform of the project provides is used.
	Targets map[string]Target `yaml:"targets"`
	Check   Check             `yaml:"check"`
}

type SummaryLine struct {
	Label string `yaml:"label"`
	Value string `yaml:"value"`
}

type Target struct {
	Steps    []Step          `yaml:"steps"`
	Commands map[string]Step `yaml:"commands"`
}

const (
	KindPlatform = "platform"
	KindApp      = "app"
)

func (m *Manifest) IsApp() bool { return m.Kind == KindApp }

// Target is the target of an app on a platform that provides provides, and
// its name.
func (m *Manifest) Target(provides []string) (string, *Target, bool) {
	for _, name := range slices.Sorted(maps.Keys(m.Targets)) {
		if slices.Contains(provides, name) {
			t := m.Targets[name]
			return name, &t, true
		}
	}
	return "", nil, false
}

// SecretPrefix begins the name of every secret of an app, so that the
// secrets of apps and of the platform in one vault.yml never meet.
func (m *Manifest) SecretPrefix() string { return strings.ReplaceAll(m.Name, "-", "_") + "_" }

type Requires struct {
	Damstack string   `yaml:"damstack"`
	Toolbox  string   `yaml:"toolbox"`
	Host     []string `yaml:"host"`
	// Provides are what an app needs of the platform, all of them.
	Provides []string `yaml:"provides"`
}

// Question is asked on the first deploy; the answers render Config into the
// stack.yaml of the project.
type Question struct {
	Name    string   `yaml:"name"`
	Prompt  string   `yaml:"prompt"`
	Type    string   `yaml:"type"`
	Default any      `yaml:"default"`
	Options []string `yaml:"options"`
	Pattern string   `yaml:"pattern"`
	// Checks run on the answer; one that fails asks again.
	Checks []CheckSpec `yaml:"checks"`
	// Section groups the questions under a heading; Note is said under it,
	// before the question.
	Section string `yaml:"section"`
	Note    string `yaml:"note"`
	// Advanced is a question not asked: its default is taken, and stack.yaml
	// changes it later.
	Advanced bool `yaml:"advanced"`
	// When is a condition on an earlier answer: the name of a bool question
	// that was answered yes, or name=value of an enum or string one. This
	// one is asked only if it holds.
	When string `yaml:"when"`
}

// Secret is generated or asked for once, and kept in the encrypted vault.yml
// of the project under its name.
type Secret struct {
	Name     string `yaml:"name"`
	Generate string `yaml:"generate"`
	Bytes    int    `yaml:"bytes"`
	Ask      string `yaml:"ask"`
	When     string `yaml:"when"`
	// Cert is where a generated ca puts its certificate in the project; the
	// key is the secret.
	Cert   string      `yaml:"cert"`
	Checks []CheckSpec `yaml:"checks"`
}

// Server is how damstack reaches the server of a project, as templates over
// its stack.yaml: before the first step it puts the SSH key on the server for
// FirstUser, who has only a password, unless OpsUser or FirstUser log in
// already. doctor dials port 22 of Tunnel, the address over the private
// network.
type Server struct {
	Address   string `yaml:"address"`
	FirstUser string `yaml:"first_user"`
	OpsUser   string `yaml:"ops_user"`
	Tunnel    string `yaml:"tunnel"`
	// TunnelHelp says how to turn the tunnel on, when a step that needs it
	// finds it off; TunnelConfigs is a pattern of the configurations of the
	// devices in the project, shown then, the first to import, the others as
	// QR codes.
	TunnelHelp    string `yaml:"tunnel_help"`
	TunnelConfigs string `yaml:"tunnel_configs"`
}

// Backup is where the server keeps its restic backups, and where damstack
// pulls them on this machine, as templates over stack.yaml. From is a path on
// the server, reached over SFTP as server.ops_user through server.tunnel; a
// stack without a server has it in the project. To is a path on this machine,
// under the home directory with ~/, or in the project. Keep is how many
// snapshots stay after a pull, by restic's names: last, hourly, daily,
// weekly, monthly, yearly. Every is how often a pull runs by itself, such as
// 1h or 30m, or off.
type Backup struct {
	From     string            `yaml:"from"`
	To       string            `yaml:"to"`
	Password string            `yaml:"password"`
	Keep     map[string]string `yaml:"keep"`
	Every    string            `yaml:"every"`
}

// KeepNames are the counts of restic forget a backup may keep.
var KeepNames = []string{"last", "hourly", "daily", "weekly", "monthly", "yearly"}

// CheckSpec names a check of an answer, with an argument for some:
// ssh-port, or {cloudflare-token: dns_zones}.
type CheckSpec struct {
	Name string
	Arg  string
}

// Checks are the checks of answers damstack knows.
var Checks = []string{"cloudflare-token", "ssh-port"}

func (c *CheckSpec) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		c.Name = n.Value
		return nil
	case yaml.MappingNode:
		if len(n.Content) == 2 {
			c.Name, c.Arg = n.Content[0].Value, n.Content[1].Value
			return nil
		}
	}
	return fmt.Errorf("line %d: a check is a name, or a name with its argument", n.Line)
}

// Generators are the ways a secret can be generated.
var Generators = []string{"base64", "ca", "hex", "password", "uuid"}

// Step runs one tool: exactly one of Ansible, Tofu and Run.
type Step struct {
	Name string `yaml:"name"`
	// Title says what the step does, for people, and Takes about how long.
	Title   string            `yaml:"title"`
	Takes   string            `yaml:"takes"`
	Ansible *Ansible          `yaml:"ansible"`
	Tofu    *Tofu             `yaml:"tofu"`
	Run     []string          `yaml:"run"`
	Env     map[string]string `yaml:"env"`
	Keep    *Keep             `yaml:"keep"`
	Confirm bool              `yaml:"confirm"`
	Once    bool              `yaml:"once"`
	// Tunnel is whether the step reaches the server over the private
	// network; damstack checks that server.tunnel answers first.
	Tunnel bool `yaml:"tunnel"`
	// AfterApps runs a step of a platform after the steps of its apps, such
	// as the one that publishes the DNS records of all of them.
	AfterApps bool `yaml:"after_apps"`
}

type Ansible struct {
	Playbook     string `yaml:"playbook"`
	Inventory    string `yaml:"inventory"`
	Requirements string `yaml:"requirements"`
}

type Tofu struct {
	Dir    string  `yaml:"dir"`
	Action string  `yaml:"action"`
	Policy *Policy `yaml:"policy"`
	// Outputs writes outputs, as JSON, to files of the project after an
	// apply: output name to path.
	Outputs map[string]string `yaml:"outputs"`
}

// Actions are what a tofu step does.
var Actions = []string{"apply", "output", "plan"}

type Policy struct {
	Dir       string `yaml:"dir"`
	NomadJobs bool   `yaml:"nomad_jobs"`
}

// Keep moves values a step left in a JSON file of the project into secrets,
// then removes the file.
type Keep struct {
	File    string            `yaml:"file"`
	Secrets map[string]string `yaml:"secrets"`
}

// Check is how damstack proves the stack without a server: a project set up
// from Answers, then its playbooks, OpenTofu directories and policies
// checked, then Steps run on it.
type Check struct {
	Answers string `yaml:"answers"`
	// Platform is the stack.yaml of a platform an app is checked on.
	Platform string `yaml:"platform"`
	Steps    []Step `yaml:"steps"`
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
	capRe      = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)
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

	c := checker{root: &root, server: m.Server, app: m.Kind == KindApp}
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
	server   *Server
	app      bool
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
	for i, line := range m.Done {
		c.checkTemplate(fmt.Sprintf("done[%d]", i), line)
	}
	if m.Server != nil && m.Server.TunnelConfigs != "" {
		if _, err := filepath.Match(m.Server.TunnelConfigs, ""); err != nil || !filepath.IsLocal(m.Server.TunnelConfigs) {
			c.add("server.tunnel_configs", "is a pattern of paths inside the project, such as clients/*.conf")
		}
	}
	for i, line := range m.Summary {
		if line.Label == "" {
			c.add(fmt.Sprintf("summary[%d].label", i), "is required")
		}
		c.checkTemplate(fmt.Sprintf("summary[%d].value", i), line.Value)
	}

	c.checkSecrets(m, dir)
	if m.Config != "" {
		c.checkFile("config", m.Config, dir, false)
		if text, err := os.ReadFile(filepath.Join(dir, m.Config)); dir != "" && filepath.IsLocal(m.Config) && err == nil {
			c.checkTemplate("config", string(text))
		}
	} else if len(m.Questions) > 0 {
		c.add("config", "is required with questions: the template the answers render into stack.yaml")
	}

	if m.Server != nil {
		if m.Server.Address == "" {
			c.add("server.address", "is required")
		}
		if m.Server.FirstUser == "" {
			c.add("server.first_user", "is required")
		}
		for field, text := range map[string]string{"address": m.Server.Address, "first_user": m.Server.FirstUser,
			"ops_user": m.Server.OpsUser, "tunnel": m.Server.Tunnel, "tunnel_help": m.Server.TunnelHelp} {
			c.checkTemplate("server."+field, text)
		}
	}

	if b := m.Backup; b != nil {
		for field, text := range map[string]string{"from": b.From, "to": b.To, "password": b.Password} {
			if text == "" {
				c.add("backup."+field, "is required")
			}
		}
		for field, text := range map[string]string{"from": b.From, "to": b.To, "password": b.Password, "every": b.Every} {
			c.checkTemplate("backup."+field, text)
		}
		for _, name := range slices.Sorted(maps.Keys(b.Keep)) {
			if !slices.Contains(KeepNames, name) {
				c.add("backup.keep."+name, "is not a count restic keeps; one of %s", strings.Join(KeepNames, ", "))
			}
			c.checkTemplate("backup.keep."+name, b.Keep[name])
		}
	}

	for i, p := range m.Provides {
		if !capRe.MatchString(p) {
			c.add(fmt.Sprintf("provides[%d]", i), "must be lowercase letters, digits and hyphens")
		}
	}
	switch m.Kind {
	case "", KindPlatform:
		if len(m.Steps) == 0 {
			c.add("steps", "at least one step is required")
		}
		c.checkSteps("steps", m.Steps, dir, false)
		c.checkCommands("commands", m.Commands, dir)
		if len(m.Targets) > 0 {
			c.add("targets", "are for an app; a platform has steps")
		}
		if len(m.Requires.Provides) > 0 {
			c.add("requires.provides", "is for an app, what it needs of the platform")
		}
		for _, key := range slices.Sorted(maps.Keys(m.AppEnv)) {
			c.checkTemplate("app_env."+key, m.AppEnv[key])
		}
	case KindApp:
		if len(m.Steps) > 0 || len(m.Commands) > 0 {
			c.add("steps", "an app has its steps and commands under targets, one per kind of platform")
		}
		if len(m.Provides) > 0 || len(m.AppEnv) > 0 {
			c.add("provides", "provides and app_env are for a platform")
		}
		if m.Server != nil {
			c.add("server", "is the platform's; an app reaches the server through it")
		}
		if m.Backup != nil {
			c.add("backup", "is the platform's; it backs up the apps on it")
		}
		if len(m.Requires.Provides) == 0 {
			c.add("requires.provides", "is required: what the app needs of the platform, such as nomad")
		}
		if len(m.Targets) == 0 {
			c.add("targets", "at least one is required: how the app runs on a platform that provides its name")
		}
		for _, name := range slices.Sorted(maps.Keys(m.Targets)) {
			path := "targets." + name
			if !capRe.MatchString(name) {
				c.add(path, "the name must be lowercase letters, digits and hyphens, what a platform provides")
			}
			t := m.Targets[name]
			if len(t.Steps) == 0 {
				c.add(path+".steps", "at least one step is required")
			}
			c.checkSteps(path+".steps", t.Steps, dir, true)
			c.checkCommands(path+".commands", t.Commands, dir)
		}
		for i, s := range m.Secrets {
			if !strings.HasPrefix(s.Name, m.SecretPrefix()) {
				c.add(fmt.Sprintf("secrets[%d].name", i), "the secrets of an app begin with its name: %s", m.SecretPrefix())
			}
		}
		if m.Check.Platform == "" {
			c.add("check.platform", "is required: the stack.yaml of a platform the app is checked on")
		} else {
			c.checkFile("check.platform", m.Check.Platform, dir, false)
		}
	default:
		c.add("kind", "%q is not a kind; platform or app", m.Kind)
	}

	if m.Check.Answers == "" {
		c.add("check.answers", "is required: answers damstack sets up a test project from, to prove the stack without a server")
	} else {
		c.checkFile("check.answers", m.Check.Answers, dir, false)
	}
	for i, s := range m.Check.Steps {
		c.checkStep(fmt.Sprintf("check.steps[%d]", i), s, dir)
	}
}

func (c *checker) checkSteps(path string, steps []Step, dir string, app bool) {
	seen := map[string]bool{}
	for i, s := range steps {
		p := fmt.Sprintf("%s[%d]", path, i)
		if !nameRe.MatchString(s.Name) {
			c.add(p+".name", "must be 2 to 31 lowercase letters, digits and hyphens, starting with a letter")
		}
		if seen[s.Name] {
			c.add(p+".name", "%q is already a step", s.Name)
		}
		seen[s.Name] = true
		if app && s.AfterApps {
			c.add(p+".after_apps", "is for a step of a platform")
		}
		c.checkStep(p, s, dir)
	}
}

func (c *checker) checkCommands(path string, commands map[string]Step, dir string) {
	for _, name := range sortedKeys(commands) {
		p := path + "." + name
		if !nameRe.MatchString(name) {
			c.add(p, "the name must be 2 to 31 lowercase letters, digits and hyphens, starting with a letter")
		}
		if slices.Contains(Builtin, name) {
			c.add(p, "%q is a command of damstack itself", name)
		}
		c.checkStep(p, commands[name], dir)
	}
}

func (c *checker) checkStep(path string, s Step, dir string) {
	if s.Tunnel && !c.app && (c.server == nil || c.server.Tunnel == "") {
		c.add(path+".tunnel", "needs server.tunnel, the address of the server over the private network")
	}
	kinds := 0
	if s.Ansible != nil {
		kinds++
		if s.Ansible.Playbook == "" {
			c.add(path+".ansible.playbook", "is required")
		} else {
			c.checkFile(path+".ansible.playbook", s.Ansible.Playbook, dir, false)
		}
		if s.Ansible.Inventory != "" {
			c.checkFile(path+".ansible.inventory", s.Ansible.Inventory, dir, true)
		}
		if s.Ansible.Requirements != "" {
			c.checkFile(path+".ansible.requirements", s.Ansible.Requirements, dir, false)
		}
	}
	if s.Tofu != nil {
		kinds++
		if s.Tofu.Dir == "" {
			c.add(path+".tofu.dir", "is required")
		} else {
			c.checkFile(path+".tofu.dir", s.Tofu.Dir, dir, true)
		}
		if !slices.Contains(Actions, s.Tofu.Action) {
			c.add(path+".tofu.action", "%q is not an action; one of %s", s.Tofu.Action, strings.Join(Actions, ", "))
		}
		if s.Tofu.Policy != nil {
			c.checkFile(path+".tofu.policy.dir", s.Tofu.Policy.Dir, dir, true)
		}
		for _, name := range slices.Sorted(maps.Keys(s.Tofu.Outputs)) {
			if file := s.Tofu.Outputs[name]; file == "" || !filepath.IsLocal(file) {
				c.add(path+".tofu.outputs."+name, "is a path inside the project")
			}
		}
	}
	if s.Run != nil {
		kinds++
		c.checkRun(path+".run", s.Run, dir)
	}
	if kinds != 1 {
		c.add(path, "a step runs exactly one of ansible, tofu and run")
	}
	for _, key := range slices.Sorted(maps.Keys(s.Env)) {
		c.checkTemplate(path+".env."+key, s.Env[key])
	}
	if s.Keep != nil {
		if s.Keep.File == "" || !filepath.IsLocal(s.Keep.File) {
			c.add(path+".keep.file", "is required, a path inside the project")
		}
		if len(s.Keep.Secrets) == 0 {
			c.add(path+".keep.secrets", "names at least one secret")
		}
		for name := range s.Keep.Secrets {
			if !questionRe.MatchString(name) {
				c.add(path+".keep.secrets", "%q is not a secret name", name)
			}
		}
	}
}

func (c *checker) checkSecrets(m *Manifest, dir string) {
	bools := map[string]bool{}
	for _, q := range m.Questions {
		bools[q.Name] = q.Type == "bool"
	}
	seen := map[string]bool{}
	for i, s := range m.Secrets {
		path := fmt.Sprintf("secrets[%d]", i)
		if !questionRe.MatchString(s.Name) {
			c.add(path+".name", "must be lowercase letters, digits and underscores, starting with a letter")
		}
		if seen[s.Name] {
			c.add(path+".name", "%q is already a secret", s.Name)
		}
		if _, ok := bools[s.Name]; ok {
			c.add(path+".name", "%q is already a question", s.Name)
		}
		seen[s.Name] = true
		switch {
		case (s.Generate == "") == (s.Ask == ""):
			c.add(path, "a secret is either generated or asked for: exactly one of generate and ask")
		case s.Generate != "" && !slices.Contains(Generators, s.Generate):
			c.add(path+".generate", "%q is not a generator; one of %s", s.Generate, strings.Join(Generators, ", "))
		}
		if s.Bytes < 0 || s.Bytes > 1024 {
			c.add(path+".bytes", "must be between 1 and 1024")
		}
		if s.Cert != "" && (s.Generate != "ca" || !filepath.IsLocal(s.Cert)) {
			c.add(path+".cert", "is for a generated ca only, a path inside the project")
		}
		if s.When != "" {
			c.checkWhen(path+".when", s.When, m.Questions)
		}
		for _, check := range s.Checks {
			if !slices.Contains(Checks, check.Name) {
				c.add(path+".checks", "%q is not a check; one of %s", check.Name, strings.Join(Checks, ", "))
			}
		}
	}
}

// checkWhen checks a condition on the answers to earlier questions.
func (c *checker) checkWhen(path, when string, earlier []Question) {
	name, value, eq := strings.Cut(when, "=")
	i := slices.IndexFunc(earlier, func(q Question) bool { return q.Name == name })
	if i < 0 {
		c.add(path, "%q is not a question asked before this", name)
		return
	}
	q := earlier[i]
	switch {
	case !eq && q.Type != "bool":
		c.add(path, "%q is not a bool question; write %s=<answer> for another", name, name)
	case eq && q.Type == "enum" && !slices.Contains(q.Options, value):
		c.add(path, "%q is not an option of %s", value, name)
	case eq && q.Type != "enum" && q.Type != "" && q.Type != "string":
		c.add(path, "%s=... is for an enum or string question", name)
	}
}

// Holds reports whether when holds for the answers: a bool answer is yes, or
// name=value an answer is value. An empty when always holds.
func Holds(when string, answers map[string]any) bool {
	if when == "" {
		return true
	}
	name, value, eq := strings.Cut(when, "=")
	if !eq {
		return answers[name] == true
	}
	return answers[name] == value
}

func (c *checker) checkTemplate(path, text string) {
	if _, err := template.New(path).Option("missingkey=error").Funcs(TemplateFuncs).Parse(text); err != nil {
		c.add(path, "%v", err)
	}
}

// checkFile checks that a path of the manifest is inside the stack and there,
// a directory when dirWanted.
func (c *checker) checkFile(path, name, dir string, dirWanted bool) {
	if !filepath.IsLocal(name) {
		c.add(path, "%s is not a path inside the stack", name)
		return
	}
	if dir == "" {
		return
	}
	info, err := os.Stat(filepath.Join(dir, name))
	switch {
	case err != nil:
		c.add(path, "%s does not exist in the stack", name)
	case dirWanted && !info.IsDir():
		c.add(path, "%s is not a directory", name)
	case !dirWanted && info.IsDir():
		c.add(path, "%s is a directory, not a file", name)
	}
}

func (c *checker) checkQuestions(questions []Question) {
	seen := map[string]bool{}
	for i, q := range questions {
		path := fmt.Sprintf("questions[%d]", i)
		if q.When != "" {
			c.checkWhen(path+".when", q.When, questions[:i])
		}
		if !questionRe.MatchString(q.Name) {
			c.add(path+".name", "must be lowercase letters, digits and underscores, starting with a letter")
		}
		if q.Name == "project" {
			c.add(path+".name", "project is the name of the project, which damstack asks itself")
		}
		if seen[q.Name] {
			c.add(path+".name", "%q is already a question", q.Name)
		}
		seen[q.Name] = true
		if strings.TrimSpace(q.Prompt) == "" {
			c.add(path+".prompt", "is required")
		}
		for _, check := range q.Checks {
			if !slices.Contains(Checks, check.Name) {
				c.add(path+".checks", "%q is not a check; one of %s", check.Name, strings.Join(Checks, ", "))
			}
		}
		if q.Advanced && q.Default == nil && q.Type != "bool" {
			c.add(path+".advanced", "an advanced question is not asked, so it needs a default")
		}
		if text, ok := q.Default.(string); ok && strings.Contains(text, "{{") {
			c.checkTemplate(path+".default", text)
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
		keyNode, next := child(node, key)
		if next == nil {
			return line
		}
		line, node = keyNode.Line, next
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

// child finds the key of a mapping and its value.
func child(node *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	if node.Kind != yaml.MappingNode {
		return nil, nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i], node.Content[i+1]
		}
	}
	return nil, nil
}

func sortedKeys(m map[string]Step) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
