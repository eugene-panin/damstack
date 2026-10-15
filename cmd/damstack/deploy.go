package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/muesli/cancelreader"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/eugene-panin/damstack/internal/ask"
	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/engine"
	"github.com/eugene-panin/damstack/internal/event"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/ops"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/release"
	"github.com/eugene-panin/damstack/internal/setup"
	"github.com/eugene-panin/damstack/internal/stack"
	"github.com/eugene-panin/damstack/internal/ui/plain"
	"github.com/eugene-panin/damstack/internal/ui/tui"
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
	// events gets what damstack says while it works: plain lines here, the
	// TUI later.
	events event.Sink
	// plain is --plain: lines, never the TUI.
	plain bool
	// ask, when set, asks yes-or-no questions instead of the prompt: the TUI.
	ask func(question string) (bool, error)
	// planner, when set, asks whether to apply a plan: the TUI's plan screen.
	planner func(event.Plan) (event.Approval, error)
	// breakGlass is --break-glass: change a shared project without its lock,
	// from the state on this machine, for when the platform itself is down.
	breakGlass bool
	// all is deploy --all: run every step, those whose inputs did not change too.
	all bool
	// session, on the home screen, keeps one program on the terminal while
	// its screens follow one another (tui.Session).
	session *tui.Session
	// only, when set, is what a deploy runs: the platform's steps by name and
	// the apps, all their steps, by theirs; it runs them though unchanged.
	only map[string]bool
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
	return &streams{in: stdin, out: stdout, err: stderr, prompt: p, events: plain.New(stdout)}
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
	if s.ask != nil {
		return s.ask(question)
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
	verbose    bool
	stack      string
	name       string
	dir        string
	answers    string
	from       string
	yes        bool
	appsOnly   bool
	all        bool
	only       string
	breakGlass bool
	plain      bool
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
	cmd.Flags().BoolVar(&o.appsOnly, "apps-only", false, "deploy only the apps, skipping the platform's Ansible steps (bootstrap, provision); for when the platform is already up and unchanged")
	cmd.Flags().BoolVar(&o.all, "all", false, "run every step, also the platform's Ansible steps whose inputs did not change since they last ran well")
	cmd.Flags().StringVar(&o.only, "only", "", "deploy only these, comma-separated: steps of the platform (such as provision, apply) and apps (such as sauron); they run though unchanged")
	cmd.Flags().BoolVar(&o.breakGlass, "break-glass", false, "change a project shared on its platform without the platform's lock, from the state on this machine; only to repair the platform itself")
	cmd.Flags().BoolVar(&o.plain, "plain", false, "show plain lines, as in a log, instead of the full-screen view on a terminal")
	return cmd
}

var projectNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)

func deploy(ctx context.Context, s *streams, o deployOptions) error {
	s.verbose, s.yes, s.breakGlass, s.plain, s.all = o.verbose, o.yes, o.breakGlass, o.plain, o.all
	if o.only != "" {
		s.only = map[string]bool{}
		for _, part := range strings.Split(o.only, ",") {
			s.only[strings.TrimSpace(part)] = true
		}
	}
	cfg, err := ops.LoadConfig(ctx)
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
		m, dir, err := ops.ProjectStack(ctx, p, o.from)
		if err != nil {
			return err
		}
		return runSteps(ctx, s, p, m, dir, o.appsOnly)
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
	return runSteps(ctx, s, p, m, dir, false)
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
		ref := project.StackRef{Name: m.Name, URL: "file://" + dir, Tag: ops.DevTag}
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

// runSteps deploys a project: the steps of its platform, then those of each
// app, then the platform's steps that come after the apps.
func runSteps(ctx context.Context, s *streams, p *project.Project, m *manifest.Manifest, dir string, appsOnly bool) error {
	r, err := deployer(s, appsOnly).Deploy(ctx, p, m, dir)
	if err != nil {
		return err
	}
	return deployed(ctx, s, p, m, dir, r)
}

// deployer deploys over the streams, as their flags say.
func deployer(s *streams, appsOnly bool) *ops.Deployer {
	return &ops.Deployer{UI: streamsUI{s}, Screen: streamsScreen{s}, Opts: ops.Options{
		Yes: s.yes, Verbose: s.verbose, BreakGlass: s.breakGlass, All: s.all, AppsOnly: appsOnly, Only: s.only,
	}}
}

// deployed says what a deploy of p set up and what it changed, and schedules
// the pulls of its backups.
func deployed(ctx context.Context, s *streams, p *project.Project, m *manifest.Manifest, dir string, r ops.Result) error {
	keys := r.Keys
	fmt.Fprintf(s.out, "\nDone. %s is running.\n", p.Meta.Name)
	done(s.out, p, m, "")
	if keys.Any() {
		fmt.Fprintln(s.out, "\nNew WireGuard keys: each device needs its new configuration, and the old one no longer works.")
		names := keys.Added
		if keys.Server {
			t, err := ops.OpenTunnel(streamsUI{s}, s.yes, p, m, dir)
			if err == nil {
				names = t.Names()
			}
		}
		for _, name := range names {
			fmt.Fprintf(s.out, "  damstack tunnel show %s    (--qr for a phone)\n", name)
		}
	}
	for _, ref := range p.Meta.Apps {
		if am, _ := ops.CachedRef(ref); am != nil && len(am.Done) > 0 {
			fmt.Fprintf(s.out, "%s:\n", ref.Name)
			done(s.out, p, am, ref.Name)
		}
	}
	trustNotice(ctx, s, p, m)
	if m.Backup == nil {
		return nil
	}
	b, err := ops.LoadBackup(ctx, p)
	if err != nil {
		return err
	}
	fmt.Fprintln(s.out)
	return ops.ScheduleBackup(ctx, streamsUI{s}, p, b, true)
}

// done says what a stack serves, from its done lines.
func done(w io.Writer, p *project.Project, m *manifest.Manifest, app string) {
	for _, line := range ops.DoneLines(p, m, app) {
		fmt.Fprintf(w, "  %s\n", line)
	}
}
