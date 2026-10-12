package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// designHome is the home screen of the design: twinmind looked at, mailbox
// not yet.
func designHome(width, height int) *Home {
	rows := []ProjectRow{
		{Name: "twinmind", Stack: "hashi v0.22.1", Server: "169.58.210.131",
			Apps: []string{"postgres", "nats", "seaweedfs", "loki", "mimir", "grafana", "odoo", "sauron"}, Backup: "03:00", BackupOK: true},
		{Name: "mailbox", Stack: "hashi v0.19.2", Server: "95.217.4.18", Apps: []string{"mail"}, Backup: "03:00", BackupOK: true},
	}
	m := NewHome("0.13.0", true, rows, "twinmind", start, nil)
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	var apps []AppState
	for _, a := range rows[0].Apps {
		apps = append(apps, AppState{Name: a, State: Healthy})
	}
	apps[7].Text = "7/7"
	m.Update(lookedMsg{"twinmind", ProjectState{State: Healthy, Text: "all healthy", Apps: apps,
		Notes: []string{"deploy  last 2h ago", "keys    1 of 12 set · damstack app keys sauron"},
		Needs: []Need{
			{Text: "3 secrets seen in plain text", Urgent: true, Key: "!"},
			{Text: "backups never restored", Urgent: true},
			{Text: "2 changes wait for a deploy", Key: "d"},
		}}, start})
	return m
}

func TestTheHomeScreenAtTheSizesItIsMadeFor(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		w, h := size[0], size[1]
		frame := designHome(w, h).Frame()
		checkShape(t, frame, w, h)
		golden(t, fmt.Sprintf("home-%dx%d", w, h), frame)
	}
}

func TestAHomeKeyChoosesWhatToDoWithTheProject(t *testing.T) {
	m := designHome(100, 30)
	press(m, "down")
	if cmd := press(m, "d"); cmd == nil || m.Action() != (HomeAction{Key: "d", Project: "mailbox"}) {
		t.Errorf("d must deploy the chosen project and close the screen: %+v", m.Action())
	}
	m = designHome(100, 30)
	press(m, "k")
	if m.Action().Key != "k" || m.cursor != 0 {
		t.Errorf("k is keys, not up: %+v, cursor %d", m.Action(), m.cursor)
	}
	m = designHome(100, 30)
	press(m, "q")
	if m.Action() != (HomeAction{}) {
		t.Errorf("q quits with nothing to do: %+v", m.Action())
	}
}

func TestANeedOfAnotherProjectSaysWhichProject(t *testing.T) {
	m := designHome(100, 30)
	m.Update(lookedMsg{"mailbox", ProjectState{State: Warning, Text: "1 warning", Needs: []Need{{Text: "hashi v0.22.1 is out"}}}, start})
	if !strings.Contains(m.Frame(), "mailbox: hashi v0.22.1 is out") {
		t.Error("a need of mailbox, with twinmind chosen, is named after it")
	}
}
