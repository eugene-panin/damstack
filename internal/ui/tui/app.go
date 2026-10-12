package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The app screen (design: 6 · App): how an app runs, group by group, what it
// serves, its logs, and what is done to it: restarts, an update, its settings
// and keys.

// AppGroup is a task group of an app's jobs, as Nomad runs it.
type AppGroup struct {
	Job, Name      string
	Running, Count int
	// MemoryUsed and Memory are in MB, what its allocations use and may.
	MemoryUsed, Memory int
	Restarts           int
	// Healthy is how many running allocations Nomad found healthy.
	Healthy int
	// Batch is a group that runs once and is done.
	Batch bool
	// Task is its first task, whose logs show; Allocs its running allocations.
	Task   string
	Allocs []string
}

// AppView is what is known of an app when it was looked at.
type AppView struct {
	Source, Version, Latest string
	Groups                  []AppGroup
	Links                   []Link
	Err                     string
}

// AppActions are what the app screen asks and does outside it.
type AppActions struct {
	Look func() AppView
	// Logs are the last lines a group's task wrote, on stderr or stdout.
	Logs    func(g AppGroup, stderr bool) []string
	Restart func(g AppGroup) error
	Check   func(url string) string
	Open    func(url string) error
}

// App is the screen of an app of a project.
type App struct {
	actions AppActions
	project string
	name    string
	view    AppView
	looked  time.Time
	answers map[string]answer
	width   int
	height  int
	cursor  int
	logs    []string
	showLog bool
	stderr  bool
	busy    string
	flash   string
	choice  string
	confirm string
}

// NewApp is the screen of app of project.
func NewApp(project, name string, actions AppActions) *App {
	return &App{actions: actions, project: project, name: name, answers: map[string]answer{}, width: 100, height: 30}
}

// Choice is what the person chose to do after the screen: u to update, e
// the settings, k the keys; empty when they went back.
func (m *App) Choice() string { return m.choice }

type appLookedMsg struct {
	AppView
	at time.Time
}

type appLogsMsg []string

type appTickMsg time.Time

type appDoneMsg struct{ text string }

func (m *App) look() tea.Cmd {
	if m.actions.Look == nil {
		return nil
	}
	return func() tea.Msg { return appLookedMsg{m.actions.Look(), time.Now()} }
}

func (m *App) Init() tea.Cmd {
	return tea.Batch(m.look(), tea.Tick(5*time.Second, func(t time.Time) tea.Msg { return appTickMsg(t) }))
}

func (m *App) group() (AppGroup, bool) {
	if m.cursor < 0 || m.cursor >= len(m.view.Groups) {
		return AppGroup{}, false
	}
	return m.view.Groups[m.cursor], true
}

func (m *App) readLogs() tea.Cmd {
	g, ok := m.group()
	if !ok || m.actions.Logs == nil {
		return nil
	}
	stderr := m.stderr
	return func() tea.Msg { return appLogsMsg(m.actions.Logs(g, stderr)) }
}

func (m *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case appLookedMsg:
		first := m.looked.IsZero()
		m.view, m.looked = msg.AppView, msg.at
		m.cursor = min(m.cursor, max(len(m.view.Groups)-1, 0))
		if first && m.actions.Check != nil {
			var cmds []tea.Cmd
			for _, l := range m.view.Links {
				u := l.URL
				cmds = append(cmds, func() tea.Msg {
					start := time.Now()
					status := m.actions.Check(u)
					return answeredMsg{u, answer{status, time.Since(start)}, time.Now()}
				})
			}
			return m, tea.Batch(cmds...)
		}
	case answeredMsg:
		m.answers[msg.url] = msg.answer
	case appLogsMsg:
		m.logs = msg
	case appTickMsg:
		cmds := []tea.Cmd{m.look(), tea.Tick(5*time.Second, func(t time.Time) tea.Msg { return appTickMsg(t) })}
		if m.showLog {
			cmds = append(cmds, m.readLogs())
		}
		return m, tea.Batch(cmds...)
	case appDoneMsg:
		m.busy, m.flash = "", msg.text
		return m, m.look()
	case flashMsg:
		m.flash = string(msg)
	case tea.KeyPressMsg:
		return m, m.key(msg.String())
	}
	return m, nil
}

