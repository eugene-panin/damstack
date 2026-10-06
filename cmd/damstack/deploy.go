package main

import (
	"bufio"
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

	"github.com/muesli/cancelreader"
	"github.com/skip2/go-qrcode"
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
	// verbose shows the output of the tools instead of keeping it in a log.
	verbose bool
	// yes is --yes: go ahead wherever damstack would ask to.
	yes bool
}

// newStreams asks its questions on stderr, so they reach the person when the
// output goes to a file, and only when stdin is a terminal. A question waiting
// for an answer gives up when ctx ends, at the first Ctrl-C.
func newStreams(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) *streams {
	p := ask.NewPrompter(stdin, stderr)
	p.NoTerminal = true
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		p.NoTerminal = false
		if cr, err := cancelreader.NewReader(f); err == nil {
			context.AfterFunc(ctx, func() { cr.Cancel() })
			p.In = bufio.NewReader(cancelled{cr})
		}
		p.Hidden = func() (string, error) { return readPassword(ctx, int(f.Fd())) }
	}
	return &streams{in: stdin, out: stdout, err: stderr, prompt: p}
}

// readPassword reads a line from the terminal fd without echoing it, and gives
// up when ctx ends, at the first Ctrl-C. The read itself cannot be stopped, so
// it is left behind with the terminal put back as it was: damstack ends then.
func readPassword(ctx context.Context, fd int) (string, error) {
	state, err := term.GetState(fd)
	if err != nil {
		return "", err
	}
	type result struct {
		b   []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		b, err := term.ReadPassword(fd)
		done <- result{b, err}
	}()
	select {
	case r := <-done:
		return string(r.b), r.err
	case <-ctx.Done():
		_ = term.Restore(fd, state)
		return "", context.Canceled
	}
}

// cancelled reports a read given up at Ctrl-C as context.Canceled, which
// damstack ends with quietly, as an interrupted run.
type cancelled struct{ r io.Reader }

func (c cancelled) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if errors.Is(err, cancelreader.ErrCanceled) {
		err = context.Canceled
	}
	return n, err
}

// confirm asks a yes or no question, unless --yes answered it already;
// without a terminal to ask in, only --yes goes ahead.
func (s *streams) confirm(question string, def bool) (bool, error) {
	if s.yes {
		return true, nil
	}
	if s.prompt.NoTerminal {
		return false, fmt.Errorf("%q: %w; pass --yes to go ahead", question, ask.ErrNoTerminal)
	}
	return s.prompt.Confirm(question, def)
}

func (s *streams) tty() bool {
	in, ok := s.in.(*os.File)
	out, ok2 := s.out.(*os.File)
	return ok && ok2 && term.IsTerminal(int(in.Fd())) && term.IsTerminal(int(out.Fd()))
}

type deployOptions struct {
	verbose bool
	stack   string
	name    string
	dir     string
	answers string
	from    string
	yes     bool
}

func deployCommand(s *streams) *cobra.Command {
	var o deployOptions
	cmd := &cobra.Command{
		Use:   "deploy [project or stack]",
		Short: "Deploy a project again, or set up a new one from a stack",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				o.stack = args[0]
			}
			return deploy(cmd.Context(), s, o)
		},
	}
	cmd.Flags().StringVar(&o.name, "name", "", "the name of the new project")
	cmd.Flags().StringVar(&o.dir, "dir", "", "where to put the new project, instead of ~/.damstack/<name>")
	cmd.Flags().StringVar(&o.answers, "answers", "", "a YAML file with answers to the questions, and the secrets the stack asks for")
	cmd.Flags().BoolVarP(&o.verbose, "verbose", "v", false, "show the output of Ansible and OpenTofu, instead of keeping it in a log of the project")
	cmd.Flags().StringVar(&o.from, "from", "", "deploy the stack in this directory as it is, instead of a release; for writing a stack")
	cmd.Flags().BoolVar(&o.yes, "yes", false, "go ahead wherever damstack would ask, such as before a step marked confirm; needed without a terminal")
	return cmd
}

var projectNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)

