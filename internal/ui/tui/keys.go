package tui

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The keys screen (design: 11b · App secrets): the keys an app asks for, which
// are set and which services use each, and setting one, its value never shown.

// Key is a key of an app as the keys screen lists it.
type Key struct {
	// Name is the secret's name in vault.yml.
	Name  string
	Label string
	// UsedBy are the services that get it; empty for all of them.
	UsedBy []string
	Set    bool
	// Changed is when it was set; Pending says a deploy has not put it in
	// place yet.
	Changed  time.Time
	Pending  bool
	Optional bool
	// NewAt is the page where a key is made.
	NewAt string
}

// KeyActions are what the keys screen's keys do outside it.
type KeyActions struct {
	// Set keeps a key's new value; Unset removes an optional one.
	Set   func(name, value string) error
	Unset func(name string) error
	Open  func(url string) error
}

// AppKeys is the keys screen of an app.
type AppKeys struct {
	actions KeyActions
	app     string
	source  string
	keys    []Key
	made    int
	now     time.Time
	width   int
	height  int

	cursor  int
	editing bool
	input   string
	// removing is the key x was pressed on once: a second x removes it.
	removing string
	flash    string
	deploy   bool
}

// NewAppKeys is the keys screen of app, from source (owner/repo · release);
// made is how many secrets of the app damstack makes itself.
func NewAppKeys(app, source string, keys []Key, made int, now time.Time, actions KeyActions) *AppKeys {
	return &AppKeys{actions: actions, app: app, source: source, keys: keys, made: made, now: now, width: 100, height: 30}
}

// Deploy is whether the person asked to deploy on leaving.
func (m *AppKeys) Deploy() bool { return m.deploy }

func (m *AppKeys) Init() tea.Cmd { return nil }

func (m *AppKeys) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case flashMsg:
		m.flash = string(msg)
	case tea.PasteMsg:
		if m.editing {
			m.input += strings.TrimSpace(msg.Content)
		}
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		if m.editing {
			return m, m.editKey(msg)
		}
		return m, m.listKey(msg.String())
	}
	return m, nil
}

func (m *AppKeys) listKey(k string) tea.Cmd {
	m.flash = ""
	if k != "x" {
		m.removing = ""
	}
	if len(m.keys) == 0 {
		if k == "q" || k == "esc" {
			return tea.Quit
		}
		return nil
	}
	cur := &m.keys[m.cursor]
	switch k {
	case "q", "esc":
		return tea.Quit
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j":
		m.cursor = min(m.cursor+1, len(m.keys)-1)
	case "enter":
		m.editing, m.input = true, ""
	case "?":
		if cur.NewAt == "" {
			m.flash = "the app does not say where " + cur.Label + " is made"
			return nil
		}
		return m.open(cur.NewAt)
	case "x":
		switch {
		case !cur.Set:
			m.flash = cur.Label + " is not set"
		case !cur.Optional:
			m.flash = sentence(cur.Label) + " is needed: it can be replaced, not removed"
		case m.removing != cur.Name:
			m.removing = cur.Name
			m.flash = "x again removes " + cur.Label
		default:
			m.removing = ""
			if m.actions.Unset != nil {
				if err := m.actions.Unset(cur.Name); err != nil {
					m.flash = err.Error()
					return nil
				}
			}
			cur.Set, cur.Pending, cur.Changed = false, true, m.now
			m.flash = sentence(cur.Label) + " is removed; d deploys it"
		}
	case "d":
		m.deploy = true
		return tea.Quit
	}
	return nil
}

func (m *AppKeys) editKey(msg tea.KeyPressMsg) tea.Cmd {
	cur := &m.keys[m.cursor]
	switch msg.String() {
	case "esc":
		m.editing, m.input = false, ""
	case "enter":
		if m.input == "" {
			m.flash = "paste the key first"
			return nil
		}
		if m.actions.Set != nil {
			if err := m.actions.Set(cur.Name, m.input); err != nil {
				m.flash = err.Error()
				return nil
			}
		}
		m.editing, m.input = false, ""
		cur.Set, cur.Pending, cur.Changed = true, true, m.now
		m.flash = sentence(cur.Label) + " is kept; d deploys it"
	case "backspace":
		if m.input != "" {
			m.input = m.input[:len(m.input)-1]
		}
	case "ctrl+o":
		return m.open(cur.NewAt)
	default:
		if msg.Text != "" {
			m.input += msg.Text
			m.flash = ""
		}
	}
	return nil
}

func (m *AppKeys) open(link string) tea.Cmd {
	if link == "" || m.actions.Open == nil {
		return nil
	}
	return func() tea.Msg {
		if err := m.actions.Open(link); err != nil {
			return flashMsg("could not open " + link + ": " + err.Error())
		}
		return flashMsg("opened " + link)
	}
}

