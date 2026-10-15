package ops

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eugene-panin/damstack/internal/backup"
	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/release"
	"github.com/eugene-panin/damstack/internal/toolbox"
)

// fakeToolbox puts a toolbox in the cache whose restic answers snapshots
// --json with snaps and dump with the snapshot, and keeps the arguments of
// every call, one line each, in calls.
func fakeToolbox(t *testing.T, snaps []backup.Snapshot) (calls string) {
	t.Helper()
	cache, err := config.CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	tb := toolbox.Dir(cache, release.Toolbox)
	if err := os.MkdirAll(filepath.Join(tb, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tb, "VERSION"), []byte(release.Toolbox+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(snaps)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	fixture, calls := filepath.Join(dir, "snapshots.json"), filepath.Join(dir, "calls")
	if err := os.WriteFile(fixture, data, 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho \"$*\" >> '" + calls + "'\n" +
		"case \"$3\" in\n  snapshots) cat '" + fixture + "' ;;\n  dump) echo 'the snapshot' ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(tb, "bin", "restic"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return calls
}

type restoreRun struct {
	p      *project.Project
	m      *manifest.Manifest
	dir    string
	ui     *fakeUI
	runner *fakeRunner
	calls  string
	known  string
	to     string
}

var snapshotTime = time.Date(2026, 10, 1, 3, 0, 0, 0, time.Local)

// newRestore is a project whose stack backs up, with backups on this machine
// when here, and a known_hosts of its old server.
func newRestore(t *testing.T, here bool, snaps ...backup.Snapshot) *restoreRun {
	t.Helper()
	p, _ := newProject(t, "{}\n")
	devStack(t, p, "backup: {from: backups/server, to: backups/here, password: the-servers}\n")
	m, dir := CachedStack(p)
	r := &restoreRun{p: p, m: m, dir: dir, ui: &fakeUI{yes: true}, runner: &fakeRunner{do: map[string]func(context.Context) error{}},
		known: filepath.Join(p.Dir, project.KnownHosts), to: filepath.Join(p.Dir, "backups/here")}
	r.calls = fakeToolbox(t, snaps)
	fakeEngines(t, r.runner)
	if here {
		if err := os.MkdirAll(r.to, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(r.to, "config"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(r.known), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.known, []byte("203.0.113.10 ssh-ed25519 old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *restoreRun) restore(t *testing.T) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	d := &Deployer{UI: r.ui, Screen: &fakeScreen{ui: r.ui}, Opts: Options{Yes: true}}
	_, when, err := Restore(ctx, d, r.p, r.m, r.dir)
	return when, err
}

func (r *restoreRun) restic(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(r.calls)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(data)
}

func scheduled(id string, at time.Time) backup.Snapshot {
	return backup.Snapshot{ID: id, Time: at, Tags: []string{"scheduled"}}
}

func TestARestoreAsksBeforeReplacingTheServerAndStopsOnNo(t *testing.T) {
	r := newRestore(t, true, scheduled("4f2a9c1e", snapshotTime))
	r.ui.yes = false
	srv := manifest.Server{Address: "203.0.113.10", OpsUser: "ops"}
	r.m.Server = &srv
	when, err := r.restore(t)
	if err != nil || when != "" {
		t.Fatalf("a no is no error: %q, %v", when, err)
	}
	want := "Set up the server at 203.0.113.10 for demo from the backup of " + snapshotTime.Format("2006-01-02 15:04") + "? Whatever is on that server is replaced."
	if len(r.ui.confirms) != 1 || r.ui.confirms[0] != want {
		t.Errorf("asked %q", r.ui.confirms)
	}
	if got := r.runner.steps(); got != "" {
		t.Errorf("ran %q", got)
	}
	if calls := r.restic(t); strings.Contains(calls, "dump") {
		t.Errorf("nothing is taken out of the backups: %s", calls)
	}
	if _, err := os.Stat(r.known); err != nil {
		t.Errorf("known_hosts stays: %v", err)
	}
}

func TestARestoreDeploysTheLatestScheduledSnapshotAndRemovesIt(t *testing.T) {
	for _, c := range []struct {
		name string
		fail bool
	}{
		{"a deploy that goes well", false},
		{"a deploy that fails", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			later := snapshotTime.Add(2 * time.Hour)
			r := newRestore(t, true, scheduled("4f2a9c1e", snapshotTime.Add(-24*time.Hour)), scheduled("b7d03e55", snapshotTime),
				backup.Snapshot{ID: "c0ffee00", Time: later, Tags: []string{"manual"}})
			tar := filepath.Join(r.p.Dir, project.WorkDir, "restore", "snapshot.tar")
			var held string
			r.runner.do["up"] = func(context.Context) error {
				data, err := os.ReadFile(tar)
				if err != nil {
					return err
				}
				held = string(data)
				if c.fail {
					return errors.New("boom")
				}
				return nil
			}
			when, err := r.restore(t)
			if c.fail {
				if err == nil || !strings.Contains(err.Error(), "the step up failed: boom") || when != "" {
					t.Fatalf("got %q, %v", when, err)
				}
			} else if err != nil || when != snapshotTime.Format("2006-01-02 15:04") {
				t.Fatalf("got %q, %v", when, err)
			}
			if held != "the snapshot\n" {
				t.Errorf("the step is given the snapshot: %q", held)
			}
			if len(r.runner.envs) != 1 || r.runner.envs[0]["DAMSTACK_RESTORE"] != tar {
				t.Errorf("envs %v", r.runner.envs)
			}
			if !strings.Contains(r.restic(t), "dump --archive tar b7d03e55 /") {
				t.Errorf("the latest scheduled snapshot is taken out: %s", r.restic(t))
			}
			if _, err := os.Stat(filepath.Dir(tar)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the snapshot taken out is removed: %v", err)
			}
			if _, err := os.Stat(r.known); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the old server's host key is put away: %v", err)
			}
			if data, _ := os.ReadFile(r.known + ".old"); string(data) != "203.0.113.10 ssh-ed25519 old\n" {
				t.Errorf("known_hosts.old: %q", data)
			}
			if want := "Restore demo from the backup of " + snapshotTime.Format("2006-01-02 15:04") + "?"; len(r.ui.confirms) != 1 || r.ui.confirms[0] != want {
				t.Errorf("asked %q", r.ui.confirms)
			}
			if !strings.Contains(r.ui.said(), "Took the backup of "+snapshotTime.Format("2006-01-02 15:04")+" out of "+r.to+".") {
				t.Errorf("said %q", r.ui.said())
			}
		})
	}
}

func TestARestoreNeedsBackupsOnThisMachine(t *testing.T) {
	for _, c := range []struct {
		name   string
		here   bool
		snaps  []backup.Snapshot
		backup bool
		want   string
	}{
		{"no backups here", false, nil, true, "no backups of demo are on this machine, at %s: damstack backup pull copies them here, while the server is there"},
		{"no scheduled snapshot", true, []backup.Snapshot{{ID: "c0ffee00", Time: snapshotTime, Tags: []string{"manual"}}}, true,
			"the backups of demo at %s hold no snapshot of the server yet"},
		{"a stack that makes none", true, nil, false, "the stack demo of demo makes no backups, so nothing to restore"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRestore(t, c.here, c.snaps...)
			if !c.backup {
				r.m.Backup = nil
			}
			want := strings.Replace(c.want, "%s", r.to, 1)
			if _, err := r.restore(t); err == nil || err.Error() != want {
				t.Errorf("got %v, want %q", err, want)
			}
			if len(r.ui.confirms) != 0 || r.runner.steps() != "" {
				t.Errorf("asked %q, ran %q", r.ui.confirms, r.runner.steps())
			}
			if _, err := os.Stat(r.known); err != nil {
				t.Errorf("known_hosts stays: %v", err)
			}
		})
	}
}
