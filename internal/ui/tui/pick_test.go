package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func designPick(width, height int) *DeployPick {
	m := NewDeployPick("twinmind", []PickRow{
		{Name: "bootstrap", Title: "Securing the server", Note: "done once"},
		{Name: "provision", Title: "Consul, Vault, Nomad and WireGuard", Note: "unchanged since Oct 12 11:05"},
		{Name: "apply", Title: "Traefik and the admin pages", Note: "ran 2 h ago", Picked: true},
		{Name: "dns", Title: "DNS records", Note: "ran 2 h ago", Picked: true},
		{Name: "odoo", Title: "odoo  v0.3.10", App: true, Note: "deployed 2 h ago", Picked: true},
		{Name: "sauron", Title: "sauron  v0.1.19", App: true, Note: "keys changed", Changed: true, Picked: true},
	})
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

func TestTheDeployChoiceAtTheSizesItIsMadeFor(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		w, h := size[0], size[1]
		frame := designPick(w, h).Frame()
		checkShape(t, frame, w, h)
		golden(t, fmt.Sprintf("pick-%dx%d", w, h), frame)
	}
}

func TestOnlyWhatIsPickedIsDeployed(t *testing.T) {
	m := designPick(100, 30)
	press(m, "c", "enter")
	if names, ok := m.Picked(); !ok || strings.Join(names, " ") != "sauron" {
		t.Errorf("c picks what changed: %v %v", names, ok)
	}
	m = designPick(100, 30)
	press(m, "a")
	if names, _ := m.Picked(); len(names) != 6 {
		t.Errorf("a picks all when some are not: %v", names)
	}
	press(m, "a")
	if cmd := press(m, "enter"); cmd != nil || !strings.Contains(m.flash, "space picks") {
		t.Error("nothing picked deploys nothing, and says so")
	}
	m = designPick(100, 30)
	press(m, "esc")
	if _, ok := m.Picked(); ok {
		t.Error("esc deploys nothing")
	}
}
