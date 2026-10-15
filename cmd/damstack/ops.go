package main

import (
	"context"
	"io"
	"os"
	"sync/atomic"
	"time"

	"github.com/eugene-panin/damstack/internal/engine"
	"github.com/eugene-panin/damstack/internal/event"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/ops"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/wg"
)

// streamsUI is the ops.UI of the streams, read through them at each call: a
// screen takes them over while it is up.
type streamsUI struct{ s *streams }

func (u streamsUI) Out() io.Writer { return u.s.out }

func (u streamsUI) Err() io.Writer { return u.s.err }

func (u streamsUI) Emit(ev event.Event) { u.s.events.Emit(ev) }

func (u streamsUI) Confirm(question string, def bool) (bool, error) {
	return u.s.confirm(question, def)
}

func (u streamsUI) Approve(p event.Plan) (event.Approval, error) { return u.s.approve(p) }

func (u streamsUI) Line(prompt string) (string, error) { return u.s.prompt.Line(prompt) }

func (u streamsUI) Terminal() io.Reader {
	if u.s.tty() {
		return u.s.in
	}
	return nil
}

// streamsScreen is the ops.Screen of the streams: the deploy screen when the
// terminal allows.
type streamsScreen struct{ s *streams }

func (sc streamsScreen) Run(ctx context.Context, p *project.Project, m *manifest.Manifest, work func(context.Context, *atomic.Bool) error) error {
	return onScreen(ctx, sc.s, p, m, work)
}

type job = ops.Job

type sharing = ops.Sharing

const devTag = ops.DevTag

const projectKeyFile = ops.ProjectKeyFile

var urlRe = ops.URLRe

func projectStack(ctx context.Context, p *project.Project, from string) (*manifest.Manifest, string, error) {
	return ops.ProjectStack(ctx, p, from)
}

func refStack(ctx context.Context, ref project.StackRef) (*manifest.Manifest, string, error) {
	return ops.RefStack(ctx, ref)
}

func cachedStack(p *project.Project) (*manifest.Manifest, string) { return ops.CachedStack(p) }

func cachedRef(ref project.StackRef) (*manifest.Manifest, string) { return ops.CachedRef(ref) }

func appEnv(p *project.Project, m *manifest.Manifest, dir, password string) (map[string]string, error) {
	return ops.AppEnv(p, m, dir, password)
}

func doneLines(p *project.Project, m *manifest.Manifest, app string) []string {
	return ops.DoneLines(p, m, app)
}

func runDone(p *project.Project, m *manifest.Manifest) event.RunDone { return ops.RunDone(p, m) }

func logTo(p *project.Project, j job) (string, func(), error) { return ops.LogTo(p, j) }

func record(p *project.Project, command, step string, start time.Time, err error) error {
	return ops.Record(p, command, step, start, err)
}

func platformInputs(p *project.Project, stackDir, playbook, password string) (string, error) {
	return ops.PlatformInputs(p, stackDir, playbook, password)
}

func appSecretNames(p *project.Project) map[string]bool { return ops.AppSecretNames(p) }

func unchanged(p *project.Project, step, inputs string, now time.Time) string {
	return ops.Unchanged(p, step, inputs, now)
}

func keyFor(p *project.Project, m *manifest.Manifest, password string) (string, error) {
	return ops.KeyFor(p, m, password)
}

func ensureSSHKey(p *project.Project, m *manifest.Manifest, password string) (bool, error) {
	return ops.EnsureSSHKey(p, m, password)
}

func lockHere(p *project.Project) (*os.File, error) { return ops.LockHere(p) }

func ensureKeys(p *project.Project, m *manifest.Manifest, dir, password string) (wg.Change, error) {
	return ops.EnsureKeys(p, m, password)
}

func newEngine(ctx context.Context, s *streams, p *project.Project, m *manifest.Manifest, dir string) (*engine.Engine, string, error) {
	return ops.NewEngine(ctx, streamsUI{s}, s.yes, p, m, dir)
}

func fetchToolbox(ctx context.Context, s *streams, m *manifest.Manifest) (string, string, error) {
	return ops.FetchToolbox(ctx, s.err, m)
}

func waitTunnel(ctx context.Context, s *streams, p *project.Project, srv *manifest.Server) error {
	return ops.WaitTunnel(ctx, streamsUI{s}, p, srv)
}

func lockProject(ctx context.Context, s *streams, p *project.Project, m *manifest.Manifest, dir, password, action string) (*sharing, error) {
	return ops.LockProject(ctx, streamsUI{s}, s.breakGlass, p, m, dir, password, action)
}

func readProject(s *streams, p *project.Project, m *manifest.Manifest, dir, password string) *sharing {
	return ops.ReadProject(s.err, p, m, dir, password)
}

func buildJobs(ctx context.Context, s *streams, p *project.Project, m *manifest.Manifest, e *engine.Engine, restore, appsOnly bool) ([]job, error) {
	return ops.BuildJobs(ctx, streamsUI{s}, s.yes, p, m, e, restore, appsOnly)
}
