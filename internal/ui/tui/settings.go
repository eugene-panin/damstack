package tui

import (
	"bytes"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/eugene-panin/damstack/internal/settings"
)

// The settings screen (design: 4 · Settings, as the wizard asks): stack.yaml
// as sections of fields, the comment above each as its help, one value
// changed at a time; the file is there for what this does not change.

// SettingsResult is what the person chose on leaving the settings screen.
type SettingsResult struct {
	// Save says to write the file; Deploy to deploy it then; Raw to open it
	// in the editor.
	Save, Deploy, Raw bool
}

// Settings is the settings screen of a project.
type Settings struct {
	doc      *settings.Doc
	original []byte
	project  string
	sections []settings.Section
	touched  map[string]bool
	width    int
	height   int

	sec, cursor int
	editing     bool
	input       string
	// list mode: the item chosen, and whether a new one is being typed.
	listing bool
	item    int
	adding  bool

	flash      string
	flashError bool
	discarding bool
	result     SettingsResult
}

// NewSettings is the settings screen of a project's stack.yaml.
func NewSettings(project string, doc *settings.Doc) *Settings {
	m := &Settings{doc: doc, original: doc.Bytes(), project: project, touched: map[string]bool{}, width: 100, height: 30}
	m.sections = doc.Sections()
	m.cursor = m.next(0, 1)
	return m
}

// Result is what the person chose; Doc the file as they left it.
func (m *Settings) Result() SettingsResult { return m.result }
func (m *Settings) Doc() *settings.Doc     { return m.doc }

func (m *Settings) dirty() bool { return !bytes.Equal(m.doc.Bytes(), m.original) }

func (m *Settings) fields() []settings.Field {
	if m.sec >= len(m.sections) {
		return nil
	}
	return m.sections[m.sec].Fields
}

func (m *Settings) field() *settings.Field {
	fs := m.fields()
	if m.cursor < 0 || m.cursor >= len(fs) {
		return nil
	}
	return &fs[m.cursor]
}

// next is the first field from i, going by step, that is not a heading.
func (m *Settings) next(i, step int) int {
	fs := m.fields()
	for ; i >= 0 && i < len(fs); i += step {
		if fs[i].Kind != settings.Heading {
			return i
		}
	}
	return -1
}

func (m *Settings) Init() tea.Cmd { return nil }

func (m *Settings) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.PasteMsg:
		if m.editing || m.adding {
			m.input += strings.TrimSpace(msg.Content)
		}
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		switch {
		case m.editing || m.adding:
			m.inputKey(msg)
		case m.listing:
			m.listKey(msg.String())
		default:
			return m, m.key(msg.String())
		}
	}
	return m, nil
}

func (m *Settings) say(text string, isErr bool) { m.flash, m.flashError = text, isErr }

func (m *Settings) key(k string) tea.Cmd {
	m.flash = ""
	if k != "q" {
		m.discarding = false
	}
	switch k {
	case "up", "k":
		if i := m.next(m.cursor-1, -1); i >= 0 {
			m.cursor = i
		}
	case "down", "j":
		if i := m.next(m.cursor+1, 1); i >= 0 {
			m.cursor = i
		}
	case "tab", "right", "l":
		m.section((m.sec + 1) % len(m.sections))
	case "shift+tab", "left", "h":
		m.section((m.sec + len(m.sections) - 1) % len(m.sections))
	case "enter", "space", " ":
		m.change()
	case "E":
		m.result = SettingsResult{Save: m.dirty(), Raw: true}
		return tea.Quit
	case "d":
		m.result = SettingsResult{Save: m.dirty(), Deploy: true}
		return tea.Quit
	case "esc":
		m.result = SettingsResult{Save: m.dirty()}
		return tea.Quit
	case "q":
		if m.dirty() && !m.discarding {
			m.discarding = true
			m.say("q again leaves without saving the changes; esc saves them", true)
			return nil
		}
		m.result = SettingsResult{}
		return tea.Quit
	}
	return nil
}

func (m *Settings) section(i int) {
	m.sec = i
	m.cursor = m.next(0, 1)
}

// change starts changing the chosen field, as its kind allows.
func (m *Settings) change() {
	f := m.field()
	if f == nil {
		return
	}
	switch f.Kind {
	case settings.Toggle:
		v := "true"
		if f.Value == "true" {
			v = "false"
		}
		m.set(func() error { return m.doc.Set(f.Path, v) })
	case settings.Text, settings.Number:
		m.editing, m.input = true, f.Value
	case settings.List:
		m.listing, m.item = true, 0
	case settings.Fixed:
		if f.Once {
			m.say(f.Label+" is chosen once: changing it breaks what runs. E opens the file if you must.", true)
		} else {
			m.say(f.Label+" is changed in the file: E opens it", false)
		}
	}
}

