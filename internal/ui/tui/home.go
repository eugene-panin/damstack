package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The home screen (design: 7 · Home): the projects, how the chosen one is,
// what needs you, and the keys that do everything else.

// State is how something runs, as the screens colour it.
type State int

const (
	Unknown State = iota
	Healthy
	Warning
	Down
	NotSetUp
)

// ProjectRow is a project as the home screen lists it.
type ProjectRow struct {
	Name, Stack, Server string
	Apps                []string
	// Backup is when the server backs up, and whether the last copy here is
	// fresh.
	Backup   string
	BackupOK bool
}

// AppState is how one app of a project runs: Text says more, such as 7/7.
type AppState struct {
	Name  string
	State State
	Text  string
}

// Need is something that needs the person: Urgent ones in red, the key that
// deals with it, if one does.
type Need struct {
	Text   string
	Urgent bool
	Key    string
}

// ProjectState is what is known of a project once it was looked at: its
// apps, its notes (such as its last deploy), and what needs the person.
type ProjectState struct {
	State State
	Text  string
	Apps  []AppState
	Notes []string
	Needs []Need
}

// HomeAction is what the person chose to do on the home screen; the command
// does it, then shows the home screen again.
type HomeAction struct {
	Key     string
	Project string
}

// Home is the home screen.
type Home struct {
	version string
	ready   bool
	rows    []ProjectRow
	states  map[string]ProjectState
	look    func(project string) ProjectState
	now     time.Time
	checked time.Time
	width   int
	height  int

	cursor int
	action HomeAction
	// note is what the last action left to say, such as why it failed.
	note string
}

// HomeKeys are the keys of the home screen and what they say, in the order
// of the design's line; a key is shown once the command does it.
var HomeKeys = []struct{ Key, Label string }{
	{"d", "deploy"}, {"u", "links"}, {"e", "edit"}, {"k", "keys"}, {"!", "security"}, {"s", "ssh"},
}

// NewHome is the home screen of projects, the current one chosen; look tells
// how a project is, off the screen's loop.
func NewHome(version string, ready bool, rows []ProjectRow, current string, now time.Time, look func(string) ProjectState) *Home {
	m := &Home{version: version, ready: ready, rows: rows, states: map[string]ProjectState{}, look: look, now: now, width: 100, height: 30}
	for i, r := range rows {
		if r.Name == current {
			m.cursor = i
		}
	}
	return m
}

// SetNote shows what the last action left to say, such as why it failed.
func (m *Home) SetNote(note string) { m.note = note }

// Action is what the person chose; a zero one when they quit.
func (m *Home) Action() HomeAction { return m.action }

type lookedMsg struct {
	project string
	state   ProjectState
	at      time.Time
}

type refreshMsg time.Time

// lookAll looks at every project at once.
func (m *Home) lookAll() tea.Cmd {
	if m.look == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, r := range m.rows {
		name := r.Name
		cmds = append(cmds, func() tea.Msg { return lookedMsg{name, m.look(name), time.Now()} })
	}
	cmds = append(cmds, tea.Tick(30*time.Second, func(t time.Time) tea.Msg { return refreshMsg(t) }))
	return tea.Batch(cmds...)
}

func (m *Home) Init() tea.Cmd { return m.lookAll() }

// SetState says how a project is, as a look found.
func (m *Home) SetState(project string, s ProjectState, at time.Time) {
	m.states[project] = s
	m.checked = at
}

