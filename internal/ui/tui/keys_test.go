package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// sauronKeys are the keys of the design's screen.
func sauronKeys() []Key {
	return []Key{
		{Name: "sauron_anthropic_api_key", Label: "Anthropic API key", UsedBy: []string{"enrich", "incidents"}, Set: true,
			Changed: start.Add(-72 * time.Hour), Optional: true, NewAt: "https://console.anthropic.com/settings/keys"},
		{Name: "sauron_telegram_bot_token", Label: "Telegram bot token", UsedBy: []string{"fetcher"}, Optional: true, NewAt: "https://t.me/BotFather"},
		{Name: "sauron_xai_api_key", Label: "xAI API key", UsedBy: []string{"enrich", "incidents"}, Optional: true, NewAt: "https://console.x.ai"},
	}
}

func keysScreen(width, height int, actions KeyActions) *AppKeys {
	m := NewAppKeys("sauron", "twinmind-pro/damstack-sauron · v0.1.19", sauronKeys(), 0, start, actions)
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

func TestTheKeysScreenAtTheSizesItIsMadeFor(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		w, h := size[0], size[1]
		m := keysScreen(w, h, KeyActions{})
		frame := m.Frame()
		checkShape(t, frame, w, h)
		golden(t, fmt.Sprintf("keys-%dx%d", w, h), frame)

		press(m, "enter")
		m.Update(tea.PasteMsg{Content: "sk-ant-api03-secret"})
		frame = m.Frame()
		checkShape(t, frame, w, h)
		golden(t, fmt.Sprintf("keys-replace-%dx%d", w, h), frame)
	}
}

func TestAKeyIsSetWithoutBeingShown(t *testing.T) {
	var name, value, opened string
	m := keysScreen(100, 30, KeyActions{
		Set:  func(n, v string) error { name, value = n, v; return nil },
		Open: func(u string) error { opened = u; return nil },
	})
	press(m, "down", "down")
	m.Update(press(m, "?")())
	if opened != "https://console.x.ai" {
		t.Errorf("? must open where the key is made: %q", opened)
	}
	press(m, "enter")
	m.Update(tea.PasteMsg{Content: "xai-secret\n"})
	if strings.Contains(m.Frame(), "xai-secret") {
		t.Error("a key must never be shown")
	}
	if !strings.Contains(m.Frame(), "enrich and incidents restart") {
		t.Error("the box says which services restart")
	}
	m.Update(press(m, "enter")())
	if name != "sauron_xai_api_key" || value != "xai-secret" || m.editing {
		t.Errorf("enter must keep the key: %q %q", name, value)
	}
	if k := m.keys[2]; !k.Set || !k.Pending || !strings.Contains(m.Frame(), "to deploy") {
		t.Errorf("a key kept is set, waiting for a deploy: %+v", k)
	}
	if press(m, "d"); !m.Deploy() {
		t.Error("d must ask for a deploy")
	}
}

func TestAKeyIsRemovedOnTheSecondX(t *testing.T) {
	removed := ""
	m := keysScreen(100, 30, KeyActions{Unset: func(n string) error { removed = n; return nil }})
	press(m, "x")
	if removed != "" || !strings.Contains(m.flash, "x again") {
		t.Fatalf("the first x must only ask: %q", m.flash)
	}
	press(m, "x")
	if removed != "sauron_anthropic_api_key" || m.keys[0].Set {
		t.Errorf("the second x must remove it: %q", removed)
	}
}
