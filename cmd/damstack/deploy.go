package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/eugene-panin/damstack/internal/ask"
	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/doctor"
	"github.com/eugene-panin/damstack/internal/engine"
	"github.com/eugene-panin/damstack/internal/login"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/release"
	"github.com/eugene-panin/damstack/internal/setup"
	"github.com/eugene-panin/damstack/internal/stack"
	"github.com/eugene-panin/damstack/internal/toolbox"
)

type streams struct {
	in     io.Reader
	out    io.Writer
	err    io.Writer
	prompt *ask.Prompter
}

func newStreams(stdin io.Reader, stdout, stderr io.Writer) *streams {
	p := ask.NewPrompter(stdin, stdout)
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		p.Hidden = func() (string, error) {
			b, err := term.ReadPassword(int(f.Fd()))
			return string(b), err
		}
	}
	return &streams{in: stdin, out: stdout, err: stderr, prompt: p}
}

func (s *streams) tty() bool {
	in, ok := s.in.(*os.File)
	out, ok2 := s.out.(*os.File)
	return ok && ok2 && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
}

type deployOptions struct {
	stack   string
	name    string
	dir     string
	answers string
	from    string
}

func deployCommand(s *streams) *cobra.Command {
	var o deployOptions
	cmd := &cobra.Command{
		Use:   "deploy [stack]",
		Short: "Set up a project from a stack and deploy it; in a project, deploy it again",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				o.stack = args[0]
			}
			return deploy(cmd.Context(), s, o)
		},
	}
	cmd.Flags().StringVar(&o.name, "name", "", "the name of the new project")
	cmd.Flags().StringVar(&o.dir, "dir", "", "where to put the new project, instead of ~/damstack/<name>")
	cmd.Flags().StringVar(&o.answers, "answers", "", "a YAML file with answers to the questions, and the secrets the stack asks for")
	cmd.Flags().StringVar(&o.from, "from", "", "deploy the stack in this directory as it is, instead of a release; for writing a stack")
	return cmd
}

var projectNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)

func deploy(ctx context.Context, s *streams, o deployOptions) error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	p, err := project.Find(wd)
	if err != nil {
		return err
	}
	if p != nil {
		if o.stack != "" || o.name != "" || o.dir != "" || o.answers != "" {
			return fmt.Errorf("you are in the project %s, at %s, and damstack deploy deploys it again; "+
				"to set up another project, run damstack deploy outside it", p.Meta.Name, p.Dir)
		}
		m, dir, err := projectStack(ctx, p, o.from)
		if err != nil {
			return err
		}
		return runSteps(ctx, s, p, m, dir)
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	m, dir, ref, err := chooseStack(ctx, s, cfg, o)
	if err != nil {
		return err
	}
	given := map[string]any{}
	if o.answers != "" {
		data, err := os.ReadFile(o.answers)
		if err != nil {
			return err
		}
		if err := yaml.Unmarshal(data, &given); err != nil {
			return fmt.Errorf("%s: %w", o.answers, err)
		}
	}
	name := o.name
	if v, ok := given["project"].(string); ok && name == "" {
		name = v
	}
	delete(given, "project")
	for name == "" || !projectNameRe.MatchString(name) || taken(cfg, name) {
		if name != "" {
			fmt.Fprintf(s.out, "  %s cannot be the name: 2 to 31 lowercase letters, digits and hyphens, not a project already\n", name)
		}
		if name, err = s.prompt.Line(fmt.Sprintf("A name for this project, such as my-%s: ", m.Name)); err != nil {
			return err
		}
	}
	path := o.dir
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path = filepath.Join(home, "damstack", name)
	}
	if path, err = filepath.Abs(path); err != nil {
		return err
	}

	p, _, err = setup.Create(setup.Options{Manifest: m, Stack: dir, Ref: ref, Name: name, Dir: path, Given: given, Prompter: s.prompt})
	if err != nil {
		return err
	}
	cfg.Projects = append(cfg.Projects, config.Project{Name: name, Stack: ref.Name, Path: path})
	if err := cfg.Save(); err != nil {
		return err
	}
	pass, _ := p.PasswordPath()
	fmt.Fprintf(s.out, "\nSet up %s in %s.\n  stack.yaml is the one file to edit; vault.yml holds the secrets, encrypted.\n"+
		"  The vault password is %s: keep a copy somewhere safe, such as a password manager.\n\n", name, path, pass)
	ok, err := s.prompt.Confirm("Deploy it now?", true)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintf(s.out, "Edit stack.yaml if you want, then run damstack deploy in %s.\n", path)
		return nil
	}
	return runSteps(ctx, s, p, m, dir)
}

