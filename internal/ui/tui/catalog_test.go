package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func designCatalog(width, height int) *Catalog {
	m := NewCatalog("twinmind", "hashi v0.22.1", []CatalogApp{
		{Name: "mail", Description: "your own mail server, by Stalwart", Source: "eugene-panin/damstack-mail", Latest: "v0.4.1"},
		{Name: "odoo", Description: "Odoo ERP", Source: "eugene-panin/damstack-odoo", Installed: "v0.3.10", Latest: "v0.3.11"},
		{Name: "grafana", Description: "Grafana", Source: "eugene-panin/damstack-grafana", Installed: "v0.1.6", Latest: "v0.1.6"},
		{Name: "sauron", Description: "the crawler and its console", Source: "twinmind-pro/damstack-sauron", Installed: "v0.1.19", Private: true},
	}, CatalogActions{})
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m.Update(detailsMsg{"mail", CatalogDetails{
		Needs: []FormNeed{{Text: "platform hashi, which provides nomad", Met: true}, {Text: "public ports 25 465 587 993, opened on install", Met: true}},
		Notes: []string{"takes about 4 minutes"},
	}})
	return m
}

func TestTheCatalogAtTheSizesItIsMadeFor(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		w, h := size[0], size[1]
		frame := designCatalog(w, h).Frame()
		checkShape(t, frame, w, h)
		golden(t, fmt.Sprintf("catalog-%dx%d", w, h), frame)
	}
}

func TestTheCatalogChoosesAnAppToInstallOrUpdate(t *testing.T) {
	m := designCatalog(100, 30)
	if a, _ := m.chosen(); a.Name != "mail" {
		t.Fatalf("an app to install comes first: %s", a.Name)
	}
	press(m, "i")
	if c := m.Choice(); c.Key != "i" || c.App.Name != "mail" {
		t.Errorf("i installs the chosen app: %+v", c)
	}
	m = designCatalog(100, 30)
	press(m, "down", "i")
	if m.Choice().Key != "" || !strings.Contains(m.flash, "u updates it") {
		t.Errorf("an installed app is not installed again; an update is offered: %q", m.flash)
	}
	press(m, "u")
	if c := m.Choice(); c.Key != "u" || c.App.Name != "odoo" {
		t.Errorf("u updates an app with a newer release: %+v", c)
	}
	m = designCatalog(100, 30)
	press(m, "/")
	m.Update(tea.PasteMsg{Content: "graf"})
	if s := m.shown(); len(s) != 1 || s[0].Name != "grafana" {
		t.Errorf("/ filters by name and description: %v", s)
	}
	if !newer("v0.3.11", "v0.3.9") || newer("v0.1.6", "v0.1.6") {
		t.Error("releases compare as numbers")
	}
}