// set applies a change, then reads the sections again, the cursor kept.
func (m *Settings) set(change func() error) bool {
	if err := change(); err != nil {
		m.say(err.Error(), true)
		return false
	}
	m.touched[m.sections[m.sec].Title] = true
	m.sections = m.doc.Sections()
	return true
}

func (m *Settings) inputKey(msg tea.KeyPressMsg) {
	f := m.field()
	switch msg.String() {
	case "esc":
		m.editing, m.adding, m.input = false, false, ""
	case "enter":
		value := m.input
		switch {
		case m.adding:
			items := append(append([]string{}, f.Items...), value)
			if value != "" && m.set(func() error { return m.doc.SetList(f.Path, items) }) {
				m.item = len(items) - 1
			}
			m.adding, m.input = false, ""
		case m.listing:
			items := append([]string{}, f.Items...)
			items[m.item] = value
			if m.set(func() error { return m.doc.SetList(f.Path, items) }) {
				m.editing, m.input = false, ""
			}
		default:
			if m.set(func() error { return m.doc.Set(f.Path, value) }) {
				m.editing, m.input = false, ""
			}
		}
	case "backspace":
		if r := []rune(m.input); len(r) > 0 {
			m.input = string(r[:len(r)-1])
		}
	default:
		if msg.Text != "" {
			m.input += msg.Text
		}
	}
}

func (m *Settings) listKey(k string) {
	f := m.field()
	m.flash = ""
	switch k {
	case "esc", "left":
		m.listing = false
	case "up", "k":
		m.item = max(m.item-1, 0)
	case "down", "j":
		m.item = min(m.item+1, max(len(f.Items)-1, 0))
	case "a", "+":
		m.adding, m.input = true, ""
	case "enter":
		if len(f.Items) > 0 {
			m.editing, m.input = true, f.Items[m.item]
		} else {
			m.adding, m.input = true, ""
		}
	case "x", "delete", "backspace":
		if len(f.Items) == 0 {
			return
		}
		items := append(append([]string{}, f.Items[:m.item]...), f.Items[m.item+1:]...)
		if m.set(func() error { return m.doc.SetList(f.Path, items) }) {
			m.item = max(min(m.item, len(items)-1), 0)
		}
	}
}

func (m *Settings) View() tea.View {
	v := tea.NewView(m.Frame())
	v.AltScreen = true
	v.BackgroundColor = colorBg
	v.ForegroundColor = colorFg
	return v
}

// Frame is the screen's text: exactly height rows of exactly width cells.
func (m *Settings) Frame() string {
	w, h := m.width, m.height
	if w < minWidth || h < minHeight {
		return tooSmall(w, h)
	}
	inner := w - 4
	head := line{left: []span{text("Settings", bold), text(" · "+m.project+" · stack.yaml", dim)}}
	if m.dirty() {
		head.right = []span{text("changed · esc saves", amber)}
	}
	rows := []line{head, {left: []span{text(strings.Repeat("─", inner), rule)}}}
	bottom := []line{m.helpLine(inner), {left: []span{text(strings.Repeat("─", inner), rule)}}, m.footer()}
	body := h - len(rows) - len(bottom)
	leftW := min(max(inner*22/100, 16), 24)
	rightW := inner - leftW - 5
	left, right := m.sectionList(), m.fieldRows(rightW, body)
	for i := 0; i < body; i++ {
		l, r := line{}, line{}
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		rows = append(rows, line{left: []span{text(l.String(leftW), raw), text("  ", plain), text("│", rule), text("  ", plain), text(r.String(rightW), raw)}})
	}
	rows = append(rows, bottom...)
	out := make([]string, len(rows))
	margin := line{}.String(2)
	for i, r := range rows {
		out[i] = margin + r.String(inner) + margin
	}
	return strings.Join(out, "\n")
}

func (m *Settings) sectionList() []line {
	var out []line
	appsShown := false
	for i, s := range m.sections {
		if len(s.Path) == 2 && s.Path[0] == "apps" && !appsShown {
			appsShown = true
			out = append(out, line{}, line{left: []span{text("apps", dim)}})
		}
		l := line{left: []span{text("  ", plain), text(s.Title, plain)}}
		if i == m.sec {
			l.left = []span{text("› ", blue), text(s.Title, bold)}
		}
		if m.touched[s.Title] {
			l.right = []span{text("•", amber)}
		}
		out = append(out, l)
	}
	return out
}