func (m *App) key(k string) tea.Cmd {
	m.flash = ""
	if k != m.confirm {
		m.confirm = ""
	}
	g, ok := m.group()
	switch k {
	case "ctrl+c", "esc", "q":
		if m.showLog && k == "esc" {
			m.showLog = false
			return nil
		}
		return tea.Quit
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
		if m.showLog {
			return m.readLogs()
		}
	case "down", "j":
		m.cursor = min(m.cursor+1, max(len(m.view.Groups)-1, 0))
		if m.showLog {
			return m.readLogs()
		}
	case "l":
		m.showLog = !m.showLog
		if m.showLog {
			m.logs = nil
			return m.readLogs()
		}
	case "o":
		if m.showLog {
			m.stderr = !m.stderr
			m.logs = nil
			return m.readLogs()
		}
	case "r":
		if !ok || m.busy != "" || m.actions.Restart == nil {
			return nil
		}
		if m.confirm != "r" {
			m.confirm = "r"
			m.flash = "r again restarts " + g.Name + ", one allocation at a time"
			return nil
		}
		m.confirm, m.busy = "", "restarting "+g.Name
		return func() tea.Msg {
			if err := m.actions.Restart(g); err != nil {
				return appDoneMsg{"could not restart " + g.Name + ": " + err.Error()}
			}
			return appDoneMsg{g.Name + " restarted"}
		}
	case "R":
		if m.busy != "" || m.actions.Restart == nil {
			return nil
		}
		if m.confirm != "R" {
			m.confirm = "R"
			m.flash = "R again restarts every group of " + m.name + ", one at a time"
			return nil
		}
		m.confirm, m.busy = "", "restarting "+m.name
		groups := m.view.Groups
		return func() tea.Msg {
			for _, g := range groups {
				if g.Batch {
					continue
				}
				if err := m.actions.Restart(g); err != nil {
					return appDoneMsg{"could not restart " + g.Name + ": " + err.Error()}
				}
			}
			return appDoneMsg{m.name + " restarted"}
		}
	case "u":
		if m.view.Latest == "" || m.view.Latest == m.view.Version {
			m.flash = m.name + " is at its newest release"
			return nil
		}
		m.choice = "u"
		return tea.Quit
	case "e", "K":
		m.choice = map[string]string{"e": "e", "K": "k"}[k]
		return tea.Quit
	}
	return nil
}

func (m *App) View() tea.View {
	v := tea.NewView(m.Frame())
	v.AltScreen = true
	v.BackgroundColor = colorBg
	v.ForegroundColor = colorFg
	return v
}

// state is how the app runs, as its groups say.
func (m *App) state() span {
	if m.looked.IsZero() {
		return text("○ looking…", dim)
	}
	if m.view.Err != "" {
		return text("○ "+m.view.Err, dim)
	}
	bad := 0
	for _, g := range m.view.Groups {
		if !g.Batch && g.Running < g.Count {
			bad++
		}
	}
	switch {
	case len(m.view.Groups) == 0:
		return text("○ not running", amber)
	case bad == 0:
		return text("● healthy", green)
	case bad == len(m.view.Groups):
		return text("● down", red)
	}
	return text("● "+plural(bad, "group")+" short", amber)
}