func taken(cfg *config.Config, name string) bool {
	for _, p := range cfg.Projects {
		if p.Name == name {
			return true
		}
	}
	return false
}

func chooseStack(ctx context.Context, s *streams, cfg *config.Config, o deployOptions) (*manifest.Manifest, string, project.StackRef, error) {
	if o.from != "" {
		dir, err := filepath.Abs(o.from)
		if err != nil {
			return nil, "", project.StackRef{}, err
		}
		m, err := manifest.Load(dir, release.Version)
		if err != nil {
			return nil, "", project.StackRef{}, err
		}
		return m, dir, project.StackRef{Name: m.Name, URL: "file://" + dir, Tag: devTag}, nil
	}
	name := o.stack
	if name == "" {
		stacks := cfg.AllStacks()
		names := make([]string, len(stacks))
		for i, st := range stacks {
			names[i] = st.Name
		}
		if len(names) == 1 {
			name = names[0]
		} else {
			answer, err := ask.Questions(s.prompt, []manifest.Question{{Name: "stack", Prompt: "Which stack", Type: "enum", Options: names, Default: names[0]}}, nil)
			if err != nil {
				return nil, "", project.StackRef{}, err
			}
			name = answer["stack"].(string)
		}
	}
	st, ok := cfg.Stack(name)
	if !ok {
		return nil, "", project.StackRef{}, fmt.Errorf("no stack named %s; damstack stacks lists them, damstack add adds one", name)
	}
	cache, err := config.CacheDir()
	if err != nil {
		return nil, "", project.StackRef{}, err
	}
	fmt.Fprintf(s.out, "Looking at %s\n", st.URL)
	r, err := stack.Latest(ctx, st.URL)
	if err != nil {
		return nil, "", project.StackRef{}, err
	}
	dir, err := stack.Fetch(ctx, cache, st.URL, r)
	if err != nil {
		return nil, "", project.StackRef{}, err
	}
	m, err := manifest.Load(dir, release.Version)
	if err != nil {
		return nil, "", project.StackRef{}, err
	}
	fmt.Fprintf(s.out, "%s %s: %s\n\n", st.Name, r.Tag, m.Description)
	return m, dir, project.StackRef{Name: st.Name, URL: st.URL, Tag: r.Tag, Commit: r.Commit}, nil
}

// devTag marks a project set up from a stack directory rather than a release.
const devTag = "dev"

// projectStack is the stack of a project at the release it was deployed
// with, or the stack in from.
func projectStack(ctx context.Context, p *project.Project, from string) (*manifest.Manifest, string, error) {
	dir := from
	if dir == "" {
		ref := p.Meta.Stack
		if ref.Tag == devTag {
			return nil, "", fmt.Errorf("%s was set up from the stack in %s; deploy it with --from and that directory",
				p.Meta.Name, strings.TrimPrefix(ref.URL, "file://"))
		}
		cache, err := config.CacheDir()
		if err != nil {
			return nil, "", err
		}
		if dir, err = stack.Fetch(ctx, cache, ref.URL, stack.Release{Tag: ref.Tag, Commit: ref.Commit}); err != nil {
			return nil, "", err
		}
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, "", err
	}
	m, err := manifest.Load(dir, release.Version)
	return m, dir, err
}

// cachedStack is the stack of a project if it is on this machine already.
func cachedStack(p *project.Project) (*manifest.Manifest, string) {
	ref := p.Meta.Stack
	dir := strings.TrimPrefix(ref.URL, "file://")
	if ref.Tag != devTag {
		cache, err := config.CacheDir()
		if err != nil {
			return nil, ""
		}
		dir = stack.Dir(cache, ref.URL, ref.Tag)
	}
	m, err := manifest.Load(dir, release.Version)
	if err != nil {
		return nil, ""
	}
	return m, dir
}

func sshKey() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	for _, name := range doctor.KeyNames {
		key := filepath.Join(home, ".ssh", name)
		if _, err := os.Stat(key + ".pub"); err != nil {
			continue
		}
		if _, err := os.Stat(key); err == nil {
			return key, nil
		}
	}
	return "", errors.New("no SSH key in ~/.ssh; damstack doctor says how to make one")
}

