package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func designSources(width, height int, actions SourceActions) *Sources {
	m := NewSources([]SourceRow{
		{Name: "eugene-panin/damstack-library", Builtin: true, Apps: []string{"mail", "odoo", "postgres", "nats", "seaweedfs", "loki", "mimir", "grafana"}, Synced: start.Add(-2 * time.Minute)},
		{Name: "twinmind-pro", Private: true, Login: "eugene-panin", Apps: []string{"sauron"}, Synced: start.Add(-2 * time.Minute)},
	}, start, actions)
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

func TestTheSourcesScreenAtTheSizesItIsMadeFor(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		w, h := size[0], size[1]
		m := designSources(w, h, SourceActions{})
		press(m, "down")
		frame := m.Frame()
		checkShape(t, frame, w, h)
		golden(t, fmt.Sprintf("sources-%dx%d", w, h), frame)
	}
}

func TestASourceIsAddedCheckedAndRemoved(t *testing.T) {
	var removed string
	m := designSources(100, 30, SourceActions{
		Sync: func(name string) SourceRow {
			return SourceRow{Name: name, Private: true, Login: "eugene-panin", Apps: []string{"billing", "crm"}, Synced: start}
		},
		Remove: func(name string) error { removed = name; return nil },
	})
	press(m, "a")
	m.Update(tea.PasteMsg{Content: "https://github.com/acme/damstack-apps"})
	cmd := press(m, "enter")
	if !m.syncing["acme/damstack-apps"] {
		t.Fatalf("↵ checks the source, its address made owner/name: %v", m.syncing)
	}
	m.Update(cmd())
	if len(m.rows) != 3 || m.cursor != 2 || !strings.Contains(m.flash, "2 apps") {
		t.Fatalf("a source checked is listed with what it has: %v %q", m.rows, m.flash)
	}
	press(m, "a")
	m.Update(tea.PasteMsg{Content: "twinmind-pro"})
	if press(m, "enter"); !strings.Contains(m.flash, "a source already") {
		t.Errorf("a source is added once: %q", m.flash)
	}
	press(m, "d")
	if removed != "" || !strings.Contains(m.flash, "d again") {
		t.Fatalf("the first d only asks: %q", m.flash)
	}
	press(m, "d")
	if removed != "acme/damstack-apps" || len(m.rows) != 2 {
		t.Errorf("the second d removes it: %q", removed)
	}
	press(m, "up", "up", "d")
	if !strings.Contains(m.flash, "library stays") {
		t.Errorf("the library is not removed: %q", m.flash)
	}
}