// Frame is the screen's text: exactly height rows of exactly width cells.
func (m *App) Frame() string {
	w, h := m.width, m.height
	if w < minWidth || h < minHeight {
		return tooSmall(w, h)
	}
	inner := w - 4
	head := line{left: []span{text(m.name, bold), text("  "+strings.TrimSpace(m.view.Source+" · "+m.view.Version), dim), text("  ", plain), m.state()}}
	if m.view.Latest != "" && m.view.Latest != m.view.Version && newer(m.view.Latest, m.view.Version) {
		head.right = []span{text(m.view.Latest+" available", amber)}
	}
	rows := []line{head, {left: []span{text(strings.Repeat("─", inner), rule)}}, {}}
	rows = append(rows, line{left: []span{text("  "+padTo("group", 12), dim), text(padTo("count", 8), dim), text(padTo("memory", 18), dim),
		text(padTo("restarts", 10), dim), text("healthy", dim)}})
	for i, g := range m.view.Groups {
		rows = append(rows, m.groupRow(i, g))
	}
	if len(m.view.Groups) == 0 && !m.looked.IsZero() {
		rows = append(rows, line{left: []span{text("  no group of "+m.name+" runs: is it deployed, and the tunnel on?", dim)}})
	}
	rows = append(rows, line{})
	if m.showLog {
		g, _ := m.group()
		stream := "stdout"
		if m.stderr {
			stream = "stderr"
		}
		rows = append(rows, line{left: []span{text("log", bold), text("  "+g.Name+" · "+stream+" · o switches", dim)}})
		room := h - len(rows) - 3
		logs := m.logs
		if len(logs) > room {
			logs = logs[len(logs)-room:]
		}
		for _, l := range logs {
			rows = append(rows, line{left: []span{text(l, dim)}})
		}
		if m.logs == nil {
			rows = append(rows, line{left: []span{text("reading …", dim)}})
		}
	} else if len(m.view.Links) > 0 {
		rows = append(rows, line{left: []span{text("serves", dim)}})
		for _, l := range m.view.Links {
			ans := text("…", dim)
			if a, ok := m.answers[l.URL]; ok {
				st := green
				if len(a.status) != 3 || a.status[0] == '5' {
					st = red
				}
				ans = text(a.status, st)
			}
			rows = append(rows, line{left: []span{text("  "+padTo(l.URL, 48), plain), ans}})
		}
	}
	bottom := []line{{}, {left: []span{text(strings.Repeat("─", inner), rule)}}, m.footer()}
	switch {
	case m.busy != "":
		bottom[0] = line{left: []span{text(m.busy+" …", amber)}}
	case m.flash != "":
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

func (m *App) groupRow(i int, g AppGroup) line {
	dot := text("● ", green)
	switch {
	case g.Batch:
		dot = text("✓ ", dim)
	case g.Running == 0:
		dot = text("● ", red)
	case g.Running < g.Count:
		dot = text("● ", amber)
	}
	name := text(padTo(g.Name, 12), plain)
	if i == m.cursor {
		name = text(padTo(g.Name, 12), bold)
	}
	count := fmt.Sprintf("%d/%d", g.Running, g.Count)
	mem := "—"
	switch {
	case g.Memory > 0 && g.MemoryUsed > 0:
		mem = fmt.Sprintf("%s / %s", megabytes(g.MemoryUsed), megabytes(g.Memory))
	case g.Memory > 0:
		// Nomad measures no use when its driver keeps no stats.
		mem = "— / " + megabytes(g.Memory)
	}
	healthy := fmt.Sprintf("%d/%d", g.Healthy, g.Running)
	if g.Batch {
		count, healthy = "done", ""
	}
	restarts := text(padTo(fmt.Sprint(g.Restarts), 10), plain)
	if g.Restarts > 0 {
		restarts.style = amber
	}
	l := line{left: []span{dot, name, text(padTo(count, 8), plain), text(padTo(mem, 18), plain), restarts, text(healthy, plain)}}
	if i == m.cursor {
		l.bg = colorSelect
	}
	return l
}

// megabytes is a size in MB as the design writes it: 310 MB, 1.1 GB.
func megabytes(mb int) string {
	if mb >= 1024 {
		return fmt.Sprintf("%.1f GB", float64(mb)/1024)
	}
	return fmt.Sprintf("%d MB", mb)
}

func (m *App) footer() line {
	if m.showLog {
		return line{left: []span{text("↑↓", key), text(" group   ", dim), text("o", key), text(" stdout/stderr   ", dim), text("esc", key), text(" close the log", dim)}}
	}
	return line{left: []span{text("↑↓", key), text(" group   ", dim), text("l", key), text(" logs   ", dim), text("r", key), text(" restart   ", dim),
		text("R", key), text(" restart all   ", dim), text("u", key), text(" update   ", dim), text("e", key), text(" settings   ", dim),
		text("K", key), text(" keys   ", dim), text("esc", key), text(" back", dim)}}
}