func newEngine(s *streams, p *project.Project, m *manifest.Manifest, dir string) (*engine.Engine, string, error) {
	password, err := p.Password()
	if err != nil {
		return nil, "", err
	}
	passwordPath, err := p.PasswordPath()
	if err != nil {
		return nil, "", err
	}
	key, err := sshKey()
	if err != nil {
		return nil, "", err
	}
	public, err := os.ReadFile(key + ".pub")
	if err != nil {
		return nil, "", err
	}
	image := m.Image
	if image == "" {
		image = release.ImageRef()
		if m.Requires.Toolbox != "" {
			if ok, err := manifest.Satisfies(release.ImageTag, m.Requires.Toolbox); err != nil || !ok {
				return nil, "", fmt.Errorf("the stack needs the tools image %s, this damstack uses %s; update damstack", m.Requires.Toolbox, release.ImageTag)
			}
		}
	}
	var stdin io.Reader
	if s.tty() {
		stdin = s.in
	}
	runner := &toolbox.Runner{
		Image: image, Stack: dir, Project: p.Dir, Password: passwordPath, Key: key,
		PublicKey: strings.TrimSpace(string(public)), UID: os.Getuid(), GID: os.Getgid(),
		TTY: stdin != nil, Stdin: stdin, Stdout: s.out, Stderr: s.err,
	}
	return &engine.Engine{
		Manifest: m, Stack: dir, Project: p, Runner: runner, Password: password, Key: true, Color: stdin != nil, Out: s.out,
		Confirm: func(q string) (bool, error) { return s.prompt.Confirm(q, false) },
	}, key, nil
}

func runSteps(ctx context.Context, s *streams, p *project.Project, m *manifest.Manifest, dir string) error {
	e, key, err := newEngine(s, p, m, dir)
	if err != nil {
		return err
	}
	if m.Server != nil {
		if err := ensureLogin(ctx, s, e, key); err != nil {
			return err
		}
	}
	for _, step := range m.Steps {
		if step.Once {
			done, err := p.Done(step.Name)
			if err != nil {
				return err
			}
			if done {
				fmt.Fprintf(s.out, "\n== %s: done before, runs once\n", step.Name)
				continue
			}
		}
		fmt.Fprintf(s.out, "\n== %s\n", step.Name)
		start := time.Now()
		err := e.Run(ctx, step, nil)
		if rerr := record(p, "deploy", step.Name, start, err); rerr != nil && err == nil {
			err = rerr
		}
		if errors.Is(err, engine.ErrDeclined) {
			return fmt.Errorf("%s: %w; run damstack deploy again when you want it", step.Name, err)
		}
		if err != nil {
			return fmt.Errorf("the step %s failed: %w\nFix what it says above, then run damstack deploy again in %s", step.Name, err, p.Dir)
		}
	}
	fmt.Fprintf(s.out, "\nDeployed %s.\n", p.Meta.Name)
	return nil
}

func ensureLogin(ctx context.Context, s *streams, e *engine.Engine, key string) error {
	config, err := e.Project.Config()
	if err != nil {
		return err
	}
	secrets, err := e.Project.Secrets(e.Password)
	if err != nil {
		return err
	}
	var server login.Server
	for _, f := range []struct {
		name, text string
		into       *string
	}{
		{"server.address", e.Manifest.Server.Address, &server.Address},
		{"server.first_user", e.Manifest.Server.FirstUser, &server.FirstUser},
		{"server.ops_user", e.Manifest.Server.OpsUser, &server.OpsUser},
	} {
		if *f.into, err = manifest.Render(f.name, f.text, config, secrets); err != nil {
			return fmt.Errorf("%s of the stack: %w", f.name, err)
		}
	}
	return login.New(key, filepath.Join(e.Project.Dir, project.KnownHosts), s.out).Ensure(ctx, server)
}

func record(p *project.Project, command, step string, start time.Time, err error) error {
	e := project.Entry{
		Time: start, Command: command, Step: step, Result: project.OK,
		Seconds: time.Since(start).Round(100 * time.Millisecond).Seconds(),
		Stack:   p.Meta.Stack.Tag, Commit: p.Meta.Stack.Commit, Damstack: release.Version,
	}
	if err != nil {
		e.Result, e.Error = project.Failed, err.Error()
	}
	return p.Record(e)
}
