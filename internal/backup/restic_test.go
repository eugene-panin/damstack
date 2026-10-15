package backup

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeRestic is a restic that answers snapshots --json with snaps and keeps
// the arguments of every call, one line each, in calls.
func fakeRestic(t *testing.T, snaps []Snapshot) (restic, calls string) {
	t.Helper()
	dir := t.TempDir()
	data, err := json.Marshal(snaps)
	if err != nil {
		t.Fatal(err)
	}
	fixture, calls := filepath.Join(dir, "snapshots.json"), filepath.Join(dir, "calls")
	if err := os.WriteFile(fixture, data, 0o644); err != nil {
		t.Fatal(err)
	}
	restic = filepath.Join(dir, "restic")
	script := "#!/bin/sh\necho \"$*\" >> '" + calls + "'\nif [ \"$3\" = snapshots ]; then cat '" + fixture + "'; fi\n"
	if err := os.WriteFile(restic, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return restic, calls
}

func callsOf(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestACopyTakesOnlyTheNewBackupsAndNoneFromTheFuture(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	snaps := []Snapshot{
		{ID: "old", Time: now.Add(-48 * time.Hour)},
		{ID: "latest", Time: now.Add(-24 * time.Hour)},
		{ID: "new", Time: now.Add(-time.Hour)},
		{ID: "skewed", Time: now.Add(30 * time.Minute)},
		{ID: "future", Time: now.Add(2 * time.Hour)},
	}
	for _, c := range []struct {
		name   string
		latest time.Time
		copied []string
	}{
		{"a first pull", time.Time{}, []string{"old", "latest", "new", "skewed"}},
		{"a pull after one", now.Add(-24 * time.Hour), []string{"new", "skewed"}},
		{"nothing new", now.Add(time.Hour), nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			restic, calls := fakeRestic(t, snaps)
			repo := &Repo{Restic: restic, Path: t.TempDir(), Password: "own", Out: io.Discard, Err: io.Discard}
			if err := os.WriteFile(filepath.Join(repo.Path, "config"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			from := Source{Repo: "sftp:ops@10.77.0.1:/var/backups/restic", Options: []string{"-o", "sftp.command=ssh"}}
			n, later, err := repo.Copy(context.Background(), from, c.latest, now)
			if err != nil {
				t.Fatal(err)
			}
			if len(later) != 1 || later[0].ID != "future" {
				t.Errorf("later than now: %+v", later)
			}
			if n != len(c.copied) {
				t.Errorf("copied %d, want %d", n, len(c.copied))
			}
			got := callsOf(t, calls)
			if got[0] != "-r "+from.Repo+" snapshots --json --no-lock -o sftp.command=ssh" {
				t.Errorf("the snapshots are read from the server: %q", got[0])
			}
			copies := slices.DeleteFunc(slices.Clone(got), func(s string) bool { return !strings.Contains(s, " copy ") })
			switch {
			case c.copied == nil && len(copies) > 0:
				t.Errorf("with nothing new, nothing is copied: %q", copies)
			case c.copied != nil && (len(copies) != 1 || !strings.HasSuffix(copies[0], "--from-repo "+from.Repo+" -o sftp.command=ssh "+strings.Join(c.copied, " "))):
				t.Errorf("copied %q, want %v", copies, c.copied)
			}
			if got[len(got)-1] != "-r "+repo.Path+" check --quiet" {
				t.Errorf("a copy is checked: %q", got[len(got)-1])
			}
		})
	}
}

func TestTheFirstCopyMakesTheRepositoryHere(t *testing.T) {
	restic, calls := fakeRestic(t, []Snapshot{{ID: "a1", Time: time.Now().Add(-time.Hour)}})
	repo := &Repo{Restic: restic, Path: filepath.Join(t.TempDir(), "backups"), Password: "own", Out: io.Discard, Err: io.Discard}
	from := Source{Repo: "/var/backups/restic"}
	if n, _, err := repo.Copy(context.Background(), from, time.Time{}, time.Now()); err != nil || n != 1 {
		t.Fatalf("copied %d: %v", n, err)
	}
	got := callsOf(t, calls)
	if got[0] != "-r "+repo.Path+" init --quiet --copy-chunker-params --from-repo "+from.Repo {
		t.Errorf("the repository here is made with the chunker of the server's: %q", got[0])
	}
	if info, err := os.Stat(repo.Path); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("only its owner reads the copy: %v %v", info, err)
	}
}