// fieldRows are the chosen section's help, then its fields, scrolled so the
// chosen one shows.
func (m *Settings) fieldRows(width, height int) []line {
	s := m.sections[m.sec]
	rows := []line{{left: []span{text(s.Title, bold)}}}
	if s.Help != "" {
		help := wrap(s.Help, width)
		if len(help) > 2 {
			help = help[:2]
			help[1] = strings.TrimRight(help[1], " ") + " …"
		}
		for _, l := range help {
			rows = append(rows, line{left: []span{text(l, dim)}})
		}
	}
	rows = append(rows, line{})
	var list []line
	at := 0
	for i, f := range s.Fields {
		if i == m.cursor {
			at = len(list)
		}
		list = append(list, m.fieldLine(i, f, width)...)
	}
	room := height - len(rows)
	if len(list) > room {
		top := min(max(at-room/2, 0), len(list)-room)
		list = list[top : top+room]
	}
	return append(rows, list...)
}

func (m *Settings) fieldLine(i int, f settings.Field, width int) []line {
	indent := strings.Repeat("  ", f.Depth)
	cursor := i == m.cursor
	if f.Kind == settings.Heading {
		return []line{{left: []span{text("  "+indent+f.Label, dim)}}}
	}
	mark, name := text("  ", plain), text(padTo(indent+f.Label, 24), plain)
	if cursor {
		mark, name = text("› ", blue), text(padTo(indent+f.Label, 24), bold)
	}
	var value []span
	switch {
	case cursor && m.editing && !m.listing:
		value = []span{text(m.input, plain), text("▌", blue)}
	case f.Kind == settings.Toggle && f.Value == "true":
		value = []span{text("● yes", green)}
	case f.Kind == settings.Toggle:
		value = []span{text("○ no", dim)}
	case f.Kind == settings.List && len(f.Items) == 0:
		value = []span{text("none", dim)}
	case f.Once:
		value = []span{text(f.Value, dim), text("  chosen once", amber)}
	case f.Kind == settings.Fixed:
		value = []span{text(f.Value, dim), text("  in the file", dim)}
	default:
		value = []span{text(f.Value, plain)}
	}
	l := line{left: append([]span{mark, name}, value...)}
	if cursor && !m.listing {
		l.bg = colorSelect
	}
	out := []line{l}
	if cursor && m.listing {
		for j, item := range f.Items {
			il := line{left: []span{text("      "+indent, plain), text("· ", dim), text(item, plain)}}
			if j == m.item {
				il.left[1] = text("› ", blue)
				if m.editing {
					il.left[2] = text(m.input+"▌", plain)
				} else {
					il.left[2] = text(item, bold)
				}
				il.bg = colorSelect
			}
			out = append(out, il)
		}
		add := line{left: []span{text("      "+indent, plain), text("+ add", dim)}}
		if m.adding {
			add = line{left: []span{text("      "+indent, plain), text("+ ", blue), text(m.input, plain), text("▌", blue)}}
		}
		out = append(out, add)
	}
	return out
}

// helpLine is a word just said, or the chosen field's help.
func (m *Settings) helpLine(width int) line {
	if m.flash != "" {
		st := blue
		if m.flashError {
			st = red
		}
		return line{left: []span{text(m.flash, st)}}
	}
	if f := m.field(); f != nil && f.Help != "" {
		return line{left: []span{text(f.Help, dim)}}
	}
	return line{}
}

func (m *Settings) footer() line {
	switch {
	case m.editing || m.adding:
		return line{left: []span{text("↵", key), text(" keep   ", dim), text("esc", key), text(" cancel", dim)}}
	case m.listing:
		return line{left: []span{text("↑↓", key), text(" item   ", dim), text("a", key), text(" add   ", dim), text("↵", key), text(" change   ", dim),
			text("x", key), text(" remove   ", dim), text("esc", key), text(" done", dim)}}
	}
	full := []span{text("↑↓", key), text(" choose   ", dim), text("tab", key), text(" section   ", dim), text("↵", key), text(" change   ", dim),
		text("E", key), text(" edit as YAML   ", dim), text("d", key), text(" save and deploy   ", dim), text("esc", key), text(" save and leave", dim)}
	if width(full) <= m.width-4 {
		return line{left: full}
	}
	return line{left: []span{text("↵", key), text(" change   ", dim), text("tab", key), text(" section   ", dim), text("E", key), text(" YAML   ", dim),
		text("d", key), text(" deploy   ", dim), text("esc", key), text(" save", dim)}}
}
