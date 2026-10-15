package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// The done screen (design: 6 · Done): what the run set up, whether each
// address answers, and what to run next. It stays until q.

type (
	checkedMsg struct{ url, status string }
	flashMsg   string
)

// done is whether the screen shows the end of a run that went well.
func (m *Deploy) done() bool { return m.finished && m.err == nil && m.summary != nil }

// checkLinks asks every address whether it answers, all at once.
func (m *Deploy) checkLinks() tea.Cmd {
	if m.actions.Check == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, l := range m.summary.Links {
		u := l.URL
		cmds = append(cmds, func() tea.Msg { return checkedMsg{u, m.actions.Check(u)} })
	}
	return tea.Batch(cmds...)
}

func (m *Deploy) doneKey(k string) tea.Cmd {
	links := m.summary.Links
	switch k {
	case "q", "esc", "ctrl+c":
		return tea.Quit
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j":
		m.cursor = max(min(m.cursor+1, len(links)-1), 0)
	case "enter":
		if m.cursor < len(links) && m.actions.Open != nil {
			u := links[m.cursor].URL
			return func() tea.Msg {
				if err := m.actions.Open(u); err != nil {
					return flashMsg("could not open it: " + err.Error())
				}
				return flashMsg("opened " + u)
			}
		}
	case "t":
		if m.cursor < len(links) && links[m.cursor].Token != "" && m.actions.CopyToken != nil {
			name := links[m.cursor].Token
			return func() tea.Msg {
				if err := m.actions.CopyToken(name); err != nil {
					return flashMsg("no token: " + err.Error())
				}
				return flashMsg("the " + name + " token is in the clipboard")
			}
		}
	case "w":
		if m.actions.QR != nil {
			return tea.ExecProcess(m.actions.QR(), func(error) tea.Msg { return nil })
		}
	}
	return nil
}

func (m *Deploy) doneFrame() string {
	inner := m.width - 4
	s := m.summary
	apps := 0
	for _, st := range m.steps {
		if strings.Contains(st.Name, "/") && (st.state == stepDone || st.state == skipped) {
			apps++
		}
	}
	meta := m.run.Stack
	if apps > 0 {
		meta += fmt.Sprintf(" · %d apps healthy", apps)
	}
	rows := []line{
		{left: []span{text("✓ ", green), text(m.run.Project+" is up", bold), text("  "+meta, dim)},
			right: []span{text("took ", dim), text(clock(m.now.Sub(m.started)), green)}},
		{left: []span{text(strings.Repeat("─", inner), rule)}},
		m.stepsLine(),
		{},
	}

	var admin, apps2 []int
	for i, l := range s.Links {
		if l.Admin {
			admin = append(admin, i)
		} else {
			apps2 = append(apps2, i)
		}
	}
	if len(admin) > 0 {
		var b []line
		for _, i := range admin {
			b = append(b, m.linkRow(i))
		}
		rows = append(rows, box(inner, []span{text("Admin pages", bold), text("  through WireGuard: turn the tunnel on first", dim)}, b)...)
		rows = append(rows, line{})
	}
	if len(apps2) > 0 {
		var b []line
		for _, i := range apps2 {
			b = append(b, m.linkRow(i))
		}
		rows = append(rows, box(inner, []span{text("Apps", bold)}, b)...)
		rows = append(rows, line{})
	}
	if len(s.Next) > 0 {
		rows = append(rows, line{left: []span{text("Next", bold)}})
		for i, n := range s.Next {
			mark := "  "
			if i == 0 {
				mark = "› "
			}
			rows = append(rows, line{left: []span{text("  "+mark, blue), text(fmt.Sprintf("%-30s", n.Command), plain), text("  "+n.Why, dim)}})
		}
	}

	footer := line{left: []span{text("↵", key), text(" open in browser   ", dim), text("t", key), text(" copy token   ", dim),
		text("w", key), text(" WireGuard QR   ", dim), text("q", key), text(" quit", dim)}}
	if m.flash != "" {
		footer.right = []span{text(m.flash, green)}
	}
	body := m.height - 2
	if len(rows) > body {
		rows = rows[:body]
	}
	for len(rows) < body {
		rows = append(rows, line{})
	}
	rows = append(rows, line{left: []span{text(strings.Repeat("─", inner), rule)}}, footer)
	out := make([]string, len(rows))
	margin := line{}.String(2)
	for i, r := range rows {
		out[i] = margin + r.String(inner) + margin
	}
	return strings.Join(out, "\n")
}

// stepsLine is every step in a word and its time: the apps as one.
func (m *Deploy) stepsLine() line {
	l := line{left: []span{text("steps ", dim)}}
	var apps stepView
	appsSeen := false
	add := func(name string, s stepView) {
		mark, st := "✓ ", green
		if s.state == failed {
			mark, st = "✗ ", red
		}
		l.left = append(l.left, text(" "+mark, st), text(name+" ", dim), text(clock(s.took), dim), text(" ", dim))
	}
	for _, s := range m.steps {
		if strings.Contains(s.Name, "/") {
			apps.took += s.took
			if !appsSeen {
				appsSeen = true
				apps.state = stepDone
			}
			continue
		}
		if appsSeen && apps.state == stepDone {
			add("apps", apps)
			apps.state = pending
		}
		add(s.Name, s)
	}
	if appsSeen && apps.state == stepDone {
		add("apps", apps)
	}
	return l
}

func (m *Deploy) linkRow(i int) line {
	link := m.summary.Links[i]
	l := line{left: []span{text("  ", plain), text(link.URL, plain)}}
	if i == m.cursor {
		l.bg = colorSelect
		l.left[0] = text("› ", blue)
	}
	// Two columns on the right, the same width on every row: how the address
	// answers, and how one signs in.
	status, checked := m.checks[link.URL]
	col := func(s string) string { return fmt.Sprintf("%13s", s) }
	switch {
	case !checked:
		l.right = append(l.right, text(col(spinner[m.frame%len(spinner)]), amber))
	case status == "no DNS record" || status == "no answer":
		l.right = append(l.right, text(col(status), amber))
	case strings.HasPrefix(status, "2") || strings.HasPrefix(status, "3"):
		l.right = append(l.right, text(col(status), green))
	default:
		l.right = append(l.right, text(col(status), red))
	}
	signIn := ""
	if link.Token != "" {
		signIn = "token"
	}
	l.right = append(l.right, text(fmt.Sprintf("   %-5s", signIn), dim))
	return l
}

// box draws rows in a frame of rules, a title on its first row.
func box(width int, title []span, rows []line) []line {
	inner := width - 4
	out := []line{{left: []span{text("╭"+strings.Repeat("─", width-2)+"╮", rule)}}}
	wrapRow := func(l line) line {
		return line{left: []span{text("│ ", rule), text(l.String(inner), raw), text(" │", rule)}}
	}
	out = append(out, wrapRow(line{left: title}))
	for _, r := range rows {
		out = append(out, wrapRow(r))
	}
	return append(out, line{left: []span{text("╰"+strings.Repeat("─", width-2)+"╯", rule)}})
}
