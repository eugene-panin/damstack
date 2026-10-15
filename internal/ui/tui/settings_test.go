package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/eugene-panin/damstack/internal/settings"
)

const designStack = `# Name of the stack, also the Consul and Nomad datacenter.
name: twinmind

server:
  # Public address of the server, as the provider gave it.
  address: 169.58.210.131
  # Name of the server in Consul, Nomad and Vault. Chosen once: renaming a running node
  # breaks the Vault raft.
  node_name: server
  # Ports open to the internet: 80 and 443 for Traefik.
  public_ports: [80, 443]

# Private network over WireGuard.
network:
  clients:
    - laptop
    - phone

# Vault seals itself every time it starts. true keeps its unseal keys on the server.
vault:
  auto_unseal: true

# The apps of the project.
apps:
  sauron:
    # The name the operator console is served on.
    console_hostname: sauron.twinmind.pro
`

var settingsKinds = map[settings.Kind]SettingsKind{settings.Text: SettingText, settings.Number: SettingNumber, settings.Toggle: SettingToggle,
	settings.List: SettingList, settings.Heading: SettingHeading, settings.Fixed: SettingFixed}

func settingsScreen(t *testing.T, width, height int) (*Settings, *settings.Doc) {
	t.Helper()
	doc, err := settings.Parse([]byte(designStack))
	if err != nil {
		t.Fatal(err)
	}
	sections := func() []SettingsSection {
		var out []SettingsSection
		for _, s := range doc.Sections() {
			var fields []SettingsField
			for _, f := range s.Fields {
				fields = append(fields, SettingsField{Path: f.Path, Label: f.Label, Help: f.Help, Kind: settingsKinds[f.Kind], Value: f.Value,
					Items: f.Items, Depth: f.Depth, Once: f.Once})
			}
			out = append(out, SettingsSection{Title: s.Title, Path: s.Path, Help: s.Help, Fields: fields})
		}
		return out
	}
	original := string(doc.Bytes())
	m := NewSettings("twinmind", SettingsActions{Sections: sections, Changed: func() bool { return string(doc.Bytes()) != original },
		Set: doc.Set, SetList: doc.SetList})
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m, doc
}

func TestTheSettingsScreenAtTheSizesItIsMadeFor(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		w, h := size[0], size[1]
		m, _ := settingsScreen(t, w, h)
		press(m, "tab")
		frame := m.Frame()
		checkShape(t, frame, w, h)
		golden(t, fmt.Sprintf("settings-%dx%d", w, h), frame)
	}
}

func TestASettingIsChangedOnTheScreen(t *testing.T) {
	m, doc := settingsScreen(t, 100, 30)
	press(m, "tab") // server
	press(m, "enter")
	for range len("169.58.210.131") {
		press(m, "backspace")
	}
	m.Update(tea.PasteMsg{Content: "5.6.7.8"})
	press(m, "enter")
	if !strings.Contains(string(doc.Bytes()), "  address: 5.6.7.8\n") || m.editing {
		t.Fatalf("enter keeps the typed value:\n%s", doc.Bytes())
	}
	press(m, "down")
	press(m, "enter")
	if !strings.Contains(m.flash, "chosen once") || m.editing {
		t.Errorf("a value chosen once is not changed here: %q", m.flash)
	}
	press(m, "down", "enter", "a")
	m.Update(tea.PasteMsg{Content: "25"})
	press(m, "enter")
	if !strings.Contains(string(doc.Bytes()), "public_ports: [80, 443, 25]") {
		t.Errorf("a adds an item:\n%s", doc.Bytes())
	}
	press(m, "x", "esc")
	if !strings.Contains(string(doc.Bytes()), "public_ports: [80, 443]") {
		t.Errorf("x removes the item chosen:\n%s", doc.Bytes())
	}
	press(m, "tab", "tab", "enter") // vault: auto_unseal
	if !strings.Contains(string(doc.Bytes()), "auto_unseal: false") {
		t.Errorf("↵ flips a yes or no:\n%s", doc.Bytes())
	}
	press(m, "q")
	if m.Result() != (SettingsResult{}) || !strings.Contains(m.flash, "q again") {
		t.Errorf("q with changes asks first: %q", m.flash)
	}
	press(m, "esc")
	if r := m.Result(); !r.Save || r.Deploy {
		t.Errorf("esc saves and leaves: %+v", r)
	}
}

func TestAWrongValueIsRefusedWithWhy(t *testing.T) {
	m, doc := settingsScreen(t, 100, 30)
	press(m, "tab", "down", "down", "enter", "a")
	m.Update(tea.PasteMsg{Content: "http"})
	press(m, "enter")
	if !strings.Contains(m.flash, "not a whole number") || strings.Contains(string(doc.Bytes()), "http") {
		t.Errorf("a list of numbers takes a number: %q", m.flash)
	}
}
