package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/eugene-panin/damstack/internal/backup"
	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/project"
)

func renameCommand(s *streams) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <project> <new name>",
		Short: "Give a project another name on this Mac; its server runs on as it is",
		Long: "Renames the project in the list of damstack, its directory when it is in the default place, its vault\n" +
			"password and the schedule of its backup pulls. The server, stack.yaml and the backups stay as they are.",
		Example: "  damstack rename gcp-test2 gcp-test",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return rename(cmd.Context(), s, args[0], args[1])
		},
	}
}

func rename(ctx context.Context, s *streams, from, to string) (err error) {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	entry, ok := cfg.Project(from)
	switch {
	case !ok:
		return fmt.Errorf("no project named %s; damstack lists them", from)
	case !projectNameRe.MatchString(to) || taken(cfg, to):
		return fmt.Errorf("%s cannot be the name: 2 to 31 lowercase letters, digits and hyphens, not a project or a stack already", to)
	}
	p, err := project.Open(entry.Path)
	if err != nil {
		return err
	}
	oldPass, err := project.PasswordPath(from)
	if err != nil {
		return err
	}
	newPass, err := project.PasswordPath(to)
	if err != nil {
		return err
	}
	dir := entry.Path
	projects, err := config.ProjectsDir()
	if err != nil {
		return err
	}
	if filepath.Clean(dir) == filepath.Join(projects, from) {
		dir = filepath.Join(projects, to)
	}
	for _, path := range []string{filepath.Dir(newPass), dir} {
		if path == entry.Path {
			continue
		}
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s is there already; move it away, or choose another name", path)
		}
	}
	b, _ := loadBackup(ctx, p)
	scheduled := b != nil && backup.Scheduled(ctx, from)

	var undo []func()
	defer func() {
		if err != nil {
			for i := len(undo) - 1; i >= 0; i-- {
				undo[i]()
			}
		}
	}()
	if scheduled {
		if _, err := backup.Install(ctx, backup.Job{Project: from}); err != nil {
			return err
		}
		undo = append(undo, func() { scheduleBackup(ctx, s, p, b, false) })
	}
	if err := move(filepath.Dir(oldPass), filepath.Dir(newPass), &undo); err != nil {
		return err
	}
	if err := move(entry.Path, dir, &undo); err != nil {
		return err
	}
	if p, err = project.Open(dir); err != nil {
		return err
	}
	p.Meta.Name = to
	if err := p.SaveMeta(); err != nil {
		return err
	}
	undo = append(undo, func() { p.Meta.Name = from; p.SaveMeta() })
	for i := range cfg.Projects {
		if cfg.Projects[i].Name == from {
			cfg.Projects[i].Name, cfg.Projects[i].Path = to, dir
		}
	}
	if cfg.Current == from {
		cfg.Current = to
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(s.out, "%s is now %s, in %s.\n", from, to, dir)
	if scheduled {
		if err := scheduleBackup(ctx, s, p, b, true); err != nil {
			return fmt.Errorf("%w; damstack backup schedule %s schedules the pulls again", err, to)
		}
	}
	if m, _, merr := projectStack(ctx, p, ""); merr == nil && m.Server != nil && m.Server.WireGuard != nil {
		fmt.Fprintf(s.out, "The WireGuard app keeps the tunnel under the name %s: rename it there to %s, so that damstack tunnel finds it.\n", from, to)
	}
	return nil
}

// move renames a file or directory, adding the way back to undo; it does
// nothing for the same path.
func move(from, to string, undo *[]func()) error {
	if from == to {
		return nil
	}
	if _, err := os.Stat(from); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
		return err
	}
	if err := os.Rename(from, to); err != nil {
		return err
	}
	*undo = append(*undo, func() { os.Rename(to, from) })
	return nil
}
