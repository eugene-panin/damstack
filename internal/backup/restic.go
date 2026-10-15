// Package backup pulls the restic backups a server makes to this machine, and
// runs the pulls on a schedule.
package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	Restic string
	Path   string
	// Password opens the repository here; FromPassword the one it pulls from,
	// the server's, which opened the repository here too before it had a key
	// of its own.
	Password     string
	FromPassword string
	// Out takes what restic says, Err its errors.
	Out io.Writer
	Err io.Writer
}

// exitWrongPassword is how restic exits when the password opens no key.
const exitWrongPassword = 12

func (r *Repo) run(ctx context.Context, stdout io.Writer, args ...string) error {
	if r.FromPassword == "" || r.FromPassword == r.Password {
		return r.runAs(ctx, r.Password, stdout, args...)
	}
	var said bytes.Buffer
	err := r.runWith(ctx, r.Password, stdout, &said, args...)
	if wrongPassword(err) {
		return r.runAs(ctx, r.FromPassword, stdout, args...)
	}
	r.Err.Write(said.Bytes())
	return err
}

func (r *Repo) runAs(ctx context.Context, password string, stdout io.Writer, args ...string) error {
	stderr := r.Err
	if quiet(args) {
		stderr = io.Discard
	}
	return r.runWith(ctx, password, stdout, stderr, args...)
}

func (r *Repo) runWith(ctx context.Context, password string, stdout, stderr io.Writer, args ...string) error {
	from := r.FromPassword
	if from == "" {
		from = r.Password
	}
	cmd := exec.CommandContext(ctx, r.Restic, append([]string{"-r", r.Path}, args...)...)
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "RESTIC_") }),
		"RESTIC_PASSWORD="+password, "RESTIC_FROM_PASSWORD="+from)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("restic %s: %w", args[0], err)
	}
	return nil
}

func wrongPassword(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == exitWrongPassword
}

// quiet are the calls made to learn whether a password opens the
// repository: their refusal is no error to show.
func quiet(args []string) bool {
	return len(args) > 1 && (args[0] == "cat" && args[1] == "config" || args[0] == "key" && args[1] == "list")
}

// Exists is whether a repository is at Path.
func (r *Repo) Exists() bool {
	_, err := os.Stat(filepath.Join(r.Path, "config"))
	return err == nil
}

// Copy copies the snapshots of from made after latest, the latest one here,
// creating the repository first with a key of its own, then checks it. A
// snapshot dated before latest is not a new backup, and one later than now
// is not one at all: neither is copied, so that none of them makes forget
// drop the good ones. It returns how many it copied, and those dated later
// than now.
func (r *Repo) Copy(ctx context.Context, from Source, latest, now time.Time) (int, []Snapshot, error) {
	source := append([]string{"--from-repo", from.Repo}, from.Options...)
	if !r.Exists() {
		if err := os.MkdirAll(r.Path, 0o700); err != nil {
			return 0, nil, err
		}
		if err := r.runAs(ctx, r.Password, r.Out, append([]string{"init", "--quiet", "--copy-chunker-params"}, source...)...); err != nil {
			return 0, nil, err
		}
	} else if err := r.own(ctx); err != nil {
		return 0, nil, err
	}
	there, err := r.snapshotsOf(ctx, from)
	if err != nil {
		return 0, nil, err
	}
	var ids []string
	var later []Snapshot
	for _, s := range there {
		switch {
		case s.Time.After(now.Add(time.Hour)):
			later = append(later, s)
		case latest.IsZero() || s.Time.After(latest):
			ids = append(ids, s.ID)
		}
	}
	if len(ids) > 0 {
		if err := r.runAs(ctx, r.Password, r.Out, append(append([]string{"copy", "--quiet"}, source...), ids...)...); err != nil {
			return 0, later, err
		}
	}
	if err := r.runAs(ctx, r.Password, r.Out, "check", "--quiet"); err != nil {
		return 0, later, err
	}
	return len(ids), later, nil
}

// snapshotsOf are the snapshots of the repository a pull copies from.
func (r *Repo) snapshotsOf(ctx context.Context, from Source) ([]Snapshot, error) {
	there := &Repo{Restic: r.Restic, Path: from.Repo, Password: r.FromPassword, Out: r.Out, Err: r.Err}
	if there.Password == "" {
		there.Password = r.Password
	}
	var out bytes.Buffer
	if err := there.runAs(ctx, there.Password, &out, append([]string{"snapshots", "--json", "--no-lock"}, from.Options...)...); err != nil {
		return nil, err
	}
	var snaps []Snapshot
	if err := json.Unmarshal(out.Bytes(), &snaps); err != nil {
		return nil, fmt.Errorf("restic snapshots: %w", err)
	}
	return snaps, nil
}

// own makes Password the key of the repository here, in place of the
// server's it was made with by an older damstack.
func (r *Repo) own(ctx context.Context) error {
	if r.FromPassword == "" || r.FromPassword == r.Password {
		return nil
	}
	if err := r.runAs(ctx, r.Password, io.Discard, "cat", "config"); err != nil {
		if !wrongPassword(err) {
			return err
		}
		file, err := os.CreateTemp("", "damstack-restic-*")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		_, err = file.WriteString(r.Password + "\n")
		if err := errors.Join(err, file.Close()); err != nil {
			return err
		}
		if err := r.runAs(ctx, r.FromPassword, io.Discard, "key", "add", "--quiet", "--new-password-file", file.Name()); err != nil {
			return err
		}
	}
	var out bytes.Buffer
	if err := r.runAs(ctx, r.FromPassword, &out, "key", "list", "--json"); err != nil {
		if wrongPassword(err) {
			return nil
		}
		return err
	}
	var keys []struct {
		Current bool   `json:"current"`
		ID      string `json:"id"`
	}
	if err := json.Unmarshal(out.Bytes(), &keys); err != nil {
		return fmt.Errorf("restic key list: %w", err)
	}
	for _, k := range keys {
		if k.Current {
			return r.runAs(ctx, r.Password, io.Discard, "key", "remove", k.ID)
		}
	}
	return nil
}

// Forget forgets what keep does not keep, and prunes what no snapshot needs.
func (r *Repo) Forget(ctx context.Context, keep []string) error {
	if len(keep) == 0 {
		return nil
	}
	return r.runAs(ctx, r.Password, r.Out, append([]string{"forget", "--prune", "--quiet"}, keep...)...)
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
