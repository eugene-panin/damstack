package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The sources screen (design: 7b · Sources): the git repositories damstack
// takes apps from, how it reads them, and adding one, checked first.

// SourceRow is a source as the screen lists it.
type SourceRow struct {
	Name    string
	Private bool
	// Login is whose sign-in reads it; Builtin is the library itself.
	Login   string
	Builtin bool
	Apps    []string
	Synced  time.Time
	Err     string
}

// SourceActions are what the sources screen does outside it.
type SourceActions struct {
	// Sync reads a source again, or for the first time, and keeps it.
	Sync   func(name string) SourceRow
	Remove func(name string) error
}

// Sources is the sources screen.
type Sources struct {
	actions SourceActions
	rows    []SourceRow
	now     time.Time
	width   int
	height  int
	cursor  int
	adding  bool
	input   string
	syncing map[string]bool
	confirm string
	flash   string
	catalog bool
}

// NewSources is the screen of the sources rows.
func NewSources(rows []SourceRow, now time.Time, actions SourceActions) *Sources {
	return &Sources{actions: actions, rows: rows, now: now, syncing: map[string]bool{}, width: 100, height: 30}
}

// Catalog is whether the person asked for the catalog on leaving.
func (m *Sources) Catalog() bool { return m.catalog }

type syncedMsg SourceRow

func (m *Sources) sync(name string) tea.Cmd {
	if m.actions.Sync == nil {
		return nil
	}
	m.syncing[name] = true
	return func() tea.Msg { return syncedMsg(m.actions.Sync(name)) }
}

func (m *Sources) Init() tea.Cmd { return nil }

func (m *Sources) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case syncedMsg:
		r := SourceRow(msg)
		delete(m.syncing, r.Name)
		found := false
		for i := range m.rows {
			if m.rows[i].Name == r.Name {
				m.rows[i], found = r, true
			}
		}
		if !found {
			m.rows = append(m.rows, r)
			m.cursor = len(m.rows) - 1
		}
		m.now = r.Synced
		if r.Err != "" {
			m.flash = r.Name + ": " + r.Err
		} else {
			m.flash = fmt.Sprintf("%s: %s", r.Name, plural(len(r.Apps), "app"))
		}
	case tea.PasteMsg:
		if m.adding {
			m.input += strings.TrimSpace(msg.Content)
		}
	case tea.KeyPressMsg:
		if m.adding {
			return m, m.addKey(msg)
		}
		return m, m.key(msg.String())
	}
	return m, nil
}

func (m *Sources) addKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.adding, m.input = false, ""
	case "enter":
		name := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(m.input), "https://github.com/"), "/")
		m.adding, m.input = false, ""
		if name == "" {
			return nil
		}
		for _, r := range m.rows {
			if r.Name == name {
				m.flash = name + " is a source already"
				return nil
			}
		}
		return m.sync(name)
	case "backspace":
		if r := []rune(m.input); len(r) > 0 {
			m.input = string(r[:len(r)-1])
		}
	default:
		if msg.Text != "" {
			m.input += msg.Text
		}
	}
	return nil
}

func (m *Sources) key(k string) tea.Cmd {
	m.flash = ""
	if k != "d" {
		m.confirm = ""
	}
	switch k {
	case "ctrl+c", "esc", "q":
		return tea.Quit
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j":
		m.cursor = min(m.cursor+1, max(len(m.rows)-1, 0))
	case "a":
		m.adding = true
	case "r":
		var cmds []tea.Cmd
		for _, r := range m.rows {
			if !r.Builtin {
				cmds = append(cmds, m.sync(r.Name))
			}
		}
		return tea.Batch(cmds...)
	case "c":
		m.catalog = true
		return tea.Quit
	case "d":
		if len(m.rows) == 0 {
			return nil
		}
		r := m.rows[m.cursor]
		switch {
		case r.Builtin:
			m.flash = "the library stays: it is damstack's own"
		case m.confirm != r.Name:
			m.confirm = r.Name
			m.flash = "d again removes " + r.Name + "; installed apps from it stay"
		default:
			if m.actions.Remove != nil {
				if err := m.actions.Remove(r.Name); err != nil {
					m.flash = err.Error()
					return nil
				}
			}
			m.rows = append(m.rows[:m.cursor], m.rows[m.cursor+1:]...)
			m.cursor = max(min(m.cursor, len(m.rows)-1), 0)
			m.confirm, m.flash = "", r.Name+" is no longer a source"
		}
	}
	return nil
}

