package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/eugene-panin/damstack/internal/event"
)

// The plan screen (design: 3 · Confirm a plan): what a step's plan changes,
// whether it passed the policies, and, when Nomad would change groups in place
// in a way running allocations do not take, the choice to replace them.

type approveMsg struct {
	plan   event.Plan
	answer chan<- event.Approval
}

// planning is whether a plan waits for the person.
func (m *Deploy) planning() bool { return m.approve != nil }

func (m *Deploy) planKey(k string) tea.Cmd {
	restarts := len(m.approve.plan.Restarts) > 0
	switch k {
	case "a", "enter":
		m.approve.answer <- event.Approval{Apply: true, Restart: restarts && m.choice == 0}
		m.approve = nil
	case "n", "esc":
		m.approve.answer <- event.Approval{}
		m.approve = nil
	case "up", "k":
		m.choice = 0
	case "down", "j":
		if restarts {
			m.choice = 1
		}
	case "v":
		m.showFields = !m.showFields
	case "space", " ":
		m.expand = !m.expand
	case "ctrl+c":
		m.approve.answer <- event.Approval{}
		m.approve = nil
		m.stop()
	}
	return nil
}

func (m *Deploy) planFrame() string {
	inner := m.width - 4
	p := m.approve.plan
	title := "plan"
	if s := m.step(); s != nil {
		title = s.Title
	}
	meta := []string{}
	if s := m.step(); s != nil {
		meta = append(meta, fmt.Sprintf("step %d of %d", s.N, s.Total))
	}
	if m.kind != "" {
		meta = append(meta, strings.TrimSpace(m.kind+" "+m.what))
	}
	rows := []line{
		{left: []span{text(title, bold), text("  "+strings.Join(meta, " · "), dim)}, right: []span{text("waiting for you", blue)}},
		{left: []span{text(strings.Repeat("─", inner), rule)}},
	}
	counts := line{left: []span{
		text(fmt.Sprintf("+%d", p.Create), green), text(" to add   ", dim),
		text(fmt.Sprintf("~%d", p.Update+p.Replace), amber), text(" to change   ", dim),
		text(fmt.Sprintf("−%d", p.Delete), red), text(" to destroy", dim),
	}}
	if p.Policies {
		counts.right = []span{text("✓ ", green), text("policies", dim)}
	}
	rows = append(rows, counts, line{})

	// The changes, the jobs that need new allocations marked under them.
	restartsOf := map[string][]event.Restart{}
	for _, r := range p.Restarts {
		restartsOf[r.Addr] = append(restartsOf[r.Addr], r)
	}
	var changes []line
	for _, c := range p.Changes {
		sym, st := map[string]string{"create": "+", "update": "~", "replace": "±", "delete": "−"}[c.Action],
			map[string]style{"create": green, "update": amber, "replace": amber, "delete": red}[c.Action]
		changes = append(changes, line{left: []span{text(sym+" ", st), text(c.Addr, plain)}, right: []span{text(c.Action, dim)}})
		if rs := restartsOf[c.Addr]; len(rs) > 0 {
			groups := make([]string, len(rs))
			for i, r := range rs {
				groups[i] = r.Group
			}
			for i, l := range wrap("needs new allocations: "+strings.Join(groups, ", "), inner-8) {
				mark := "      "
				if i == 0 {
					mark = "    ↻ "
				}
				changes = append(changes, line{left: []span{text(mark, amber), text(l, amber)}})
			}
			if m.showFields {
				for _, r := range rs {
					for _, f := range r.Fields {
						changes = append(changes, line{left: []span{text("      "+r.Group+" · "+f, dim)}})
					}
				}
			}
		}
	}
	room := m.height - len(rows) - 4
	if len(p.Restarts) > 0 {
		room -= 9
	}
	if !m.expand && len(changes) > max(room, 3) {
		more := len(changes) - max(room, 3) + 1
		changes = append(changes[:max(room, 3)-1], line{left: []span{text(fmt.Sprintf("  + %d more   space shows all", more), dim)}})
	}
	rows = append(rows, changes...)

	if len(p.Restarts) > 0 {
		groups := 0
		for range p.Restarts {
			groups++
		}
		opt := func(i int, label, why string) line {
			l := line{left: []span{text("   ", plain), text(label, plain), text("   "+why, dim)}}
			if m.choice == i {
				l.left[0], l.left[1] = text(" › ", blue), text(label, bold)
			}
			return l
		}
		warn := []line{
			{left: []span{text("!  ", amber), text("Nomad applies this change in place.", amber)}},
			{left: []span{text("   Running allocations keep their old network and Envoy settings,", dim)}},
			{left: []span{text("   so the change takes effect only after they restart.", dim)}},
			{},
			opt(0, fmt.Sprintf("restart %s after apply", plural(groups, "group")), "one allocation at a time"),
			opt(1, "leave them", "they take it when they next restart"),
		}
		rows = append(rows, line{})
		rows = append(rows, frameBox(inner, amber, warn)...)
	}

	footer := line{left: []span{text("a", key), text(" apply   ", dim), text("v", key), text(" view diff   ", dim),
		text("space", key), text(" expand   ", dim), text("↑↓", key), text(" choose   ", dim), text("n", key), text(" no, stop here", dim)}}
	body := m.height - 3
	if len(rows) > body {
		rows = rows[:body]
	}
	for len(rows) < body {
		rows = append(rows, line{})
	}
	rows = append(rows, line{left: []span{text("full plan: damstack plan -p "+m.run.Project, dim)}},
		line{left: []span{text(strings.Repeat("─", inner), rule)}}, footer)
	out := make([]string, len(rows))
	margin := line{}.String(2)
	for i, r := range rows {
		out[i] = margin + r.String(inner) + margin
	}
	return strings.Join(out, "\n")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
