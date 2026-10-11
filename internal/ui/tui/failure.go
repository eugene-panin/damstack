package tui

import (
	"fmt"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/eugene-panin/damstack/internal/event"
)

// The failure screen (design: 5 · A step failed): what failed, in words, the
// last lines of its log, and what can be done about it. It stays until q, or r
// to run again.

// failedScreen is whether the screen shows the end of a run that failed.
func (m *Deploy) failedScreen() bool { return m.finished && m.err != nil && !m.aborting }

// Retry is whether the person asked to run again from the failure screen.
func (m *Deploy) Retry() bool { return m.retry }

func (m *Deploy) failureKey(k string) tea.Cmd {
	switch k {
	case "q", "esc", "ctrl+c":
		return tea.Quit
	case "r":
		m.retry = true
		return tea.Quit
	case "l":
		m.toggle(logPane)
		if m.pane == logPane && m.log != "" {
			return readLog(m.log)
		}
	case "s":
		if m.actions.SSH != nil {
			return tea.ExecProcess(m.actions.SSH(), func(error) tea.Msg { return nil })
		}
	case "c":
		if m.actions.Copy != nil {
			text := m.errorText()
			return func() tea.Msg {
				if err := m.actions.Copy(text); err != nil {
					return flashMsg("could not copy it: " + err.Error())
				}
				return flashMsg("the error is in the clipboard")
			}
		}
	}
	return nil
}

// failedTask is the task that failed in the failed step, if one did.
func (m *Deploy) failedTask() string {
	for i := len(m.tasks) - 1; i >= 0; i-- {
		if m.tasks[i].status == event.TaskFailed {
			return m.tasks[i].name
		}
	}
	return ""
}

// errorText is what c copies: the error and the last lines of the log.
func (m *Deploy) errorText() string {
	var b strings.Builder
	b.WriteString(m.err.Error())
	if m.failure != nil && len(m.failure.Tail) > 0 {
		b.WriteString("\n\n" + strings.Join(m.failure.Tail, "\n"))
	}
	return b.String()
}

func (m *Deploy) failureFrame() string {
	inner := m.width - 4
	stopped := m.failure == nil // stopped as asked, or before any step failed
	head := line{left: []span{text("damstack", bold), text(" "+orDash(m.run.Command, "deploy")+" ", dim), text(m.run.Project, bold)}}
	if stopped {
		head.right = []span{text("stopped", amber), text(" at "+clock2(m.now.Sub(m.started)), dim)}
	} else {
		head.right = []span{text("stopped", red), text(" at "+clock2(m.now.Sub(m.started)), dim)}
	}
	rows := []line{head, {left: []span{text(strings.Repeat("─", inner), rule)}}, m.stepsStrip(), {}}

	// What failed, in words, in a red frame.
	var what []line
	first, rest, _ := strings.Cut(m.err.Error(), "\n")
	if task := m.failedTask(); task != "" {
		what = append(what, line{left: []span{text("✗ ", red), text(task, bold)}})
		// The tool's own words first; damstack's, which only say the
		// tool failed, after them.
		if m.failure != nil {
			if why := reason(m.failure.Tail); why != "" {
				for _, l := range wrap(why, inner-6) {
					what = append(what, line{left: []span{text("  "+l, plain)}})
				}
			}
		}
		for _, l := range wrap(first, inner-6) {
			what = append(what, line{left: []span{text("  "+l, dim)}})
		}
	} else {
		mark, st := "✗ ", red
		if stopped {
			mark, st = "‖ ", amber
		}
		for i, l := range wrap(first, inner-6) {
			if i == 0 {
				what = append(what, line{left: []span{text(mark, st), text(l, bold)}})
			} else {
				what = append(what, line{left: []span{text("  "+l, plain)}})
			}
		}
	}
	what = append(what, line{})
	what = append(what, line{left: []span{text("  The steps before it are done and do not run again; this one starts over.", dim)}})
	if rest != "" && !strings.HasPrefix(rest, "Fix what it says above") {
		for _, l := range wrap(rest, inner-6) {
			what = append(what, line{left: []span{text("  "+l, dim)}})
		}
	}
	border := red
	if stopped {
		border = amber
	}
	rows = append(rows, frameBox(inner, border, what)...)

	if m.failure != nil && len(m.failure.Tail) > 0 {
		rows = append(rows, line{}, line{left: []span{text("last lines of the log", dim)}})
		room := m.height - len(rows) - 6
		tail := m.failure.Tail
		if room > 0 && len(tail) > room {
			tail = tail[len(tail)-room:]
		}
		var logRows []line
		for _, l := range tail {
			st := dim
			if strings.Contains(l, "ERROR") || strings.Contains(l, "Error") || strings.Contains(l, "fatal") {
				st = red
			}
			logRows = append(logRows, line{left: []span{text(l, st)}})
		}
		rows = append(rows, frameBox(inner, rule, logRows)...)
	}

	footer := line{left: []span{text("r", key), text(" retry this step   ", dim), text("l", key), text(" full log   ", dim),
		text("s", key), text(" ssh to the server   ", dim), text("c", key), text(" copy the error   ", dim),
		text("q", key), text(" quit, resume later", dim)}}
	if width(footer.left) > inner {
		footer = line{left: []span{text("r", key), text(" retry   ", dim), text("l", key), text(" log   ", dim),
			text("s", key), text(" ssh   ", dim), text("c", key), text(" copy   ", dim), text("q", key), text(" quit", dim)}}
	}
	if m.flash != "" {
		footer.right = []span{text(m.flash, green)}
	}
	logLine := line{}
	if m.failure != nil && m.failure.Log != "" {
		logLine = line{left: []span{text("full log  "+m.failure.Log+"  ·  history: damstack history", dim)}}
	}
	body := m.height - 3
	if len(rows) > body {
		rows = rows[:body]
	}
	for len(rows) < body {
		rows = append(rows, line{})
	}
	rows = append(rows, logLine, line{left: []span{text(strings.Repeat("─", inner), rule)}}, footer)
	if m.pane == logPane {
		rows = m.fullLog(rows, inner)
	}
	out := make([]string, len(rows))
	margin := line{}.String(2)
	for i, r := range rows {
		out[i] = margin + r.String(inner) + margin
	}
	return strings.Join(out, "\n")
}

