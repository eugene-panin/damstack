package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/settings"
	"github.com/eugene-panin/damstack/internal/ui/tui"
)

// settingsScreen changes a project's stack.yaml on the settings screen; the
// file opens in the editor when the person asks, and comes back to it. It
// says whether the person asked to deploy.
func settingsScreen(ctx context.Context, s *streams, p *project.Project) (bool, error) {
	tui.UseProfile(colorprofile.Detect(s.out, os.Environ()))
	path := filepath.Join(p.Dir, project.ConfigFile)
	for {
		src, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		doc, err := settings.Parse(src)
		if err != nil {
			return false, fmt.Errorf("%s: %w; damstack edit --yaml opens it", path, err)
		}
		model := tui.NewSettings(p.Meta.Name, doc)
		if _, err := tea.NewProgram(model, tea.WithInput(s.in), tea.WithOutput(s.out), tea.WithContext(ctx)).Run(); err != nil {
			return false, err
		}
		r := model.Result()
		if r.Save {
			if err := writeConfig(path, model.Doc().Bytes()); err != nil {
				return false, err
			}
		}
		if !r.Raw {
			return r.Deploy, nil
		}
		if err := openEditor(s, path); err != nil {
			return false, err
		}
	}
}

// writeConfig writes stack.yaml whole or not at all.
func writeConfig(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
