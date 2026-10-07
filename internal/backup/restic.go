// Package backup pulls the restic backups a server makes to this machine, and
// runs the pulls on a schedule.
package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Snapshot is one snapshot of a repository.
type Snapshot struct {
	ID   string    `json:"short_id"`
	Time time.Time `json:"time"`
	Tags []string  `json:"tags"`
}

// Repo runs restic on the repository at Path on this machine.
type Repo struct {
	Restic   string
	Path     string
	Password string
	// Out takes what restic says, Err its errors.
	Out io.Writer
	Err io.Writer
}

func (r *Repo) run(ctx context.Context, stdout io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, r.Restic, append([]string{"-r", r.Path}, args...)...)
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "RESTIC_") }),
		"RESTIC_PASSWORD="+r.Password, "RESTIC_FROM_PASSWORD="+r.Password)
	cmd.Stdout, cmd.Stderr = stdout, r.Err
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restic %s: %w", args[0], err)
	}
	return nil
}

// Exists is whether a repository is at Path.
func (r *Repo) Exists() bool {
	_, err := os.Stat(filepath.Join(r.Path, "config"))
	return err == nil
}

// Pull copies the snapshots of from that are not here yet, creating the
// repository first, then forgets what keep does not keep.
func (r *Repo) Pull(ctx context.Context, from Source, keep []string) error {
	source := append([]string{"--from-repo", from.Repo}, from.Options...)
	if !r.Exists() {
		if err := os.MkdirAll(r.Path, 0o700); err != nil {
			return err
		}
		if err := r.run(ctx, r.Out, append([]string{"init", "--quiet", "--copy-chunker-params"}, source...)...); err != nil {
			return err
		}
	}
	if err := r.run(ctx, r.Out, append([]string{"copy", "--quiet"}, source...)...); err != nil {
		return err
	}
	if len(keep) == 0 {
		return nil
	}
	return r.run(ctx, r.Out, append([]string{"forget", "--prune", "--quiet"}, keep...)...)
}

// Snapshots are the snapshots here, oldest first.
func (r *Repo) Snapshots(ctx context.Context) ([]Snapshot, error) {
	var out bytes.Buffer
	if err := r.run(ctx, &out, "snapshots", "--json", "--no-lock"); err != nil {
		return nil, err
	}
	var snaps []Snapshot
	if err := json.Unmarshal(out.Bytes(), &snaps); err != nil {
		return nil, fmt.Errorf("restic snapshots: %w", err)
	}
	slices.SortFunc(snaps, func(a, b Snapshot) int { return a.Time.Compare(b.Time) })
	return snaps, nil
}

// Dump writes the snapshot id to w as a tar archive, which keeps the owners
// and modes of the files.
func (r *Repo) Dump(ctx context.Context, id string, w io.Writer) error {
	return r.run(ctx, w, "dump", "--archive", "tar", id, "/")
}
