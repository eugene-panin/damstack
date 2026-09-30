package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
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
		if name, err = s.prompt.Line("A name for this project, such as my-cloud: "); err != nil {
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

	p, _, err = setup.Create(setup.Options{Manifest: m, Stack: dir, Ref: ref, Name: name, Dir: path, Given: given,
		Prompter: s.prompt, Review: review(s, m, name, path)})
	if err != nil {
		return err
	}
	cfg.Projects = append(cfg.Projects, config.Project{Name: name, Stack: ref.Name, Path: path})
	if err := cfg.Save(); err != nil {
		return err
	}
	pass, _ := p.PasswordPath()
	fmt.Fprintf(s.out, "\nSet up %s in %s: stack.yaml is the one file to edit.\n"+
		"The secrets are in vault.yml, encrypted; its password is %s.\n"+
		"Keep a copy of the password somewhere safe, such as a password manager: nothing restores without it.\n", name, path, pass)
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
	arg, from := o.stack, o.from
	if arg == "" && from == "" {
		var err error
		if arg, err = pickPlatform(s, cfg); err != nil {
			return nil, "", project.StackRef{}, err
		}
	}
	if from == "" {
		if info, err := os.Stat(arg); err == nil && info.IsDir() {
			from = arg
		}
	}
	if from != "" {
		dir, err := filepath.Abs(from)
		if err != nil {
			return nil, "", project.StackRef{}, err
		}
		m, err := manifest.Load(dir, release.Version)
		if err != nil {
			return nil, "", project.StackRef{}, err
		}
		if m.IsApp() {
			return nil, "", project.StackRef{}, fmt.Errorf("%s is an app: set up a project on a platform first, then damstack app add --from %s in it", m.Name, dir)
		}
		ref := project.StackRef{Name: m.Name, URL: "file://" + dir, Tag: devTag}
		intro(s, m, ref)
		return m, dir, ref, nil
	}

	name, url, library := arg, "", false
	if st, ok := cfg.Stack(arg); ok {
		url, library = st.URL, true
	} else {
		var err error
		if url, err = stack.NormalizeURL(arg); err != nil {
			return nil, "", project.StackRef{}, fmt.Errorf("%s is neither a platform of the library, nor an address, nor a directory; damstack stacks lists the library", arg)
		}
		for _, st := range cfg.AllStacks() {
			if st.URL == url {
				name, library = st.Name, true
			}
		}
	}
	cache, err := config.CacheDir()
	if err != nil {
		return nil, "", project.StackRef{}, err
	}
	fmt.Fprintf(s.out, "Looking at %s\n", url)
	r, err := stack.Latest(ctx, url)
	if err != nil {
		return nil, "", project.StackRef{}, err
	}
	dir, err := stack.Fetch(ctx, cache, url, r)
	if err != nil {
		return nil, "", project.StackRef{}, err
	}
	m, err := manifest.Load(dir, release.Version)
	if err != nil {
		return nil, "", project.StackRef{}, err
	}
	if !library {
		name = m.Name
	}
	if m.IsApp() {
		return nil, "", project.StackRef{}, fmt.Errorf("%s is an app: set up a project on a platform first, then damstack app add %s in it", name, arg)
	}
	ref := project.StackRef{Name: name, URL: url, Tag: r.Tag, Commit: r.Commit}
	if !library {
		fmt.Fprintf(s.out, "\n%s is not in the library of damstack. It runs with your SSH key and the secrets of the project.\n", url)
		ok, err := s.prompt.Confirm("Use it only if you trust the people who wrote it. Go on?", false)
		if err != nil {
			return nil, "", project.StackRef{}, err
		}
		if !ok {
			return nil, "", project.StackRef{}, engine.ErrDeclined
		}
	}
	intro(s, m, ref)
	return m, dir, ref, nil
}

// pickPlatform asks where a project starts from: a platform of the library,
// or a stack of one's own.
func pickPlatform(s *streams, cfg *config.Config) (string, error) {
	var names []string
	fmt.Fprintln(s.out, "Where your project starts from:")
	fmt.Fprintln(s.out)
	for _, st := range cfg.AllStacks() {
		if st.Kind == manifest.KindApp {
			continue
		}
		names = append(names, st.Name)
		fmt.Fprintf(s.out, "  %d. %-8s %s\n", len(names), st.Name, st.Description)
	}
	own := len(names) + 1
	fmt.Fprintf(s.out, "  %d. your own: owner/name, a git address, or a directory\n\n", own)
	for {
		answer, err := s.prompt.Line("Which? [1] ")
		if err != nil {
			return "", err
		}
		switch n, err := strconv.Atoi(answer); {
		case answer == "":
			return names[0], nil
		case err == nil && n >= 1 && n <= len(names):
			return names[n-1], nil
		case err == nil && n == own, answer == "own":
			return s.prompt.Line("Address of the stack, owner/name or a directory: ")
		case slices.Contains(names, answer):
			return answer, nil
		}
		fmt.Fprintf(s.out, "  answer a number from 1 to %d\n", own)
	}
}

