package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func designLinks(width, height int, actions LinkActions) *Links {
	m := NewLinks("twinmind", []Link{
		{URL: "https://nomad.twinmind.damstack.dev", Admin: true, Tunnel: true, Token: "nomad"},
		{URL: "https://vault.twinmind.damstack.dev", Admin: true, Tunnel: true, Token: "vault"},
		{URL: "https://odoo-demo.twinmind.pro"},
		{URL: "https://sauron.twinmind.pro"},
		{URL: "https://sauron.twinmind.damstack.dev", Tunnel: true},
	}, actions)
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m.Update(tunnelMsg(true))
	for u, a := range map[string]answer{
		"https://odoo-demo.twinmind.pro":       {"200", 300 * time.Millisecond},
		"https://sauron.twinmind.pro":          {"no DNS record", 0},
		"https://sauron.twinmind.damstack.dev": {"200", 100 * time.Millisecond},
		"https://nomad.twinmind.damstack.dev":  {"200", 100 * time.Millisecond},
		"https://vault.twinmind.damstack.dev":  {"307", 100 * time.Millisecond},
	} {
		m.Update(answeredMsg{u, a, start})
	}
	return m
}

func TestTheLinksScreenAtTheSizesItIsMadeFor(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		w, h := size[0], size[1]
		frame := designLinks(w, h, LinkActions{}).Frame()
		checkShape(t, frame, w, h)
		golden(t, fmt.Sprintf("links-%dx%d", w, h), frame)
	}
}

func TestALinkOpensAndItsTokenIsCopied(t *testing.T) {
	var opened, token string
	m := designLinks(100, 30, LinkActions{
		Open:      func(u string) error { opened = u; return nil },
		CopyToken: func(n string) error { token = n; return nil },
	})
	if m.links[0].URL != "https://odoo-demo.twinmind.pro" {
		t.Fatalf("the apps come first: %v", m.links[0])
	}
	m.Update(press(m, "enter")())
	if opened != "https://odoo-demo.twinmind.pro" {
		t.Errorf("↵ must open the chosen address: %q", opened)
	}
	press(m, "down", "down", "down", "t")
	if token != "nomad" {
		t.Errorf("t must copy the token of a page: %q", token)
	}
	m.Update(tunnelMsg(false))
	opened = ""
	if cmd := press(m, "enter"); cmd != nil || opened != "" || !strings.Contains(m.flash, "tunnel is off") {
		t.Errorf("a tunnel address with the tunnel off says so instead: %q", m.flash)
	}
}
