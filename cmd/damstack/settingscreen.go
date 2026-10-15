package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/charmbracelet/colorprofile"

	"github.com/eugene-panin/damstack/internal/atomicfile"
	"github.com/eugene-panin/damstack/internal/project"
	"github.com/eugene-panin/damstack/internal/settings"
	"github.com/eugene-panin/damstack/internal/ui/tui"
)

// settingsScreen changes a project's stack.yaml on the settings screen; the
// file opens in the editor when the person asks, and comes back to it. It
// says whether the person asked to deploy.
func settingsScreen(ctx context.Context, s *cli, p *project.Project, focus ...string) (bool, error) {
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
		model := tui.NewSettings(p.Meta.Name, settingsActions(doc))
		model.Focus(focus)
		if err := s.show(ctx, model); err != nil {
			return false, err
		}
		r := model.Result()
		if r.Save {
			if err := writeConfig(path, doc.Bytes()); err != nil {
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

// settingsActions are the settings screen's way to doc: its sections as it is
// now, and its values changed.
func settingsActions(doc *settings.Doc) tui.SettingsActions {
	original := doc.Bytes()
	return tui.SettingsActions{
		Sections: func() []tui.SettingsSection { return settingsSections(doc.Sections()) },
		Changed:  func() bool { return !bytes.Equal(doc.Bytes(), original) },
		Set:      doc.Set,
		SetList:  doc.SetList,
	}
}

var settingsKinds = map[settings.Kind]tui.SettingsKind{
	settings.Text:    tui.SettingText,
	settings.Number:  tui.SettingNumber,
	settings.Toggle:  tui.SettingToggle,
	settings.List:    tui.SettingList,
	settings.Heading: tui.SettingHeading,
	settings.Fixed:   tui.SettingFixed,
}

func settingsSections(sections []settings.Section) []tui.SettingsSection {
	out := make([]tui.SettingsSection, len(sections))
	for i, s := range sections {
		fields := make([]tui.SettingsField, len(s.Fields))
		for j, f := range s.Fields {
			fields[j] = tui.SettingsField{Path: f.Path, Label: f.Label, Help: f.Help, Kind: settingsKinds[f.Kind], Value: f.Value,
				Items: f.Items, Depth: f.Depth, Once: f.Once}
		}
		out[i] = tui.SettingsSection{Title: s.Title, Path: s.Path, Help: s.Help, Fields: fields}
	}
	return out
}

// writeConfig writes stack.yaml whole or not at all.
func writeConfig(path string, data []byte) error {
	return atomicfile.Write(path, data, 0o644)
}
