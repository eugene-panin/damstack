package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// The deploy choice: what of a project a deploy puts in place, the platform's
// steps one by one and each app, before the deploy screen.

// PickRow is a part of a deploy that may be chosen: a step of the platform,
// or an app with all its steps.
type PickRow struct {
	Name  string
	Title string
	App   bool
	// Note says what is known of it, such as unchanged since Oct 12, and
	// Changed that it waits for a deploy.
	Note    string
	Changed bool
	Picked  bool
}

// DeployPick is the screen of what to deploy.
type DeployPick struct {
	project string
	rows    []PickRow
	width   int
	height  int
	cursor  int
	flash   string
	done    bool
}

// NewDeployPick is the choice of what of project to deploy, rows picked as
// they come.
func NewDeployPick(project string, rows []PickRow) *DeployPick {
	return &DeployPick{project: project, rows: rows, width: 100, height: 30}
}

// Picked are the names chosen, and whether the person asked to deploy them.
func (m *DeployPick) Picked() ([]string, bool) {
	var out []string
	for _, r := range m.rows {
		if r.Picked {
			out = append(out, r.Name)
		}
	}
	return out, m.done
}

func (m *DeployPick) Init() tea.Cmd { return nil }

func (m *DeployPick) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		m.flash = ""
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			return m, tea.Quit
		case "up", "k":
			m.cursor = max(m.cursor-1, 0)
		case "down", "j":
			m.cursor = min(m.cursor+1, len(m.rows)-1)
		case "space", " ":
			m.rows[m.cursor].Picked = !m.rows[m.cursor].Picked
			m.cursor = min(m.cursor+1, len(m.rows)-1)
		case "a":
			all := true
			for _, r := range m.rows {
				all = all && r.Picked
			}
			for i := range m.rows {
				m.rows[i].Picked = !all
			}
		case "p":
			for i := range m.rows {
				m.rows[i].Picked = !m.rows[i].App
			}
		case "c":
			for i := range m.rows {
				m.rows[i].Picked = m.rows[i].Changed
			}
		case "enter":
			if names, _ := m.Picked(); len(names) == 0 {
				m.flash = "space picks what to deploy"
				return m, nil
			}
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *DeployPick) View() tea.View {
	v := tea.NewView(m.Frame())
	v.AltScreen = true
	v.BackgroundColor = colorBg
	v.ForegroundColor = colorFg
	return v
}

// Frame is the screen's text: exactly height rows of exactly width cells.
func (m *DeployPick) Frame() string {
	w, h := m.width, m.height
	if w < minWidth || h < minHeight {
		return tooSmall(w, h)
	}
	inner := w - 4
	picked, _ := m.Picked()
	rows := []line{
		{left: []span{text("Deploy", bold), text(" · "+m.project+" · what to put in place", dim)},
			right: []span{text(plural(len(picked), "part")+" picked", blue)}},
		{left: []span{text(strings.Repeat("─", inner), rule)}},
		{},
	}
	section := ""
	for i, r := range m.rows {
		if s := map[bool]string{false: "platform", true: "apps"}[r.App]; s != section {
			if section != "" {
				rows = append(rows, line{})
			}
			section = s
			rows = append(rows, line{left: []span{text(s, dim)}})
		}
		box := text("[ ]", dim)
		if r.Picked {
			box = text("[x]", blue)
		}
		name := text(padTo(r.Title, 40), plain)
		if i == m.cursor {
			name = text(padTo(r.Title, 40), bold)
		}
		note := text(r.Note, dim)
		if r.Changed {
			note = text(r.Note, amber)
		}
		l := line{left: []span{box, text(" ", plain), name, note}}
		if i == m.cursor {
			l.bg = colorSelect
		}
		rows = append(rows, l)
	}
	bottom := []line{
		{left: []span{text("Platform steps whose inputs did not change come unpicked; what is picked runs.", dim)}},
		{left: []span{text(strings.Repeat("─", inner), rule)}},
		{left: []span{text("space", key), text(" pick   ", dim), text("a", key), text(" all   ", dim), text("p", key), text(" platform   ", dim),
			text("c", key), text(" changed   ", dim), text("↵", key), text(" deploy   ", dim), text("esc", key), text(" back", dim)}},
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
