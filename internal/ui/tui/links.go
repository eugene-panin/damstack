package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The links screen (design: 12 · Links): every address a project serves, how
// it is reached, how it answers, and how one signs in; ↵ opens it.

// Link is an address a project serves.
type Link struct {
	URL string
	// Admin is a page of the platform; Tunnel says it is reached through the
	// private network only.
	Admin, Tunnel bool
	// Token is the name of the token that signs in to it, for damstack token.
	Token string
}

// LinkActions are what the links screen's keys do outside it.
type LinkActions struct {
	Open      func(url string) error
	Copy      func(text string) error
	CopyToken func(name string) error
	// Check says how an address answers: its HTTP status, or why not.
	Check func(url string) string
	// Tunnel says whether the private network is up.
	Tunnel func() bool
}

type answer struct {
	status string
	took   time.Duration
}

// Links is the links screen of a project.
type Links struct {
	actions LinkActions
	project string
	links   []Link
	answers map[string]answer
	tunnel  *bool
	checked time.Time
	width   int
	height  int
	cursor  int
	flash   string
}

// NewLinks is the links screen of a project: its apps' addresses, then its
// platform's.
func NewLinks(project string, links []Link, actions LinkActions) *Links {
	var apps, admin []Link
	for _, l := range links {
		if l.Admin {
			admin = append(admin, l)
		} else {
			apps = append(apps, l)
		}
	}
	return &Links{actions: actions, project: project, links: append(apps, admin...), answers: map[string]answer{}, width: 100, height: 30}
}

type answeredMsg struct {
	url string
	answer
	at time.Time
}

type tunnelMsg bool

func (m *Links) checkAll() tea.Cmd {
	var cmds []tea.Cmd
	if m.actions.Tunnel != nil {
		cmds = append(cmds, func() tea.Msg { return tunnelMsg(m.actions.Tunnel()) })
	}
	if m.actions.Check == nil {
		return tea.Batch(cmds...)
	}
	for _, l := range m.links {
		u := l.URL
		cmds = append(cmds, func() tea.Msg {
			start := time.Now()
			status := m.actions.Check(u)
			return answeredMsg{u, answer{status, time.Since(start)}, time.Now()}
		})
	}
	return tea.Batch(cmds...)
}

func (m *Links) Init() tea.Cmd { return m.checkAll() }

func (m *Links) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case answeredMsg:
		m.answers[msg.url] = msg.answer
		m.checked = msg.at
	case tunnelMsg:
		up := bool(msg)
		m.tunnel = &up
	case flashMsg:
		m.flash = string(msg)
	case tea.KeyPressMsg:
		m.flash = ""
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit
		case "up", "k":
			m.cursor = max(m.cursor-1, 0)
		case "down", "j":
			m.cursor = max(min(m.cursor+1, len(m.links)-1), 0)
		case "r":
			m.answers = map[string]answer{}
			return m, m.checkAll()
		case "enter":
			return m, m.open()
		case "t":
			m.copyToken()
		case "y":
			if len(m.links) > 0 && m.actions.Copy != nil {
				if err := m.actions.Copy(m.links[m.cursor].URL); err != nil {
					m.flash = err.Error()
				} else {
					m.flash = m.links[m.cursor].URL + " is in the clipboard"
				}
			}
		}
	}
	return m, nil
}

func (m *Links) open() tea.Cmd {
	if len(m.links) == 0 || m.actions.Open == nil {
		return nil
	}
	l := m.links[m.cursor]
	if l.Tunnel && m.tunnel != nil && !*m.tunnel {
		m.flash = "the tunnel is off: turn it on in the WireGuard app, then ↵ again"
		return nil
	}
	return func() tea.Msg {
		if err := m.actions.Open(l.URL); err != nil {
			return flashMsg("could not open " + l.URL + ": " + err.Error())
		}
		return flashMsg("opened " + l.URL)
	}
}

func (m *Links) copyToken() {
	if len(m.links) == 0 {
		return
	}
	l := m.links[m.cursor]
	switch {
	case l.Token == "":
		m.flash = "no token signs in to " + l.URL
	case m.actions.CopyToken == nil:
	default:
		if err := m.actions.CopyToken(l.Token); err != nil {
			m.flash = err.Error()
			return
		}
		m.flash = "the " + l.Token + " token is in the clipboard"
	}
}

func (m *Links) View() tea.View {
	v := tea.NewView(m.Frame())
	v.AltScreen = true
	v.BackgroundColor = colorBg
	v.ForegroundColor = colorFg
	return v
}

// Frame is the screen's text: exactly height rows of exactly width cells.
func (m *Links) Frame() string {
	w, h := m.width, m.height
	if w < minWidth || h < minHeight {
		return tooSmall(w, h)
	}
	inner := w - 4
	head := line{left: []span{text("Links", bold), text(" · every address "+m.project+" serves", dim)}}
	if !m.checked.IsZero() {
		head.left = append(head.left, text(" · checked "+m.checked.Local().Format("15:04"), dim))
	}
	switch {
	case m.tunnel == nil:
	case *m.tunnel:
		head.right = []span{text("● tunnel on", green)}
	default:
		head.right = []span{text("○ tunnel off", amber)}
	}
	urlW := min(46, inner-11-14-14)
	rows := []line{head, {left: []span{text(strings.Repeat("─", inner), rule)}}, {}}
	section := ""
	for i, l := range m.links {
		if s := map[bool]string{false: "apps", true: "platform"}[l.Admin]; s != section {
			if section != "" {
				rows = append(rows, line{})
			}
			section = s
			rows = append(rows, line{left: []span{text(padTo(s, urlW+2), dim), text(padTo("reach", 11), dim), text(padTo("answers", 14), dim), text("sign in", dim)}})
		}
		rows = append(rows, m.linkLine(i, l, urlW))
	}
	if len(m.links) == 0 {
		rows = append(rows, line{left: []span{text("no addresses yet: they come with the first deploy", dim)}})
	}
	bottom := []line{
		{left: []span{text("↵ opens it in your browser. A tunnel address needs the tunnel on in the WireGuard app.", dim)}},
		{left: []span{text(strings.Repeat("─", inner), rule)}},
		{left: []span{text("↵", key), text(" open in browser   ", dim), text("t", key), text(" copy token   ", dim), text("y", key), text(" copy URL   ", dim),
			text("r", key), text(" check now   ", dim), text("esc", key), text(" back", dim)}},
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

func (m *Links) linkLine(i int, l Link, urlW int) line {
	cursor := i == m.cursor
	mark, url := text("  ", plain), text(padTo(l.URL, urlW), plain)
	if cursor {
		mark, url = text("› ", blue), text(padTo(l.URL, urlW), bold)
	}
	reach := "internet"
	if l.Tunnel {
		reach = "tunnel"
	}
	ans := text(padTo("…", 14), dim)
	if a, ok := m.answers[l.URL]; ok {
		st := green
		if len(a.status) != 3 || a.status[0] == '5' {
			st = red
		}
		t := a.status
		if len(a.status) == 3 {
			t += fmt.Sprintf(" · %.1fs", a.took.Seconds())
		}
		ans = text(padTo(t, 14), st)
	}
	sign := text("—", dim)
	if l.Token != "" {
		sign = text("token · t copy", plain)
	}
	ln := line{left: []span{mark, url, text(padTo(reach, 11), plain), ans, sign}}
	if cursor {
		ln.bg = colorSelect
	}
	return ln
}