func (m *Home) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case lookedMsg:
		m.SetState(msg.project, msg.state, msg.at)
		m.now = msg.at
	case refreshMsg:
		m.now = time.Time(msg)
		return m, m.lookAll()
	case tea.KeyPressMsg:
		k := msg.String()
		switch k {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit
		case "up":
			m.cursor = max(m.cursor-1, 0)
		case "down", "j":
			m.cursor = min(m.cursor+1, len(m.rows)-1)
		case "r":
			return m, m.lookAll()
		}
		for _, hk := range HomeKeys {
			if hk.Key == k && len(m.rows) > 0 {
				m.action = HomeAction{Key: k, Project: m.rows[m.cursor].Name}
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m *Home) View() tea.View {
	v := tea.NewView(m.Frame())
	v.AltScreen = true
	v.BackgroundColor = colorBg
	v.ForegroundColor = colorFg
	return v
}

// Frame is the screen's text: exactly height rows of exactly width cells.
func (m *Home) Frame() string {
	w, h := m.width, m.height
	if w < minWidth || h < minHeight {
		return tooSmall(w, h)
	}
	inner := w - 4
	head := line{left: []span{text("damstack", bold), text(" "+m.version, dim)}}
	if m.ready {
		head.left = append(head.left, text(" · this Mac is ready ", dim), text("✓", green))
	}
	if !m.checked.IsZero() {
		head.right = []span{text("live · refreshed "+m.checked.Local().Format("15:04:05"), dim)}
	} else {
		head.right = []span{text("looking…", amber)}
	}
	rows := []line{head, {left: []span{text(strings.Repeat("─", inner), rule)}}}
	rows = append(rows, m.homeColumns(line{left: []span{text("  project", dim)}}, text("stack", dim), text("server", dim), text("apps", dim), text("health", dim), text("backup", dim)))
	for i, r := range m.rows {
		rows = append(rows, m.projectRow(i, r))
	}
	if len(m.rows) == 0 {
		rows = append(rows, line{left: []span{text("  no projects yet: damstack deploy hashi sets up the first", dim)}})
	}
	rows = append(rows, line{})

	footer := m.footer()
	body := h - len(rows) - 2
	if len(m.rows) > 0 {
		left, right := m.detail(), m.needs()
		rightW := min(max(inner*36/100, 34), 46)
		leftW := inner - rightW - 5
		for i := 0; i < body; i++ {
			l, r := line{}, line{}
			if i < len(left) {
				l = left[i]
			}
			if i < len(right) {
				r = right[i]
			}
			sep := text("│", rule)
			if i >= max(len(left), len(right)) {
				sep = text(" ", plain)
			}
			rows = append(rows, line{left: []span{text(l.String(leftW), raw), text("  ", plain), sep, text("  ", plain), text(r.String(rightW), raw)}})
		}
	}
	for len(rows) < h-2 {
		rows = append(rows, line{})
	}
	rows = rows[:h-2]
	if m.note != "" {
		rows[h-3] = line{left: []span{text(m.note, red)}}
	}
	rows = append(rows, line{left: []span{text(strings.Repeat("─", inner), rule)}}, footer)
	out := make([]string, len(rows))
	margin := line{}.String(2)
	for i, r := range rows {
		out[i] = margin + r.String(inner) + margin
	}
	return strings.Join(out, "\n")
}

func (m *Home) homeColumns(first line, stack, server, apps, health, backup span) line {
	first.left = append(first.left, text(strings.Repeat(" ", max(15-width(first.left), 1)), plain))
	for _, c := range []struct {
		s span
		w int
	}{{stack, 17}, {server, 18}, {apps, 13}, {health, 16}} {
		c.s.text = padTo(c.s.text, c.w)
		first.left = append(first.left, c.s)
	}
	first.left = append(first.left, backup)
	return first
}

func (m *Home) projectRow(i int, r ProjectRow) line {
	cursor := i == m.cursor
	mark, name := text("  ", plain), text(r.Name, plain)
	if cursor {
		mark, name = text("› ", blue), text(r.Name, bold)
	}
	apps := fmt.Sprint(len(r.Apps))
	if len(r.Apps) == 1 {
		apps += " · " + r.Apps[0]
	}
	health := text("○ looking…", dim)
	if st, ok := m.states[r.Name]; ok {
		health = stateSpan(st.State, st.Text)
	}
	backup := text(orDash(r.Backup, "—"), dim)
	if r.BackupOK {
		backup = text(r.Backup+" ✓", green)
	}
	l := m.homeColumns(line{left: []span{mark, name}}, text(r.Stack, plain), text(orDash(r.Server, "—"), plain), text(apps, plain), health, backup)
	if cursor {
		l.bg = colorSelect
	}
	return l
}

func stateSpan(s State, t string) span {
	switch s {
	case Healthy:
		return text("● "+t, green)
	case Warning:
		return text("● "+t, amber)
	case Down:
		return text("● "+t, red)
	case NotSetUp:
		return text("○ "+t, dim)
	}
	return text("○ "+orDash(t, "unknown"), dim)
}

// detail is the chosen project: its apps three to a row, then its notes.
func (m *Home) detail() []line {
	r := m.rows[m.cursor]
	st, looked := m.states[r.Name]
	rows := []line{{left: []span{text(r.Name, bold)}}}
	apps := st.Apps
	if !looked {
		for _, a := range r.Apps {
			apps = append(apps, AppState{Name: a})
		}
	}
	for i := 0; i < len(apps); i += 3 {
		var l line
		for _, a := range apps[i:min(i+3, len(apps))] {
			dot := map[State]span{Healthy: text("●", green), Warning: text("●", amber), Down: text("●", red)}[a.State]
			if dot.text == "" {
				dot = text("○", dim)
			}
			cell := []span{dot, text(" "+a.Name, plain)}
			if a.Text != "" {
				cell = append(cell, text(" "+a.Text, dim))
			}
			cell = append(cell, text(strings.Repeat(" ", max(13-width(cell), 1)), plain))
			l.left = append(l.left, cell...)
		}
		rows = append(rows, l)
	}
	if len(st.Notes) > 0 {
		rows = append(rows, line{})
		for _, n := range st.Notes {
			rows = append(rows, line{left: []span{text(n, dim)}})
		}
	}
	return rows
}

// needs is what needs the person, across the projects: the chosen one's
// first.
func (m *Home) needs() []line {
	rows := []line{{left: []span{text("Needs you", bold)}}}
	var all []Need
	for i := range m.rows {
		j := (m.cursor + i) % len(m.rows)
		name := m.rows[j].Name
		for _, n := range m.states[name].Needs {
			if j != m.cursor {
				n.Text = name + ": " + n.Text
			}
			all = append(all, n)
		}
	}
	if len(all) == 0 {
		if len(m.states) == 0 {
			return append(rows, line{left: []span{text("looking…", dim)}})
		}
		return append(rows, line{left: []span{text("✓ ", green), text("nothing", dim)}})
	}
	for _, n := range all {
		mark := text("! ", amber)
		if n.Urgent {
			mark = text("✗ ", red)
		}
		l := line{left: []span{mark, text(n.Text, plain)}}
		if n.Key != "" {
			l.right = []span{text(n.Key, key)}
		}
		rows = append(rows, l)
	}
	return rows
}

func (m *Home) footer() line {
	var keys []span
	for _, hk := range HomeKeys {
		keys = append(keys, text(hk.Key, key), text(" "+hk.Label+"   ", dim))
	}
	keys = append(keys, text("q", key), text(" quit", dim))
	return line{left: keys}
}