// sentence is a label at the start of a sentence: xAI API key stays so,
// registry token is Registry token.
func sentence(s string) string {
	if len(s) < 2 || strings.ToLower(s[:1]) != s[:1] || strings.ToUpper(s[1:2]) == s[1:2] {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (m *AppKeys) View() tea.View {
	v := tea.NewView(m.Frame())
	v.AltScreen = true
	v.BackgroundColor = colorBg
	v.ForegroundColor = colorFg
	return v
}

// Frame is the screen's text: exactly height rows of exactly width cells.
func (m *AppKeys) Frame() string {
	w, h := m.width, m.height
	if w < minWidth || h < minHeight {
		return tooSmall(w, h)
	}
	inner := w - 4
	pending := 0
	for _, k := range m.keys {
		if k.Pending {
			pending++
		}
	}
	right := []span{text("values never shown", dim)}
	if pending > 0 {
		right = []span{text(plural(pending, "change")+" to deploy", amber), text("  ·  values never shown", dim)}
	}
	rows := []line{
		{left: []span{text(m.app, bold), text("  "+m.source, dim)}, right: right},
		{left: []span{text(strings.Repeat("─", inner), rule)}},
		{},
		m.keyColumns(line{left: []span{text("  key", dim)}}, text("state", dim), text("used by", dim), text("changed", dim)),
	}
	for i, k := range m.keys {
		rows = append(rows, m.keyRow(i, k))
	}
	if len(m.keys) == 0 {
		rows = append(rows, line{left: []span{text("  "+m.app+" asks for no keys", dim)}})
	}
	if m.made > 0 {
		rows = append(rows, line{left: []span{text("  "+padTo("made by damstack", 24)+fmt.Sprintf("%d · passwords and keys only its services read", m.made), dim)}})
	}
	if m.editing {
		rows = append(rows, line{})
		rows = append(rows, m.editBox(inner)...)
	}
	bottom := []line{
		{left: []span{text("Given through Vault only to the services that use them: never in stack.yaml or the state.", dim)}},
		{left: []span{text(strings.Repeat("─", inner), rule)}},
		m.footer(),
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

func padTo(s string, n int) string {
	if r := []rune(s); len(r) >= n {
		return string(r[:n-1]) + " "
	}
	return s + strings.Repeat(" ", n-len([]rune(s)))
}

// keyColumns lays a row out as the design's columns: the key, then state,
// used by and changed at fixed places.
func (m *AppKeys) keyColumns(first line, state, usedBy, changed span) line {
	first.left = append(first.left, text(strings.Repeat(" ", max(26-width(first.left), 0)), plain))
	stateW := 16
	if w := width([]span{state}); w < stateW {
		state.text += strings.Repeat(" ", stateW-w)
	} else {
		state.text += " "
	}
	usedBy.text = padTo(usedBy.text, 23)
	first.left = append(first.left, state, usedBy, changed)
	return first
}

func (m *AppKeys) keyRow(i int, k Key) line {
	cursor := i == m.cursor
	mark, name := text("  ", plain), text(padTo(k.Label, 24), plain)
	if cursor {
		mark, name = text("› ", blue), text(padTo(k.Label, 24), bold)
	}
	state := text("○ not set", dim)
	switch {
	case k.Set && k.Pending:
		state = text("● set, to deploy", amber)
	case k.Set:
		state = text("● set", green)
	case k.Pending:
		state = text("○ removed, to deploy", amber)
	}
	used := "every service"
	if len(k.UsedBy) > 0 {
		used = strings.Join(k.UsedBy, " ")
	}
	changed := "—"
	if !k.Changed.IsZero() && (k.Set || k.Pending) {
		changed = ago(m.now, k.Changed)
	}
	l := m.keyColumns(line{left: []span{mark, name}}, state, text(used, plain), text(changed, dim))
	if cursor && !m.editing {
		l.bg = colorSelect
	}
	return l
}

// ago is how long ago t was: just now, 5 minutes ago, 3 days ago.
func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute") + " ago"
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour") + " ago"
	}
	return plural(int(d.Hours()/24), "day") + " ago"
}

func (m *AppKeys) editBox(width int) []line {
	k := m.keys[m.cursor]
	verb := "Set"
	if k.Set {
		verb = "Replace"
	}
	field := []span{text(strings.Repeat("•", min(len(m.input), width-12)), dim), text("▌", blue)}
	if m.input == "" {
		field = []span{text("paste it here", dim), text("▌", blue)}
	}
	who := "every service of " + m.app
	if len(k.UsedBy) > 0 {
		who = joinAnd(k.UsedBy)
	}
	rows := []line{
		{},
		{left: field},
		{},
		{left: []span{text("then: ", dim), text(who+" restart, one at a time, on the next deploy", plain)}},
	}
	if k.Set {
		old := "the old key stops being used"
		if u, err := url.Parse(k.NewAt); err == nil && u.Host != "" {
			old += "; revoke it at " + u.Host
		}
		rows = append(rows, line{left: []span{text("      "+old, dim)}})
	}
	return box(width, []span{text(verb+" the "+k.Label, bold)}, rows)
}

func joinAnd(items []string) string {
	if len(items) == 1 {
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func (m *AppKeys) footer() line {
	if m.editing {
		return line{left: []span{text("↵", key), text(" keep it   ", dim), text("ctrl+o", key), text(" where to get it   ", dim), text("esc", key), text(" cancel", dim)}}
	}
	// The design's line, then a shorter one for narrow windows.
	full := []span{text("↵", key), text(" set or replace   ", dim), text("x", key), text(" remove   ", dim),
		text("?", key), text(" where to get it   ", dim), text("d", key), text(" deploy   ", dim), text("esc", key), text(" back", dim)}
	if width(full) <= m.width-4 {
		return line{left: full}
	}
	return line{left: []span{text("↵", key), text(" set   ", dim), text("x", key), text(" remove   ", dim),
		text("?", key), text(" where   ", dim), text("d", key), text(" deploy   ", dim), text("esc", key), text(" back", dim)}}
}