// intro says what a stack sets up, what it needs, and how long it takes.
func intro(s *streams, m *manifest.Manifest, ref project.StackRef) {
	fmt.Fprintf(s.out, "\n%s %s: %s\n", ref.Name, ref.Tag, m.Description)
	if len(m.Needs) == 0 {
		fmt.Fprintln(s.out)
		return
	}
	if m.Takes != "" {
		fmt.Fprintf(s.out, "\nYou will need, %s, and:\n", m.Takes)
	} else {
		fmt.Fprintln(s.out, "\nYou will need:")
	}
	for _, need := range m.Needs {
		fmt.Fprintf(s.out, "  • %s\n", need)
	}
	fmt.Fprintln(s.out)
}

// review shows the summary of a new project and asks whether to set it up.
func review(s *streams, m *manifest.Manifest, name, path string) func(map[string]any) error {
	return func(answers map[string]any) error {
		data := map[string]any{"project": name}
		for k, v := range answers {
			data[k] = v
		}
		fmt.Fprintln(s.out, "\nSummary")
		tw := tabwriter.NewWriter(s.out, 0, 4, 3, ' ', 0)
		fmt.Fprintf(tw, "  project\t%s\n", path)
		for _, line := range m.Summary {
			value, err := manifest.RenderDefault(line.Label, line.Value, data, nil)
			if err != nil {
				return fmt.Errorf("summary %s of the stack: %w", line.Label, err)
			}
			fmt.Fprintf(tw, "  %s\t%s\n", line.Label, value)
		}
		tw.Flush()
		ok, err := s.prompt.Confirm("Set it up?", true)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("nothing was set up: %w", engine.ErrDeclined)
		}
		return nil
	}
}

// devTag marks a project set up from a stack directory rather than a release.
const devTag = "dev"

// projectStack is the stack of a project at the release it was deployed
// with, or in the directory it was set up from, or the stack in from.
func projectStack(ctx context.Context, p *project.Project, from string) (*manifest.Manifest, string, error) {
	if from != "" {
		dir, err := filepath.Abs(from)
		if err != nil {
			return nil, "", err
		}
		m, err := manifest.Load(dir, release.Version)
		return m, dir, err
	}
	return refStack(ctx, p.Meta.Stack)
}

// refStack is a stack at a release, fetched when it is not on this machine,
// or in the directory a dev release names.
func refStack(ctx context.Context, ref project.StackRef) (*manifest.Manifest, string, error) {
	dir := strings.TrimPrefix(ref.URL, "file://")
	if ref.Tag != devTag {
		cache, err := config.CacheDir()
		if err != nil {
			return nil, "", err
		}
		if dir, err = stack.Fetch(ctx, cache, ref.URL, stack.Release{Tag: ref.Tag, Commit: ref.Commit}); err != nil {
			return nil, "", err
		}
	}
	m, err := manifest.Load(dir, release.Version)
	return m, dir, err
}

// cachedStack is the stack of a project if it is on this machine already.
func cachedStack(p *project.Project) (*manifest.Manifest, string) {
	return cachedRef(p.Meta.Stack)
}

func cachedRef(ref project.StackRef) (*manifest.Manifest, string) {
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

func newEngine(ctx context.Context, s *streams, p *project.Project, m *manifest.Manifest, dir string) (*engine.Engine, string, error) {
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
		Image: image, Stack: dir, Project: p.Dir, Password: passwordPath,
		PublicKey: strings.TrimSpace(string(public)), UID: os.Getuid(), GID: os.Getgid(),
		TTY: stdin != nil, Stdin: stdin, Stdout: s.out, Stderr: s.err,
	}
	if err := sshAccess(ctx, runner, key); err != nil {
		return nil, "", err
	}
	return &engine.Engine{
		Manifest: m, Stack: dir, Project: p, Runner: runner, Password: password, Key: runner.Key != "", Color: stdin != nil, Out: s.out,
		Confirm: func(q string) (bool, error) { return s.prompt.Confirm(q, false) },
	}, key, nil
}

