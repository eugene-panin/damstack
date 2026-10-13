package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/eugene-panin/damstack/internal/config"
	"github.com/eugene-panin/damstack/internal/sources"
	"github.com/eugene-panin/damstack/internal/ui/tui"
)

// sourcesScreen shows where apps come from, and adds, syncs and removes
// sources; it says whether the person asked for the catalog after.
func sourcesScreen(ctx context.Context, s *streams) (bool, error) {
	cfg, err := loadConfig(ctx)
	if err != nil {
		return false, err
	}
	cache, err := sources.Load()
	if err != nil {
		return false, err
	}
	library := tui.SourceRow{Name: "eugene-panin/damstack-library", Builtin: true}
	for _, st := range cfg.AllStacks() {
		if st.Kind == "app" && !slices.Contains(cfg.Stacks, st) {
			library.Apps = append(library.Apps, st.Name)
		}
	}
	if dir, err := config.CacheDir(); err == nil {
		if info, err := os.Stat(filepath.Join(dir, "library.yaml")); err == nil {
			library.Synced = info.ModTime()
		}
	}
	rows := []tui.SourceRow{library}
	for _, name := range cfg.Sources {
		rows = append(rows, sourceRow(name, cache[name]))
	}
	model := tui.NewSources(rows, time.Now(), tui.SourceActions{
		Sync: func(name string) tui.SourceRow {
			e := sources.Sync(ctx, name)
			if c, err := sources.Load(); err == nil {
				c[name] = e
				_ = c.Save()
			}
			if e.Err == "" && !slices.Contains(cfg.Sources, name) {
				cfg.Sources = append(cfg.Sources, name)
				_ = cfg.Save()
			}
			return sourceRow(name, e)
		},
		Remove: func(name string) error {
			cfg.Sources = slices.DeleteFunc(cfg.Sources, func(n string) bool { return n == name })
			if err := cfg.Save(); err != nil {
				return err
			}
			if c, err := sources.Load(); err == nil {
				delete(c, name)
				return c.Save()
			}
			return nil
		},
	})
	if err := s.show(ctx, model); err != nil {
		return false, err
	}
	return model.Catalog(), nil
}

func sourceRow(name string, e sources.Entry) tui.SourceRow {
	r := tui.SourceRow{Name: name, Private: e.Private, Login: e.Login, Synced: e.Synced, Err: e.Err}
	for _, a := range e.Apps {
		r.Apps = append(r.Apps, a.Name)
	}
	return r
}
