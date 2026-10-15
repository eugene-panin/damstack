package ops

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/eugene-panin/damstack/internal/backup"
	"github.com/eugene-panin/damstack/internal/manifest"
	"github.com/eugene-panin/damstack/internal/project"
)

// Restore sets up a new server for p from its latest backup on this machine,
// in place of a lost one, once the person agrees: d deploys the stack m in
// dir with the snapshot. It returns what the deploy changed, and when the
// backup was made.
func Restore(ctx context.Context, d *Deployer, p *project.Project, m *manifest.Manifest, dir string) (Result, string, error) {
	if m.Backup == nil {
		return Result{}, "", fmt.Errorf("the stack %s of %s makes no backups, so nothing to restore", m.Name, p.Meta.Name)
	}
	b, err := LoadBackup(ctx, p)
	if err != nil {
		return Result{}, "", err
	}
	repo, err := b.repo(ctx, d.UI)
	if err != nil {
		return Result{}, "", err
	}
	if !repo.Exists() {
		return Result{}, "", fmt.Errorf("no backups of %s are on this machine, at %s: damstack backup pull copies them here, while the server is there", p.Meta.Name, b.plan.To)
	}
	snaps, err := repo.Snapshots(ctx)
	if err != nil {
		return Result{}, "", err
	}
	snaps = slices.DeleteFunc(snaps, func(sn backup.Snapshot) bool { return !slices.Contains(sn.Tags, "scheduled") })
	if len(snaps) == 0 {
		return Result{}, "", fmt.Errorf("the backups of %s at %s hold no snapshot of the server yet", p.Meta.Name, b.plan.To)
	}
	snap := snaps[len(snaps)-1]
	when := snap.Time.Local().Format("2006-01-02 15:04")
	question := fmt.Sprintf("Restore %s from the backup of %s?", p.Meta.Name, when)
	if m.Server != nil {
		address, _, err := ServerLogin(p, m)
		if err != nil {
			return Result{}, "", err
		}
		question = fmt.Sprintf("Set up the server at %s for %s from the backup of %s? Whatever is on that server is replaced.", address, p.Meta.Name, when)
	}
	ok, err := d.UI.Confirm(question, false)
	if err != nil || !ok {
		return Result{}, "", err
	}

	root, err := p.Work()
	if err != nil {
		return Result{}, "", err
	}
	work := filepath.Join(root, "restore")
	if err := os.MkdirAll(work, 0o700); err != nil {
		return Result{}, "", err
	}
	defer os.RemoveAll(work)
	tar := filepath.Join(work, "snapshot.tar")
	f, err := os.OpenFile(tar, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return Result{}, "", err
	}
	err = repo.Dump(ctx, snap.ID, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Result{}, "", err
	}
	fmt.Fprintf(d.UI.Out(), "Took the backup of %s out of %s.\n", when, b.plan.To)

	known := filepath.Join(p.Dir, project.KnownHosts)
	if err := os.Rename(known, known+".old"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Result{}, "", err
	}
	r := *d
	r.Opts.Restore = tar
	res, err := r.Deploy(ctx, p, m, dir)
	if err != nil {
		return Result{}, "", err
	}
	return res, when, nil
}
