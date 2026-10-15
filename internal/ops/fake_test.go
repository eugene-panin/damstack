package ops

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/consul/api"

	"github.com/eugene-panin/damstack/internal/engine"
	"github.com/eugene-panin/damstack/internal/event"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/remote"
	"github.com/eugene-panin/damstack/internal/toolbox"
)

// fakeUI keeps what a command says, emits and asks, and answers each confirm
// with yes.
type fakeUI struct {
	mu       sync.Mutex
	out, err bytes.Buffer
	events   []event.Event
	confirms []string
	plans    []event.Plan
	yes      bool
	onEmit   func(event.Event)
}

func (u *fakeUI) Out() io.Writer { return syncWriter{&u.mu, &u.out} }
func (u *fakeUI) Err() io.Writer { return syncWriter{&u.mu, &u.err} }

func (u *fakeUI) Emit(e event.Event) {
	u.mu.Lock()
	u.events = append(u.events, e)
	on := u.onEmit
	u.mu.Unlock()
	if on != nil {
		on(e)
	}
}

func (u *fakeUI) Confirm(question string, _ bool) (bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.confirms = append(u.confirms, question)
	return u.yes, nil
}

func (u *fakeUI) Approve(p event.Plan) (event.Approval, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.plans = append(u.plans, p)
	return event.Approval{Apply: true}, nil
}

func (u *fakeUI) Line(string) (string, error) { return "", nil }
func (u *fakeUI) Terminal() io.Reader         { return nil }

func (u *fakeUI) said() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.out.String() + u.err.String()
}

func (u *fakeUI) emitted() []event.Event {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.events)
}

type syncWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (s syncWriter) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(b)
}

// fakeScreen runs the work of a deploy at once, with stop as its stop channel.
type fakeScreen struct {
	ui   UI
	stop chan struct{}
}

func (s *fakeScreen) Run(ctx context.Context, _ *project.Project, _ *manifest.Manifest, work func(context.Context, UI, <-chan struct{}) error) error {
	stop := s.stop
	if stop == nil {
		stop = make(chan struct{})
	}
	return work(ctx, s.ui, stop)
}

// fakeRunner runs the steps of a stack: each run step by its program, with
// what to do in it.
type fakeRunner struct {
	mu     sync.Mutex
	ran    []string
	envs   []map[string]string
	do     map[string]func(ctx context.Context) error
	engine *engine.Engine
}

func (f *fakeRunner) Run(ctx context.Context, c toolbox.Cmd) error {
	name := filepath.Base(c.Args[0])
	f.mu.Lock()
	f.ran = append(f.ran, name)
	f.envs = append(f.envs, c.Env)
	do := f.do[name]
	f.mu.Unlock()
	if do != nil {
		return do(ctx)
	}
	return nil
}

func (f *fakeRunner) steps() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.ran, " ")
}

// home gives the test a damstack of its own: its config, cache and home.
func home(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for name, dir := range map[string]string{"XDG_CONFIG_HOME": "config", "XDG_CACHE_HOME": "cache", "DAMSTACK_HOME": "projects", "HOME": "home"} {
		t.Setenv(name, filepath.Join(root, dir))
	}
}

