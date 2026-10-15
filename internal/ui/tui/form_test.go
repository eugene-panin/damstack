package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/eugene-panin/damstack/internal/ask"
	"github.com/eugene-panin/damstack/internal/manifest"
)

var mailQuestions = []manifest.Question{
	{Name: "domains", Prompt: "Mail domains", Type: "list", Section: "Domains", Pattern: `^[a-z0-9.-]+$`},
	{Name: "hostname", Prompt: "Host name of the server", Section: "Domains", Default: "mail.{{ index .domains 0 }}"},
	{Name: "database", Prompt: "Database", Type: "enum", Options: []string{"shared", "bundled"}, Default: "shared", Section: "Domains"},
	{Name: "memory", Prompt: "Memory, in MB", Default: "512", Section: "Domains", Advanced: true},
	{Name: "accounts", Prompt: "Mailboxes to make", Type: "list", Section: "Accounts", Default: "postmaster"},
}

func mailQuestion(name string) manifest.Question {
	for _, q := range mailQuestions {
		if q.Name == name {
			return q
		}
	}
	return manifest.Question{}
}

// mailForm is the design's install of mail: domains, the host name, the
// database, an advanced memory, and a key.
func mailForm(width, height int, actions FormActions) *Form {
	var qs []FormQuestion
	for _, q := range mailQuestions {
		qs = append(qs, FormQuestion{Name: q.Name, Prompt: q.Prompt, Type: q.Type, Options: q.Options, Section: q.Section, Advanced: q.Advanced})
	}
	secrets := []FormSecret{
		{Name: "mail_admin_password"},
		{Name: "mail_relay_key", Ask: "Relay API key (Mailgun), for mail to big providers", Optional: true, NewAt: "https://app.mailgun.com"},
	}
	actions.Parse = func(q FormQuestion, raw string) (any, error) { return ask.Parse(mailQuestion(q.Name), raw) }
	actions.Holds = manifest.Holds
	m := NewForm("Install mail v0.4.1 on twinmind", "mail",
		[]FormNeed{{Text: "postgres, installed", Met: true}, {Text: "public ports 25 465 587 993, opened on install", Met: true}},
		[]string{"takes about 4 minutes", "nothing changes until you confirm the review"}, qs, secrets, actions)
	m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return m
}

// settle runs the check a tab started.
func settle(m *Form, cmd tea.Cmd) {
	if cmd != nil {
		m.Update(cmd())
	}
}

func defaults(fq FormQuestion, answers map[string]any) string {
	q := mailQuestion(fq.Name)
	if q.Name == "hostname" {
		if d, ok := answers["domains"].([]any); ok && len(d) > 0 {
			return fmt.Sprint("mail.", d[0])
		}
		return ""
	}
	if q.Default == nil {
		return ""
	}
	return fmt.Sprint(q.Default)
}

func TestTheFormAtTheSizesItIsMadeFor(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		w, h := size[0], size[1]
		m := mailForm(w, h, FormActions{Default: defaults})
		settle(m, press(m, "tab"))
		m.Update(tea.PasteMsg{Content: "twinmind.pro, newo.ai"})
		frame := m.Frame()
		checkShape(t, frame, w, h)
		golden(t, fmt.Sprintf("form-%dx%d", w, h), frame)
	}
}

func TestAnInstallIsAskedPageByPageThenReviewed(t *testing.T) {
	var checked []string
	m := mailForm(100, 30, FormActions{
		Default: defaults,
		Check: func(q FormQuestion, v any, _ map[string]any) error {
			checked = append(checked, q.Name)
			if q.Name == "hostname" && v == "mail.example.com" {
				return errors.New("port 25 is closed at your provider")
			}
			return nil
		},
	})
	settle(m, press(m, "tab")) // what it needs → domains
	if m.page != 1 {
		t.Fatalf("tab leaves what it needs: page %d", m.page)
	}
	settle(m, press(m, "tab"))
	if m.page != 1 || !strings.Contains(m.errs["domains"], "give at least one") {
		t.Fatalf("an empty list does not pass: %v", m.errs)
	}
	m.Update(tea.PasteMsg{Content: "example.com"})
	press(m, "down")
	if m.raw["hostname"] != "mail.example.com" {
		t.Errorf("an empty question takes its default when it is reached, from the answers before it: %q", m.raw["hostname"])
	}
	press(m, "down", "right")
	if m.raw["database"] != "bundled" {
		t.Errorf("→ chooses the next option: %q", m.raw["database"])
	}
	settle(m, press(m, "tab"))
	if m.page != 1 || m.errs["hostname"] == "" {
		t.Fatalf("a check that fails keeps the page, in red: %v", m.errs)
	}
	press(m, "up")
	for range len("mail.example.com") {
		press(m, "backspace")
	}
	m.Update(tea.PasteMsg{Content: "mx.example.com"})
	settle(m, press(m, "tab"))
	if m.page != 2 || !strings.Contains(strings.Join(checked, " "), "memory") {
		t.Fatalf("an advanced question not shown is checked too, with its default: page %d, %v", m.page, checked)
	}
	settle(m, press(m, "tab")) // accounts, default
	settle(m, press(m, "tab")) // keys: the optional one left unset
	if m.pages[m.page].kind != reviewPage || !strings.Contains(m.Frame(), "mail only") {
		t.Fatalf("the review comes last: page %d", m.page)
	}
	press(m, "enter")
	answers, keys, ok := m.Result()
	if !ok || answers["hostname"] != "mx.example.com" || answers["database"] != "bundled" || answers["memory"] != "512" || len(keys) != 0 {
		t.Errorf("↵ on the review installs with what was given: %v %v %v", answers, keys, ok)
	}
	if d, _ := answers["domains"].([]any); len(d) != 1 || d[0] != "example.com" {
		t.Errorf("a list is items: %v", answers["domains"])
	}
}

func TestAKeyIsNeverShownAndANeedNotMetStopsTheInstall(t *testing.T) {
	m := mailForm(100, 30, FormActions{Default: defaults})
	m.page = len(m.pages) - 2
	m.Update(tea.PasteMsg{Content: "key-1234"})
	if strings.Contains(m.Frame(), "key-1234") || !strings.Contains(m.Frame(), "••••••••") {
		t.Error("a key shows as dots")
	}
	m = mailForm(100, 30, FormActions{})
	m.needs[1].Met = false
	if press(m, "tab"); m.page != 0 || !strings.Contains(m.flash, "cannot be installed") {
		t.Errorf("a need not met keeps the install from going on: %q", m.flash)
	}
	press(m, "esc")
	if _, _, ok := m.Result(); ok {
		t.Error("esc installs nothing")
	}
}