func deploy(ctx context.Context, s *streams, o deployOptions) error {
	s.verbose, s.yes = o.verbose, o.yes
	cfg, err := loadConfig(ctx)
	if err != nil {
		return err
	}
	fresh := o.name != "" || o.dir != "" || o.answers != ""
	var p *project.Project
	switch {
	case o.stack != "" && !fresh:
		if entry, ok := cfg.Project(o.stack); ok {
			if p, err = project.Open(entry.Path); err != nil {
				return err
			}
		}
	case o.stack == "" && !fresh:
		if p, err = pickOrNew(s, cfg, o.from); err != nil {
			return err
		}
	}
	if p != nil {
		m, dir, err := projectStack(ctx, p, o.from)
		if err != nil {
			return err
		}
		return runSteps(ctx, s, p, m, dir)
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
			fmt.Fprintf(s.err, "  %s cannot be the name: 2 to 31 lowercase letters, digits and hyphens, not a project or a stack already\n", name)
		}
		if s.prompt.NoTerminal {
			return fmt.Errorf("the new project needs a name, and %v: pass --name", ask.ErrNoTerminal)
		}
		if name, err = s.prompt.Line("A name for this project, such as my-cloud: "); err != nil {
			return err
		}
	}
	path := o.dir
	if path == "" {
		dir, err := config.ProjectsDir()
		if err != nil {
			return err
		}
		path = filepath.Join(dir, name)
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

// taken is whether a name is a project already, or a stack, which deploy
// would take it for.
func taken(cfg *config.Config, name string) bool {
	if _, ok := cfg.Project(name); ok {
		return true
	}
	_, ok := cfg.Stack(name)
	return ok
}

// pickOrNew is the project deploy works on when none and no stack is named:
// the one the rules of pickProject find, else one picked from a list that
// also offers a new one, which is nil.
func pickOrNew(s *streams, cfg *config.Config, from string) (*project.Project, error) {
	p, _, err := resolveProject("")
	if err != nil || p != nil {
		return p, err
	}
	if len(cfg.Projects) == 0 || from != "" {
		return nil, nil
	}
	if !s.tty() {
		return nil, fmt.Errorf("there are %d projects: name the one to deploy, such as damstack deploy %s, or a stack to set up a new one",
			len(cfg.Projects), cfg.Projects[0].Name)
	}
	fmt.Fprintln(s.err, "Which project?")
	for i, entry := range cfg.Projects {
		fmt.Fprintf(s.err, "  %d. %s\n", i+1, entry.Name)
	}
	fmt.Fprintf(s.out, "  %d. a new project\n", len(cfg.Projects)+1)
	for {
		answer, err := s.prompt.Line("Which? ")
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(answer)
		switch {
		case err == nil && n == len(cfg.Projects)+1:
			return nil, nil
		case err == nil && n >= 1 && n <= len(cfg.Projects):
			return project.Open(cfg.Projects[n-1].Path)
		}
		fmt.Fprintf(s.out, "  answer a number from 1 to %d\n", len(cfg.Projects)+1)
	}
}

func chooseStack(ctx context.Context, s *streams, cfg *config.Config, o deployOptions) (*manifest.Manifest, string, project.StackRef, error) {
	arg, from := o.stack, o.from
	if arg == "" && from == "" {
		if s.prompt.NoTerminal {
			return nil, "", project.StackRef{}, fmt.Errorf("name the stack to set up, such as damstack deploy hashi, and %v to pick one; damstack stacks lists them", ask.ErrNoTerminal)
		}
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
		ok, err := s.confirm("Use it only if you trust the people who wrote it. Go on?", false)
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
	fmt.Fprintln(s.err, "Where your project starts from:")
	fmt.Fprintln(s.err)
	for _, st := range cfg.AllStacks() {
		if st.Kind == manifest.KindApp {
			continue
		}
		names = append(names, st.Name)
		fmt.Fprintf(s.err, "  %d. %-8s %s\n", len(names), st.Name, st.Description)
	}
	own := len(names) + 1
	fmt.Fprintf(s.err, "  %d. your own: owner/name, a git address, or a directory\n\n", own)
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
		fmt.Fprintf(s.err, "  answer a number from 1 to %d\n", own)
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
		ok, err := s.confirm("Set it up?", true)
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
	key, err := keyFor(p, m, password)
	if err != nil {
		return nil, "", err
	}
	public, err := os.ReadFile(key + ".pub")
	if err != nil {
		return nil, "", err
	}
	tb, cache, err := fetchToolbox(ctx, s, m)
	if err != nil {
		return nil, "", err
	}
	var stdin io.Reader
	if s.tty() {
		stdin = s.in
	}
	runner := &toolbox.Runner{
		Toolbox: tb, Stack: dir, Project: p.Dir, Password: passwordPath, Cache: cache,
		PublicKey: strings.TrimSpace(string(public)), TTY: stdin != nil, Stdin: stdin, Stdout: s.out, Stderr: s.err,
	}
	if err := sshAccess(ctx, runner, key); err != nil {
		return nil, "", err
	}
	return &engine.Engine{
		Manifest: m, Stack: dir, Project: p, Runner: runner, Password: password, PasswordFile: passwordPath, KeyFile: runner.Key,
		Color: stdin != nil, Out: s.out,
		Confirm: func(q string) (bool, error) { return s.confirm(q, false) },
	}, key, nil
}

// fetchToolbox is the toolbox the stack m runs with, fetched the first
// time, and the cache it is in.
func fetchToolbox(ctx context.Context, s *streams, m *manifest.Manifest) (string, string, error) {
	if m != nil && m.Requires.Toolbox != "" {
		if ok, err := manifest.Satisfies(release.Toolbox, m.Requires.Toolbox); err != nil || !ok {
			return "", "", fmt.Errorf("the stack needs the toolbox %s, this damstack has %s; update damstack", m.Requires.Toolbox, release.Toolbox)
		}
	}
	cache, err := config.CacheDir()
	if err != nil {
		return "", "", err
	}
	if !toolbox.Have(cache, release.Toolbox) {
		fmt.Fprintf(s.err, "Fetching the toolbox %s, about 90 MB, once.\n", release.Toolbox)
	}
	tb, err := toolbox.Fetch(ctx, cache, release.Toolbox, release.ToolboxSHA256)
	return tb, cache, err
}

// job is a step to run, of the platform or of an app.
type job struct {
	e    *engine.Engine
	name string
	step manifest.Step
}

// runSteps deploys a project: the steps of its platform, then those of each
// app, then the platform's steps that come after the apps.
func runSteps(ctx context.Context, s *streams, p *project.Project, m *manifest.Manifest, dir string) error {
	password, err := p.Password()
	if err != nil {
		return err
	}
	if _, err := ensureSSHKey(p, m, password); err != nil {
		return err
	}
	e, key, err := newEngine(ctx, s, p, m, dir)
	if err != nil {
		return err
	}
	if m.Server != nil {
		if err := ensureLogin(ctx, s, e, key); err != nil {
			return err
		}
	}
	keys, err := ensureKeys(p, m, dir, e.Password)
	if err != nil {
		return err
	}
	jobs, err := buildJobs(ctx, s, p, m, e)
	if err != nil {
		return err
	}
	for i, j := range jobs {
		if err := runJob(ctx, s, p, j, i+1, len(jobs), m.Server); err != nil {
			return err
		}
	}
	fmt.Fprintf(s.out, "\nDone. %s is running.\n", p.Meta.Name)
	done(s.out, p, m, "")
	if keys.Any() {
		fmt.Fprintln(s.out, "\nNew WireGuard keys: each device needs its new configuration, and the old one no longer works.")
		names := keys.Added
		if keys.Server {
			t, err := newTunnel(s, p, m, dir)
			if err == nil {
				names = t.names
			}
		}
		for _, name := range names {
			fmt.Fprintf(s.out, "  damstack tunnel show %s    (--qr for a phone)\n", name)
		}
	}
	for _, ref := range p.Meta.Apps {
		if am, _ := cachedRef(ref); am != nil && len(am.Done) > 0 {
			fmt.Fprintf(s.out, "%s:\n", ref.Name)
			done(s.out, p, am, ref.Name)
		}
	}
	trustNotice(ctx, s, p, m)
	if m.Backup == nil {
		return nil
	}
	b, err := loadBackup(ctx, p)
	if err != nil {
		return err
	}
	fmt.Fprintln(s.out)
	return scheduleBackup(ctx, s, p, b, true)
}

// buildJobs lists the steps of a deploy in their order: the platform's, every
// app's, then the platform's that come after the apps.
func buildJobs(ctx context.Context, s *streams, p *project.Project, m *manifest.Manifest, e *engine.Engine) ([]job, error) {
	var jobs, after []job
	for _, step := range m.Steps {
		if step.AfterApps {
			after = append(after, job{e, step.Name, step})
		} else {
			jobs = append(jobs, job{e, step.Name, step})
		}
	}
	for _, ref := range p.Meta.Apps {
		am, adir, err := refStack(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("the app %s: %w", ref.Name, err)
		}
		_, target, ok := am.Target(m.Provides)
		if !ok {
			return nil, fmt.Errorf("the app %s has no way to run on %s, which provides %s", ref.Name, m.Name, strings.Join(m.Provides, ", "))
		}
		ae, _, err := newEngine(ctx, s, p, am, adir)
		if err != nil {
			return nil, err
		}
		ae.App = ref.Name
		if ae.BaseEnv, err = appEnv(p, m, e.Stack, ae.Password); err != nil {
			return nil, err
		}
		for _, step := range target.Steps {
			jobs = append(jobs, job{ae, ref.Name + "/" + step.Name, step})
		}
	}
	return append(jobs, after...), nil
}

// done says what a stack serves, from its done lines.
func done(w io.Writer, p *project.Project, m *manifest.Manifest, app string) {
	config, err := p.Config()
	if err != nil {
		return
	}
	data := map[string]any{"config": config}
	if app != "" {
		apps, _ := config["apps"].(map[string]any)
		data["app"] = apps[app]
	}
	for i, text := range m.Done {
		line, err := manifest.RenderData(fmt.Sprintf("done[%d]", i), text, data, nil)
		if err != nil || strings.TrimSpace(line) == "" {
			continue
		}
		fmt.Fprintf(w, "  %s\n", line)
	}
}

func runJob(ctx context.Context, s *streams, p *project.Project, j job, n, total int, srv *manifest.Server) error {
	title := j.step.Title
	if title == "" {
		title = j.name
	}
	if strings.Contains(j.name, "/") && j.step.Title != "" {
		title = j.name[:strings.Index(j.name, "/")] + ": " + title
	}
	if j.step.Once {
		done, err := p.Done(j.name)
		if err != nil {
			return err
		}
		if done {
			fmt.Fprintf(s.out, "\n%d/%d  %s: done before, runs once\n", n, total, title)
			return nil
		}
	}
	takes := ""
	if j.step.Takes != "" {
		takes = " (" + j.step.Takes + ")"
	}
	fmt.Fprintf(s.out, "\n%d/%d  %s%s\n", n, total, title, takes)
	if j.step.Tunnel && srv != nil {
		if err := waitTunnel(ctx, s, p, srv); err != nil {
			return err
		}
	}

	log := ""
	if !s.verbose {
		var restore func()
		var err error
		if log, restore, err = logTo(p, j); err != nil {
			return err
		}
		defer restore()
	}
	start := time.Now()
	err := j.e.Run(ctx, j.step, nil)
	if rerr := record(p, "deploy", j.name, start, err); rerr != nil && err == nil {
		err = rerr
	}
	if errors.Is(err, engine.ErrDeclined) {
		return fmt.Errorf("%s: %w; run damstack deploy again when you want it", j.name, err)
	}
	if err != nil {
		if log != "" {
			tail(s.out, log, 30)
			fmt.Fprintf(s.out, "  The whole output is in %s\n", log)
		}
		return fmt.Errorf("the step %s failed: %w\nFix what it says above, then run damstack deploy again in %s", j.name, err, p.Dir)
	}
	fmt.Fprintf(s.out, "     ok, %s\n", time.Since(start).Round(time.Second))
	return nil
}

// logTo sends the output of the tools of a job to a log of the project, and
// returns its path and how to send it back.
func logTo(p *project.Project, j job) (string, func(), error) {
	runner, ok := j.e.Runner.(*toolbox.Runner)
	if !ok {
		return "", func() {}, nil
	}
	dir := filepath.Join(p.Dir, project.WorkDir, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, time.Now().Format("2006-01-02T15-04-05")+"-"+strings.ReplaceAll(j.name, "/", "-")+".log")
	f, err := os.Create(path)
	if err != nil {
		return "", nil, err
	}
	stdout, stderr, stdin, tty := runner.Stdout, runner.Stderr, runner.Stdin, runner.TTY
	runner.Stdout, runner.Stderr, runner.Stdin, runner.TTY = f, f, nil, false
	j.e.Brief = true
	return path, func() {
		runner.Stdout, runner.Stderr, runner.Stdin, runner.TTY = stdout, stderr, stdin, tty
		j.e.Brief = false
		f.Close()
	}, nil
}

// tail writes the last lines of a log, indented.
func tail(w io.Writer, path string, lines int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	for _, line := range all {
		fmt.Fprintf(w, "  | %s\n", line)
	}
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
	shown := false
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
		on, known := login.WireGuardOn(ctx)
		hint := login.TunnelHint(on, public, known)
		if !s.tty() {
			for _, more := range []string{hint, help} {
				if more != "" {
					msg += " " + more
				}
			}
			return errors.New(msg)
		}
		if !shown {
			devices(s.out, p, srv, help)
			shown = true
		} else {
			fmt.Fprintf(s.out, "  %s does not answer yet.\n", address)
		}
		if hint != "" {
			fmt.Fprintf(s.out, "  %s\n", hint)
		}
		if _, err := s.prompt.Line("Press Enter once the tunnel is on: "); err != nil {
			return err
		}
	}
}

// devices says how the devices join the private network: the configuration
// to import on this computer, and a QR code for each other one.
func devices(w io.Writer, p *project.Project, srv *manifest.Server, help string) {
	fmt.Fprintln(w, "\n── Your devices join the private network")
	if srv.WireGuard != nil {
		config, _ := p.Config()
		names, _ := project.List(config, srv.WireGuard.Devices)
		for i, name := range names {
			if i == 0 {
				fmt.Fprintf(w, "This Mac: damstack tunnel show %s puts it into the WireGuard app; turn it on.\n", name)
				continue
			}
			fmt.Fprintf(w, "%s: damstack tunnel show %s --qr shows a QR code to scan in its WireGuard app.\n", name, name)
		}
		fmt.Fprintln(w)
		return
	}
	var configs []string
	if srv.TunnelConfigs != "" {
		configs, _ = filepath.Glob(filepath.Join(p.Dir, srv.TunnelConfigs))
	}
	if len(configs) == 0 {
		fmt.Fprintln(w, help)
		return
	}
	fmt.Fprintf(w, "This computer: import %s into the WireGuard app, and turn it on.\n", configs[0])
	for _, config := range configs[1:] {
		data, err := os.ReadFile(config)
		if err != nil {
			continue
		}
		code, err := qrcode.New(string(data), qrcode.Low)
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(config), filepath.Ext(config))
		fmt.Fprintf(w, "\n%s: scan this in the WireGuard app, or import %s\n%s", name, config, code.ToSmallString(false))
	}
	fmt.Fprintln(w)
}

// appEnv is the app_env of a platform, rendered for its project.
func appEnv(p *project.Project, m *manifest.Manifest, dir, password string) (map[string]string, error) {
	config, err := p.Config()
	if err != nil {
		return nil, err
	}
	secrets, err := p.Secrets(password)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"config": config, "dir": map[string]string{"project": p.Dir, "stack": dir}}
	env := map[string]string{}
	for key, text := range m.AppEnv {
		if env[key], err = manifest.RenderData("app_env."+key, text, data, secrets); err != nil {
			return nil, fmt.Errorf("app_env.%s of %s: %w", key, m.Name, err)
		}
	}
	return env, nil
}