// newProject is a project named demo with stack.yaml config and its vault
// password, in a damstack of the test's own.
func newProject(t *testing.T, config string) (*project.Project, string) {
	t.Helper()
	home(t)
	dir := t.TempDir()
	for path, text := range map[string]string{
		project.MetaFile:   "name: demo\n",
		project.ConfigFile: config,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := project.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	password, err := project.NewPassword("demo")
	if err != nil {
		t.Fatal(err)
	}
	return p, password
}

// fakeEngines makes every engine run its steps on r.
func fakeEngines(t *testing.T, r *fakeRunner) {
	t.Helper()
	old := newEngine
	t.Cleanup(func() { newEngine = old })
	newEngine = func(_ context.Context, ui UI, yes bool, p *project.Project, m *manifest.Manifest, dir string) (*engine.Engine, string, error) {
		password, err := p.Password()
		if err != nil {
			return nil, "", err
		}
		e := &engine.Engine{Manifest: m, Stack: dir, Project: p, Runner: r, Password: password,
			Events: event.SinkFunc(ui.Emit), Approve: approver(ui, yes)}
		r.mu.Lock()
		r.engine = e
		r.mu.Unlock()
		return e, "", nil
	}
}

// fakeRemotes makes the platform's Consul c.
func fakeRemotes(t *testing.T, c *fakeConsul) {
	t.Helper()
	old := openRemote
	t.Cleanup(func() { openRemote = old })
	openRemote = func(name string, _ map[string]string) (*remote.Remote, error) {
		return remote.New(name, c, c), nil
	}
}

// fakeConsul is Consul's KV store and sessions in memory, as the remote
// package relies on them.
type fakeConsul struct {
	mu       sync.Mutex
	index    uint64
	pairs    map[string]*api.KVPair
	sessions map[string]bool
	n        int
	down     bool
	silent   bool
}

func newFakeConsul() *fakeConsul {
	return &fakeConsul{pairs: map[string]*api.KVPair{}, sessions: map[string]bool{}}
}

func (f *fakeConsul) put(p *api.KVPair, session string) {
	f.index++
	c := &api.KVPair{Key: p.Key, Value: slices.Clone(p.Value), Session: session, ModifyIndex: f.index, CreateIndex: f.index}
	if old := f.pairs[p.Key]; old != nil {
		c.CreateIndex = old.CreateIndex
	}
	f.pairs[p.Key] = c
}

func (f *fakeConsul) Get(key string, _ *api.QueryOptions) (*api.KVPair, *api.QueryMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.silent {
		return nil, nil, fmt.Errorf("dial tcp 10.0.0.1:8501: i/o timeout")
	}
	if p := f.pairs[key]; p != nil {
		c := *p
		return &c, &api.QueryMeta{LastIndex: f.index}, nil
	}
	return nil, &api.QueryMeta{LastIndex: f.index}, nil
}

func (f *fakeConsul) List(prefix string, _ *api.QueryOptions) (api.KVPairs, *api.QueryMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out api.KVPairs
	for k, p := range f.pairs {
		if strings.HasPrefix(k, prefix) {
			c := *p
			out = append(out, &c)
		}
	}
	return out, &api.QueryMeta{LastIndex: f.index}, nil
}

func (f *fakeConsul) CAS(p *api.KVPair, _ *api.WriteOptions) (bool, *api.WriteMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	old := f.pairs[p.Key]
	if (p.ModifyIndex == 0) != (old == nil) || old != nil && old.ModifyIndex != p.ModifyIndex {
		return false, nil, nil
	}
	f.put(p, "")
	return true, nil, nil
}

func (f *fakeConsul) Acquire(p *api.KVPair, _ *api.WriteOptions) (bool, *api.WriteMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.sessions[p.Session] {
		return false, nil, fmt.Errorf("invalid session %q", p.Session)
	}
	if old := f.pairs[p.Key]; old != nil && old.Session != "" && old.Session != p.Session {
		return false, nil, nil
	}
	f.put(p, p.Session)
	return true, nil, nil
}

func (f *fakeConsul) Release(p *api.KVPair, _ *api.WriteOptions) (bool, *api.WriteMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if old := f.pairs[p.Key]; old != nil && old.Session == p.Session {
		old.Session = ""
		return true, nil, nil
	}
	return false, nil, nil
}

func (f *fakeConsul) Delete(key string, _ *api.WriteOptions) (*api.WriteMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pairs, key)
	return nil, nil
}

func (f *fakeConsul) Create(*api.SessionEntry, *api.WriteOptions) (string, *api.WriteMeta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return "", nil, fmt.Errorf("dial tcp 10.0.0.1:8501: connection refused")
	}
	f.n++
	id := fmt.Sprintf("session-%d", f.n)
	f.sessions[id] = true
	return id, nil, nil
}

func (f *fakeConsul) Renew(id string, _ *api.WriteOptions) (*api.SessionEntry, *api.WriteMeta, error) {
	return f.entry(id), nil, nil
}

func (f *fakeConsul) Destroy(id string, _ *api.WriteOptions) (*api.WriteMeta, error) {
	f.expire(id)
	return nil, nil
}

func (f *fakeConsul) Info(id string, _ *api.QueryOptions) (*api.SessionEntry, *api.QueryMeta, error) {
	return f.entry(id), nil, nil
}

func (f *fakeConsul) entry(id string) *api.SessionEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.sessions[id] {
		return nil
	}
	return &api.SessionEntry{ID: id}
}

// expire ends a session as its TTL would, and the keys it holds with it.
func (f *fakeConsul) expire(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, id)
	for k, p := range f.pairs {
		if p.Session == id {
			delete(f.pairs, k)
			f.index++
		}
	}
}

// expireAll ends every session, as when the platform forgets them.
func (f *fakeConsul) expireAll() {
	f.mu.Lock()
	var ids []string
	for id := range f.sessions {
		ids = append(ids, id)
	}
	f.mu.Unlock()
	for _, id := range ids {
		f.expire(id)
	}
}
