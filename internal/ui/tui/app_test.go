package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func designApp(width, height int, actions AppActions) *App {
	m := NewApp("twinmind", "sauron", actions)
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m.Update(appLookedMsg{AppView{
		Source: "twinmind-pro/damstack-sauron", Version: "v0.1.19", Latest: "v0.1.20",
		Groups: []AppGroup{
			{Job: "sauron", Name: "console", Running: 1, Count: 1, MemoryUsed: 310, Memory: 384, Healthy: 1},
			{Job: "sauron", Name: "fetcher", Running: 1, Count: 1, MemoryUsed: 1126, Memory: 2355, Restarts: 3, Healthy: 1},
			{Job: "sauron", Name: "enrich", Running: 0, Count: 1, Memory: 512},
			{Job: "databases", Name: "init", Batch: true},
		},
		Links: []Link{{URL: "https://sauron.twinmind.pro"}, {URL: "https://sauron.twinmind.damstack.dev", Tunnel: true}},
	}, start})
	m.Update(answeredMsg{"https://sauron.twinmind.pro", answer{"no DNS record", 0}, start})
	m.Update(answeredMsg{"https://sauron.twinmind.damstack.dev", answer{"200", time.Second / 10}, start})
	return m
}

func TestTheAppScreenAtTheSizesItIsMadeFor(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		w, h := size[0], size[1]
		m := designApp(w, h, AppActions{})
		frame := m.Frame()
		checkShape(t, frame, w, h)
		golden(t, fmt.Sprintf("app-%dx%d", w, h), frame)

		press(m, "down", "l")
		m.Update(appLogsMsg{"fetch 511ny: 200, 41 events", "fetch openmhz: 429, backing off 30s"})
		frame = m.Frame()
		checkShape(t, frame, w, h)
		golden(t, fmt.Sprintf("app-logs-%dx%d", w, h), frame)
	}
}

func TestAGroupIsRestartedOnTheSecondR(t *testing.T) {
	var restarted []string
	m := designApp(100, 30, AppActions{Restart: func(g AppGroup) error { restarted = append(restarted, g.Name); return nil }})
	press(m, "down")
	if cmd := press(m, "r"); cmd != nil || !strings.Contains(m.flash, "r again") {
		t.Fatalf("the first r only asks: %q", m.flash)
	}
	m.Update(press(m, "r")())
	if strings.Join(restarted, " ") != "fetcher" || !strings.Contains(m.flash, "fetcher restarted") {
		t.Errorf("the second r restarts the group chosen: %v %q", restarted, m.flash)
	}
	restarted = nil
	press(m, "R")
	m.Update(press(m, "R")())
	if strings.Join(restarted, " ") != "console fetcher enrich" {
		t.Errorf("R restarts every group that runs, not a batch done: %v", restarted)
	}
	if press(m, "u"); m.Choice() != "u" {
		t.Error("u updates to the newer release")
	}
}
