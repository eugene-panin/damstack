package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"

	"github.com/eugene-panin/damstack/internal/backup"
	"github.com/eugene-panin/damstack/internal/project"
)

func restoreCommand(s *streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore [project]",
		Short: "Set up a new server for a project from its latest backup on this machine, in place of a lost one",
		Long: "Sets the server up as a deploy does, then puts back what the backup holds: the stack says what, such as\n" +
			"the data of Consul and Vault and of the apps. Give stack.yaml the address of the new server first, with\n" +
			"damstack edit. Whatever is on that server is replaced; a server that is still there needs no restore.",
		Example: "  damstack backup status ovh\n" +
			"  damstack edit ovh\n" +
			"  damstack restore ovh",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := pickProject(s, firstArg(args))
			if err != nil {
				return err
			}
			return restore(cmd.Context(), s, p)
		},
	}
	cmd.Flags().BoolVar(&s.yes, "yes", false, "go ahead without asking; needed without a terminal")
	return cmd
}

func restore(ctx context.Context, s *streams, p *project.Project) error {
	m, dir, err := projectStack(ctx, p, "")
	if err != nil {
		return err
	}
	if m.Backup == nil {
		return fmt.Errorf("the stack %s of %s makes no backups, so nothing to restore", m.Name, p.Meta.Name)
	}
	b, err := loadBackup(ctx, p)
	if err != nil {
		return err
	}
	repo, err := b.repo(ctx, s)
	if err != nil {
		return err
	}
	if !repo.Exists() {
		return fmt.Errorf("no backups of %s are on this machine, at %s: damstack backup pull copies them here, while the server is there", p.Meta.Name, b.plan.To)
	}
	snaps, err := repo.Snapshots(ctx)
	if err != nil {
		return err
	}
	snaps = slices.DeleteFunc(snaps, func(sn backup.Snapshot) bool { return !slices.Contains(sn.Tags, "scheduled") })
	if len(snaps) == 0 {
		return fmt.Errorf("the backups of %s at %s hold no snapshot of the server yet", p.Meta.Name, b.plan.To)
	}
	snap := snaps[len(snaps)-1]
	when := snap.Time.Local().Format("2006-01-02 15:04")
	question := fmt.Sprintf("Restore %s from the backup of %s?", p.Meta.Name, when)
	if m.Server != nil {
		address, _, err := serverLogin(p, m)
		if err != nil {
			return err
		}
		question = fmt.Sprintf("Set up the server at %s for %s from the backup of %s? Whatever is on that server is replaced.", address, p.Meta.Name, when)
	}
	ok, err := s.confirm(question, false)
	if err != nil || !ok {
		return err
	}

	work := filepath.Join(p.Dir, project.WorkDir, "restore")
	if err := os.MkdirAll(work, 0o700); err != nil {
		return err
	}
	tar := filepath.Join(work, "snapshot.tar")
	f, err := os.OpenFile(tar, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	err = repo.Dump(ctx, snap.ID, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(s.out, "Took the backup of %s out of %s.\n", when, b.plan.To)

	known := filepath.Join(p.Dir, project.KnownHosts)
	if err := os.Rename(known, known+".old"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := runSteps(ctx, s, p, m, dir, tar, false); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "\n%s is back from the backup of %s.\n", p.Meta.Name, when)
	return os.RemoveAll(work)
}