// fullLog puts the log over the body of the failure screen, keeping its
// header and its keys.
func (m *Deploy) fullLog(rows []line, inner int) []line {
	body := rows[2 : len(rows)-3]
	lines := m.logTail
	if len(lines) > len(body) {
		lines = lines[len(lines)-len(body):]
	}
	for i := range body {
		body[i] = line{}
		if i < len(lines) {
			body[i] = line{left: []span{text(lines[i], dim)}}
		}
	}
	return rows
}

// stepsStrip is every step in one row: done, failed with its task count, and
// to come.
func (m *Deploy) stepsStrip() line {
	l := line{}
	appsShown := false
	for _, s := range m.steps {
		title := s.Title
		if strings.Contains(s.Name, "/") {
			if appsShown {
				continue
			}
			appsShown = true
			title, s.state = "Apps", m.appsState()
		}
		switch s.state {
		case stepDone, skipped:
			l.left = append(l.left, text("✓ ", green), text(title+"   ", plain))
		case failed:
			l.left = append(l.left, text("✗ ", red), text(title, bold))
			if s.total > 0 {
				l.left = append(l.left, text(fmt.Sprintf(" task %d/%d", s.done, s.total), dim))
			}
			l.left = append(l.left, text("   ", plain))
		default:
			l.left = append(l.left, text("○ "+title+"  ", dim))
		}
	}
	return l
}

// appsState is the state of the apps as one: failed if one did, done if all
// are, running if one is.
func (m *Deploy) appsState() stepState {
	state, all := pending, true
	for _, s := range m.steps {
		if !strings.Contains(s.Name, "/") {
			continue
		}
		switch s.state {
		case failed:
			return failed
		case running:
			state = running
		}
		if s.state != stepDone && s.state != skipped {
			all = false
		}
	}
	if all {
		return stepDone
	}
	return state
}

// frameBox draws rows in a frame of the given colour.
func frameBox(width int, colour style, rows []line) []line {
	inner := width - 4
	out := []line{{left: []span{text("╭"+strings.Repeat("─", width-2)+"╮", colour)}}}
	for _, r := range rows {
		out = append(out, line{left: []span{text("│ ", colour), text(r.String(inner), raw), text(" │", colour)}})
	}
	return append(out, line{left: []span{text("╰"+strings.Repeat("─", width-2)+"╯", colour)}})
}

var msgRe = regexp.MustCompile(`"msg":\s*"((?:[^"\\]|\\.)*)"`)

// reason is why a step failed, in the words of its tool, from the last lines
// of its log: Ansible's ERROR! or the msg of a fatal task, OpenTofu's Error:.
// Empty when none of them says.
func reason(tail []string) string {
	for i := len(tail) - 1; i >= 0; i-- {
		l := strings.TrimSpace(strings.TrimLeft(tail[i], "│ "))
		switch {
		case strings.HasPrefix(l, "ERROR!"):
			return strings.TrimSpace(strings.TrimPrefix(l, "ERROR!"))
		case strings.HasPrefix(l, "fatal:"):
			if m := msgRe.FindStringSubmatch(l); m != nil {
				return strings.ReplaceAll(m[1], `\"`, `"`)
			}
			return l
		case strings.HasPrefix(l, "Error:"):
			return strings.TrimSpace(strings.TrimPrefix(l, "Error:"))
		}
	}
	return ""
}