// runSteps deploys a project: the steps of its platform, then those of each
// app, then the platform's steps that come after the apps.
func runSteps(ctx context.Context, s *streams, p *project.Project, m *manifest.Manifest, dir string) error {
	e, key, err := newEngine(ctx, s, p, m, dir)
	if err != nil {
		return err
	}
	if m.Server != nil {
		if err := ensureLogin(ctx, s, e, key); err != nil {
			return err
		}
	}
	var before, after []manifest.Step
	for _, step := range m.Steps {
		if step.AfterApps {
			after = append(after, step)
		} else {
			before = append(before, step)
		}
	}
	if err := runList(ctx, s, p, e, m.Server, "", before); err != nil {
		return err
	}
	for _, ref := range p.Meta.Apps {
		am, adir, err := refStack(ctx, ref)
		if err != nil {
			return fmt.Errorf("the app %s: %w", ref.Name, err)
		}
		_, target, ok := am.Target(m.Provides)
		if !ok {
			return fmt.Errorf("the app %s has no way to run on %s, which provides %s", ref.Name, m.Name, strings.Join(m.Provides, ", "))
		}
		ae, _, err := newEngine(ctx, s, p, am, adir)
		if err != nil {
			return err
		}
		ae.App = ref.Name
		if ae.BaseEnv, err = appEnv(p, m, ae.Password); err != nil {
			return err
		}
		if err := runList(ctx, s, p, ae, m.Server, ref.Name+"/", target.Steps); err != nil {
			return err
		}
	}
	if err := runList(ctx, s, p, e, m.Server, "", after); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "\nDeployed %s.\n", p.Meta.Name)
	return nil
}

func runList(ctx context.Context, s *streams, p *project.Project, e *engine.Engine, srv *manifest.Server, prefix string, steps []manifest.Step) error {
	for _, step := range steps {
		name := prefix + step.Name
		if step.Once {
			done, err := p.Done(name)
			if err != nil {
				return err
			}
			if done {
				fmt.Fprintf(s.out, "\n== %s: done before, runs once\n", name)
				continue
			}
		}
		fmt.Fprintf(s.out, "\n== %s\n", name)
		if step.Tunnel && srv != nil {
			if err := waitTunnel(ctx, s, p, srv); err != nil {
				return err
			}
		}
		start := time.Now()
		err := e.Run(ctx, step, nil)
		if rerr := record(p, "deploy", name, start, err); rerr != nil && err == nil {
			err = rerr
		}
		if errors.Is(err, engine.ErrDeclined) {
			return fmt.Errorf("%s: %w; run damstack deploy again when you want it", name, err)
		}
		if err != nil {
			return fmt.Errorf("the step %s failed: %w\nFix what it says above, then run damstack deploy again in %s", name, err, p.Dir)
		}
	}
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

// waitTunnel returns once the server answers over the private network; in a
// terminal it asks to turn the tunnel on, and waits.
func waitTunnel(ctx context.Context, s *streams, p *project.Project, srv *manifest.Server) error {
	config, err := p.Config()
	if err != nil {
		return err
	}
	address, err := manifest.Render("server.tunnel", srv.Tunnel, config, nil)
	if err != nil {
		return fmt.Errorf("server.tunnel of the stack: %w", err)
	}
	help, err := manifest.Render("server.tunnel_help", srv.TunnelHelp, config, nil)
	if err != nil {
		return fmt.Errorf("server.tunnel_help of the stack: %w", err)
	}
	public, err := manifest.Render("server.address", srv.Address, config, nil)
	if err != nil {
		return fmt.Errorf("server.address of the stack: %w", err)
	}
	knownHosts := filepath.Join(p.Dir, project.KnownHosts)
	for {
		err := login.SameServer(ctx, knownHosts, public, net.JoinHostPort(address, "22"))
		if err == nil {
			return nil
		}
		msg := fmt.Sprintf("This step reaches the server through the private network, and %s does not answer.", address)
		if errors.Is(err, login.ErrOtherServer) {
			return fmt.Errorf("this step reaches the server through the private network, and at %s answers another "+
				"server than %s, most likely through the tunnel of another project. Nothing was changed. Turn that "+
				"tunnel off, and this project's on. %s", address, public, help)
		}
		if help != "" {
			msg += " " + help
		}
		if !s.tty() {
			return errors.New(msg)
		}
		fmt.Fprintln(s.out, msg)
		if _, err := s.prompt.Line("Press Enter once it is on: "); err != nil {
			return err
		}
	}
}

// appEnv is the app_env of a platform, rendered for its project.
func appEnv(p *project.Project, m *manifest.Manifest, password string) (map[string]string, error) {
	config, err := p.Config()
	if err != nil {
		return nil, err
	}
	secrets, err := p.Secrets(password)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for key, text := range m.AppEnv {
		if env[key], err = manifest.Render("app_env."+key, text, config, secrets); err != nil {
			return nil, fmt.Errorf("app_env.%s of %s: %w", key, m.Name, err)
		}
	}
	return env, nil
}
