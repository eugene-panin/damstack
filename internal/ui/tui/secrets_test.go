package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// designSecrets are the secrets of the design's screen: three seen, one
// that cannot be made anew, and the rest.
func designSecrets() []SecretRow {
	day := 24 * time.Hour
	rows := []SecretRow{
		{Name: "sauron/operators/eugene", Label: "sauron operator passwords: eugene", Owner: "sauron", Kind: Accounts, Set: start.Add(-day), Seen: true, Takes: "no restart: Vault checks it at sign-in"},
		{Name: "vault_registry_password", Label: "registry token (ghcr)", Owner: "platform", Kind: Asked, Set: start.Add(-2 * day), Seen: true,
			NewAt: "https://github.com/settings/tokens/new?scopes=read:packages", Takes: "damstack deploy"},
		{Name: "vault_consul_acl_agent_token", Label: "consul agent token", Owner: "platform", Set: start.Add(-9 * day), Seen: true, Takes: "damstack deploy"},
		{Name: "postgres_superuser_password", Label: "postgres superuser", Owner: "postgres", Set: start.Add(-9 * day), Takes: "damstack deploy"},
		{Name: "vault_backup_password", Label: "backup password", Owner: "platform", Set: start.Add(-9 * day), Keep: "old backups need it"},
		{Name: "vault_stack_ca_key", Label: "stack ca key", Owner: "platform", Set: start.Add(-120 * day), Takes: "damstack deploy"},
	}
	for i := range 35 {
		rows = append(rows, SecretRow{Name: fmt.Sprintf("app_secret_%02d", i), Label: fmt.Sprintf("app secret %02d", i), Owner: "odoo", Set: start.Add(-9 * day)})
	}
	return rows
}

func secretsScreen(width, height int, actions SecretActions) *Secrets {
	m := NewSecrets("twinmind", designSecrets(), 90, start, actions)
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

func press(m tea.Model, keys ...string) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range keys {
		var msg tea.KeyPressMsg
		switch k {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "space":
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		case "down":
			msg = tea.KeyPressMsg{Code: tea.KeyDown}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "ctrl+c":
			msg = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
		default:
			msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
		}
		_, cmd = m.Update(msg)
	}
	return cmd
}

func TestTheSecretsScreenAtTheSizesItIsMadeFor(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		w, h := size[0], size[1]
		m := secretsScreen(w, h, SecretActions{})
		frame := m.Frame()
		checkShape(t, frame, w, h)
		golden(t, fmt.Sprintf("secrets-%dx%d", w, h), frame)

		press(m, "space", "space", "space", "enter")
		frame = m.Frame()
		checkShape(t, frame, w, h)
		golden(t, fmt.Sprintf("secrets-review-%dx%d", w, h), frame)
	}
}

func TestTheRiskiestSecretsComeFirst(t *testing.T) {
	m := secretsScreen(100, 30, SecretActions{})
	var order []string
	for _, r := range m.rows[:6] {
		order = append(order, r.Name)
	}
	want := []string{"vault_consul_acl_agent_token", "vault_registry_password", "sauron/operators/eugene", "vault_stack_ca_key"}
	if strings.Join(order[:4], " ") != strings.Join(want, " ") {
		t.Errorf("seen, oldest first, then old: got %v", order)
	}
	if last := m.rows[len(m.rows)-1]; last.Name != "vault_backup_password" {
		t.Errorf("one that cannot be made anew comes last, not %s", last.Name)
	}
}

func TestASecretThatCannotBeMadeAnewIsNotPicked(t *testing.T) {
	m := secretsScreen(100, 30, SecretActions{})
	m.cursor = m.index("vault_backup_password")
	press(m, "space")
	if m.picked["vault_backup_password"] || !strings.Contains(m.flash, "old backups need it") {
		t.Errorf("space must refuse and say why: %v %q", m.picked, m.flash)
	}
}

