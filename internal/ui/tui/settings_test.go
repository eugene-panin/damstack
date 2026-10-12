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

func settingsScreen(t *testing.T, width, height int) *Settings {
	t.Helper()
	doc, err := settings.Parse([]byte(designStack))
	if err != nil {
		t.Fatal(err)
	}
	m := NewSettings("twinmind", doc)
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

func TestTheSettingsScreenAtTheSizesItIsMadeFor(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		w, h := size[0], size[1]
		m := settingsScreen(t, w, h)
		press(m, "tab")
		frame := m.Frame()
		checkShape(t, frame, w, h)
		golden(t, fmt.Sprintf("settings-%dx%d", w, h), frame)
	}
}

func TestASettingIsChangedOnTheScreen(t *testing.T) {
	m := settingsScreen(t, 100, 30)
	press(m, "tab") // server
	press(m, "enter")
	for range len("169.58.210.131") {
		press(m, "backspace")
	}
	m.Update(tea.PasteMsg{Content: "5.6.7.8"})
	press(m, "enter")
	if !strings.Contains(string(m.Doc().Bytes()), "  address: 5.6.7.8\n") || m.editing {
		t.Fatalf("enter keeps the typed value:\n%s", m.Doc().Bytes())
	}
	press(m, "down")
	press(m, "enter")
	if !strings.Contains(m.flash, "chosen once") || m.editing {
		t.Errorf("a value chosen once is not changed here: %q", m.flash)
	}
	press(m, "down", "enter", "a")
	m.Update(tea.PasteMsg{Content: "25"})
	press(m, "enter")
	if !strings.Contains(string(m.Doc().Bytes()), "public_ports: [80, 443, 25]") {
		t.Errorf("a adds an item:\n%s", m.Doc().Bytes())
	}
	press(m, "x", "esc")
	if !strings.Contains(string(m.Doc().Bytes()), "public_ports: [80, 443]") {
		t.Errorf("x removes the item chosen:\n%s", m.Doc().Bytes())
	}
	press(m, "tab", "tab", "enter") // vault: auto_unseal
	if !strings.Contains(string(m.Doc().Bytes()), "auto_unseal: false") {
		t.Errorf("↵ flips a yes or no:\n%s", m.Doc().Bytes())
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
	m := settingsScreen(t, 100, 30)
	press(m, "tab", "down", "down", "enter", "a")
	m.Update(tea.PasteMsg{Content: "http"})
	press(m, "enter")
	if !strings.Contains(m.flash, "not a whole number") || strings.Contains(string(m.Doc().Bytes()), "http") {
		t.Errorf("a list of numbers takes a number: %q", m.flash)
	}
}