func (m *Sources) View() tea.View {
	v := tea.NewView(m.Frame())
	v.AltScreen = true
	v.BackgroundColor = colorBg
	v.ForegroundColor = colorFg
	return v
}

// Frame is the screen's text: exactly height rows of exactly width cells.
func (m *Sources) Frame() string {
	w, h := m.width, m.height
	if w < minWidth || h < minHeight {
		return tooSmall(w, h)
	}
	inner := w - 4
	apps := 0
	for _, r := range m.rows {
		apps += len(r.Apps)
	}
	rows := []line{
		{left: []span{text("App sources", bold), text(" · git repositories damstack takes apps from", dim)},
			right: []span{text(fmt.Sprintf("%s · %s", plural(len(m.rows), "source"), plural(apps, "app")), dim)}},
		{left: []span{text(strings.Repeat("─", inner), rule)}},
		{},
		{left: []span{text("  "+padTo("source", 36), dim), text(padTo("access", 11), dim), text(padTo("auth", 18), dim), text(padTo("apps", 7), dim), text("synced", dim)}},
	}
	for i, r := range m.rows {
		rows = append(rows, m.sourceRow(i, r))
	}
	rows = append(rows, line{})
	if m.adding {
		rows = append(rows,
			line{left: []span{text("Add a source", bold)}},
			line{left: []span{text("owner/name, an owner for all its damstack-* repositories, or any git address", dim)}},
			line{left: []span{text(m.input, bold), text("▌", blue)}, bg: colorSelect},
			line{left: []span{text("Private ones on github.com are read with your gh login.", dim)}},
		)
	} else if len(m.rows) > 0 && m.cursor < len(m.rows) {
		r := m.rows[m.cursor]
		if len(r.Apps) > 0 {
			rows = append(rows, line{left: []span{text(r.Name+": ", dim), text(strings.Join(r.Apps, ", "), plain)}})
		}
		if r.Err != "" {
			rows = append(rows, line{left: []span{text("✗ "+r.Err, red)}})
		}
	}
	bottom := []line{
		{left: []span{text("An app runs on your server with your secrets: add sources you trust. Apps are pinned by tag and commit.", dim)}},
		{left: []span{text(strings.Repeat("─", inner), rule)}},
		{left: []span{text("a", key), text(" add   ", dim), text("r", key), text(" sync now   ", dim), text("d", key), text(" remove   ", dim),
			text("c", key), text(" catalog   ", dim), text("esc", key), text(" back", dim)}},
	}
	if m.adding {
		bottom[2] = line{left: []span{text("↵", key), text(" add and check it   ", dim), text("esc", key), text(" cancel", dim)}}
	}
	if m.flash != "" {
		bottom[0] = line{left: []span{text(m.flash, blue)}}
	}
	if len(rows) > h-len(bottom) {
		rows = rows[:h-len(bottom)]
	}
	for len(rows) < h-len(bottom) {
		rows = append(rows, line{})
	}
	rows = append(rows, bottom...)
	out := make([]string, len(rows))
	margin := line{}.String(2)
	for i, r := range rows {
		out[i] = margin + r.String(inner) + margin
	}
	return strings.Join(out, "\n")
}

func (m *Sources) sourceRow(i int, r SourceRow) line {
	cursor := i == m.cursor
	mark, name := text("  ", plain), text(padTo(r.Name, 36), plain)
	if cursor {
		mark, name = text("› ", blue), text(padTo(r.Name, 36), bold)
	}
	access, auth := "public", "—"
	if r.Private {
		access, auth = "private", "gh "+orDash(r.Login, "login")
	}
	synced := text("—", dim)
	switch {
	case m.syncing[r.Name]:
		synced = text("syncing…", amber)
	case r.Err != "":
		synced = text("✗ failed", red)
	case !r.Synced.IsZero():
		synced = text("✓ "+ago(m.now, r.Synced), green)
	}
	l := line{left: []span{mark, name, text(padTo(access, 11), plain), text(padTo(auth, 18), plain), text(padTo(fmt.Sprint(len(r.Apps)), 7), plain), synced}}
	if cursor {
		l.bg = colorSelect
	}
	return l
}