func TestRotatingAsksForTheNewValueThenMakesThemAnew(t *testing.T) {
	var opened, copied string
	var gotRows []SecretRow
	var gotValues map[string]string
	m := secretsScreen(100, 30, SecretActions{
		Open: func(u string) error { opened = u; return nil },
		Copy: func(s string) error { copied = s; return nil },
		Rotate: func(rows []SecretRow, values map[string]string) []Rotated {
			gotRows, gotValues = rows, values
			var out []Rotated
			for _, r := range rows {
				res := Rotated{Name: r.Name}
				if r.Kind == Accounts {
					res.Passwords = []Password{{"aizi", "pw-aizi"}}
				}
				out = append(out, res)
			}
			return out
		},
	})
	cmd := press(m, "space", "space", "space", "enter")
	if m.phase != reviewing || m.review[m.asking].Name != "vault_registry_password" {
		t.Fatalf("enter must ask for the registry token first: phase %d", m.phase)
	}
	m.Update(cmd())
	if !strings.HasPrefix(opened, "https://github.com/settings/tokens") {
		t.Errorf("the page where the token is made must open: %q", opened)
	}
	m.Update(tea.PasteMsg{Content: "ghp_new\n"})
	if strings.Contains(m.Frame(), "ghp_new") {
		t.Error("a value must never be shown")
	}
	cmd = press(m, "enter")
	if m.asking != -1 || cmd != nil {
		t.Fatalf("no more to ask: asking %d", m.asking)
	}
	cmd = press(m, "enter")
	if m.phase != rotating {
		t.Fatalf("enter must rotate: phase %d", m.phase)
	}
	m.Update(cmd())
	if len(gotRows) != 3 || gotValues["vault_registry_password"] != "ghp_new" {
		t.Errorf("rotate got %d rows and %v", len(gotRows), gotValues)
	}
	if m.phase != rotated || m.rows[m.index("vault_registry_password")].Seen {
		t.Error("rotated secrets are no longer seen")
	}
	press(m, "enter")
	if copied != "pw-aizi" {
		t.Errorf("enter must copy the password: %q", copied)
	}
	frame := m.Frame()
	if strings.Contains(frame, "pw-aizi") || !strings.Contains(frame, "aizi") {
		t.Error("the names show, never the passwords")
	}
	if press(m, "d"); !m.Deploy() {
		t.Error("d must ask for a deploy: the registry token is in vault.yml")
	}
}

func TestASignInsPasswordAloneAsksForNoDeploy(t *testing.T) {
	m := secretsScreen(100, 30, SecretActions{
		Rotate: func(rows []SecretRow, _ map[string]string) []Rotated {
			return []Rotated{{Name: rows[0].Name, Passwords: []Password{{"eugene", "pw"}}}}
		},
	})
	m.cursor = m.index("sauron/operators/eugene")
	cmd := press(m, "space", "enter", "enter")
	m.Update(cmd())
	if m.phase != rotated {
		t.Fatalf("enter must rotate the one sign-in: phase %d", m.phase)
	}
	if strings.Contains(m.Frame(), "deploy now") {
		t.Error("a password set in Vault has nothing for a deploy to put in place")
	}
	press(m, "enter") // copied
	if press(m, "d"); m.Deploy() {
		t.Error("d must not deploy after only a sign-in's password")
	}
}

func TestThePolicySaysWhenASecretIsOld(t *testing.T) {
	kept := 0
	m := secretsScreen(100, 30, SecretActions{OldAfter: func(d int) error { kept = d; return nil }})
	press(m, "p")
	if kept != 180 || m.old(m.rows[m.index("vault_stack_ca_key")]) {
		t.Errorf("p must go from 90 to 180 days, and a 120-day secret is not old then: %d", kept)
	}
}

func TestCtrlCDoesNotLoseTheNewPasswords(t *testing.T) {
	m := secretsScreen(100, 30, SecretActions{
		Rotate: func(rows []SecretRow, _ map[string]string) []Rotated {
			return []Rotated{{Name: rows[0].Name, Passwords: []Password{{"eugene", "pw"}}}}
		},
	})
	m.cursor = m.index("sauron/operators/eugene")
	cmd := press(m, "space", "enter", "enter")
	if press(m, "ctrl+c") != nil {
		t.Fatal("ctrl+c while the passwords are made anew must ask again")
	}
	m.Update(cmd())
	if press(m, "ctrl+c") != nil {
		t.Fatal("ctrl+c before the new password is copied must ask again")
	}
	if _, ok := press(m, "ctrl+c")().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c twice must leave")
	}
}

func TestAnEmptyListTakesEveryKey(t *testing.T) {
	m := NewSecrets("twinmind", nil, 90, start, SecretActions{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	press(m, "j", "j", "k", "p", "space", "enter", "s")
	m.Frame()
}
