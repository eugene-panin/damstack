package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// The catalog (design: 8 · Catalog): the apps a project can take, those it
// has and their updates, and what the chosen one needs.

// CatalogApp is an app the catalog lists.
type CatalogApp struct {
	Name, Description string
	// Source is the repository, such as eugene-panin/damstack-mail; URL its
	// address, Path the directory of the app in it.
	Source, URL, Path string
	// Installed is the release the project has, empty when it has none;
	// Latest the newest, empty until known.
	Installed, Latest string
	Private           bool
}

// CatalogDetails are what is known of an app once its release was read.
type CatalogDetails struct {
	Needs []FormNeed
	Notes []string
	Err   string
}

// CatalogActions are what the catalog asks outside it.
type CatalogActions struct {
	// Latest is the newest release of an app; Details what its release says.
	Latest  func(app CatalogApp) (string, error)
	Details func(app CatalogApp) CatalogDetails
	Open    func(url string) error
}

// CatalogChoice is what the person chose: i to install, u to update, ↵ to
// open an installed app.
type CatalogChoice struct {
	Key string
	App CatalogApp
}

// Catalog is the catalog screen of a project.
type Catalog struct {
	actions  CatalogActions
	project  string
	platform string
	apps     []CatalogApp
	details  map[string]CatalogDetails
	asked    map[string]bool
	filter   string
	search   bool
	cursor   int
	width    int
	height   int
	flash    string
	choice   CatalogChoice
}

// NewCatalog is the catalog of apps for a project on platform.
func NewCatalog(project, platform string, apps []CatalogApp, actions CatalogActions) *Catalog {
	return &Catalog{actions: actions, project: project, platform: platform, apps: apps,
		details: map[string]CatalogDetails{}, asked: map[string]bool{}, width: 100, height: 30}
}

// Choice is what the person chose; a zero one when they left.
func (m *Catalog) Choice() CatalogChoice { return m.choice }

type latestMsg struct {
	name, tag string
}

type detailsMsg struct {
	name string
	CatalogDetails
}

func (m *Catalog) Init() tea.Cmd {
	if m.actions.Latest == nil {
		return m.describe()
	}
	var cmds []tea.Cmd
	for _, a := range m.apps {
		a := a
		cmds = append(cmds, func() tea.Msg {
			tag, err := m.actions.Latest(a)
			if err != nil {
				return latestMsg{a.Name, ""}
			}
			return latestMsg{a.Name, tag}
		})
	}
	return tea.Batch(append(cmds, m.describe())...)
}

// describe reads the chosen app's release, once.
func (m *Catalog) describe() tea.Cmd {
	a, ok := m.chosen()
	if !ok || m.asked[a.Name] || m.actions.Details == nil {
		return nil
	}
	m.asked[a.Name] = true
	return func() tea.Msg { return detailsMsg{a.Name, m.actions.Details(a)} }
}

// shown are the apps the filter lets through: those not installed first.
func (m *Catalog) shown() []CatalogApp {
	var free, installed []CatalogApp
	for _, a := range m.apps {
		if m.filter != "" && !strings.Contains(strings.ToLower(a.Name+" "+a.Description), strings.ToLower(m.filter)) {
			continue
		}
		if a.Installed != "" {
			installed = append(installed, a)
		} else {
			free = append(free, a)
		}
	}
	return append(free, installed...)
}

func (m *Catalog) chosen() (CatalogApp, bool) {
	s := m.shown()
	if m.cursor < 0 || m.cursor >= len(s) {
		return CatalogApp{}, false
	}
	return s[m.cursor], true
}

func (a CatalogApp) update() bool {
	return a.Installed != "" && a.Latest != "" && a.Latest != a.Installed && newer(a.Latest, a.Installed)
}

// newer is whether release a comes after b, as v1.2.3 tags compare.
func newer(a, b string) bool {
	var x, y [3]int
	fmt.Sscanf(strings.TrimPrefix(a, "v"), "%d.%d.%d", &x[0], &x[1], &x[2])
	fmt.Sscanf(strings.TrimPrefix(b, "v"), "%d.%d.%d", &y[0], &y[1], &y[2])
	for i := range x {
		if x[i] != y[i] {
			return x[i] > y[i]
		}
	}
	return false
}

func (m *Catalog) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case latestMsg:
		for i := range m.apps {
			if m.apps[i].Name == msg.name {
				m.apps[i].Latest = msg.tag
			}
		}
	case detailsMsg:
		m.details[msg.name] = msg.CatalogDetails
	case flashMsg:
		m.flash = string(msg)
	case tea.PasteMsg:
		if m.search {
			m.filter += strings.TrimSpace(msg.Content)
			m.cursor = 0
		}
	case tea.KeyPressMsg:
		if m.search {
			return m, m.searchKey(msg)
		}
		return m, m.key(msg.String())
	}
	return m, nil
}

func (m *Catalog) searchKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "ctrl+c":
		return tea.Quit
	case "esc", "enter", "down":
		m.search = false
	case "backspace":
		if r := []rune(m.filter); len(r) > 0 {
			m.filter = string(r[:len(r)-1])
		}
	default:
		if msg.Text != "" {
			m.filter += msg.Text
		}
	}
	m.cursor = 0
	return m.describe()
}

func (m *Catalog) key(k string) tea.Cmd {
	m.flash = ""
	a, ok := m.chosen()
	switch k {
	case "ctrl+c", "esc", "q":
		return tea.Quit
	case "/":
		m.search = true
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
		return m.describe()
	case "down", "j":
		m.cursor = min(m.cursor+1, max(len(m.shown())-1, 0))
		return m.describe()
	case "i":
		switch {
		case !ok:
		case a.Installed != "":
			m.flash = a.Name + " is installed already" + map[bool]string{true: "; u updates it", false: ""}[a.update()]
		default:
			m.choice = CatalogChoice{"i", a}
			return tea.Quit
		}
	case "u":
		switch {
		case !ok:
		case !a.update():
			m.flash = "no newer release of " + a.Name
		default:
			m.choice = CatalogChoice{"u", a}
			return tea.Quit
		}
	case "enter":
		if ok && a.Installed != "" {
			m.choice = CatalogChoice{"enter", a}
			return tea.Quit
		}
		if ok {
			m.choice = CatalogChoice{"i", a}
			return tea.Quit
		}
	case "s":
		m.choice = CatalogChoice{Key: "s"}
		return tea.Quit
	case "r":
		if ok && a.URL != "" && m.actions.Open != nil {
			return func() tea.Msg {
				if err := m.actions.Open(a.URL); err != nil {
					return flashMsg(err.Error())
				}
				return flashMsg("opened " + a.URL)
			}
		}
	}
	return nil
}

func (m *Catalog) View() tea.View {
	v := tea.NewView(m.Frame())
	v.AltScreen = true
	v.BackgroundColor = colorBg
	v.ForegroundColor = colorFg
	return v
}

// Frame is the screen's text: exactly height rows of exactly width cells.
func (m *Catalog) Frame() string {
	w, h := m.width, m.height
	if w < minWidth || h < minHeight {
		return tooSmall(w, h)
	}
	inner := w - 4
	installed, updates := 0, 0
	for _, a := range m.apps {
		if a.Installed != "" {
			installed++
		}
		if a.update() {
			updates++
		}
	}
	counts := fmt.Sprintf("%d apps · %d installed", len(m.apps), installed)
	if updates > 0 {
		counts += fmt.Sprintf(" · %s", plural(updates, "update"))
	}
	rows := []line{
		{left: []span{text("Catalog", bold), text(" · for "+m.project+" ("+m.platform+")", dim)}, right: []span{text(counts, dim)}},
		{left: []span{text(strings.Repeat("─", inner), rule)}},
	}
	search := line{left: []span{text("/ ", dim), text(m.filter, plain)}}
	if m.search {
		search.left = append(search.left, text("▌", blue))
	} else if m.filter == "" {
		search.left = []span{text("/ search", dim)}
	}
	rows = append(rows, search, line{})
	bottom := []line{{}, {left: []span{text(strings.Repeat("─", inner), rule)}},
		{left: []span{text("/", key), text(" search   ", dim), text("i", key), text(" install   ", dim), text("u", key), text(" update   ", dim),
			text("r", key), text(" README   ", dim), text("↵", key), text(" open   ", dim), text("s", key), text(" sources   ", dim), text("esc", key), text(" back", dim)}}}
	if m.flash != "" {
		bottom[0] = line{left: []span{text(m.flash, blue)}}
	}
	body := h - len(rows) - len(bottom)
	leftW := min(max(inner*48/100, 38), 56)
	rightW := inner - leftW - 5
	left, right := m.list(leftW), m.detail(rightW)
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

func (m *Catalog) list(width int) []line {
	var out []line
	heading := false
	for i, a := range m.shown() {
		if a.Installed != "" && !heading {
			heading = true
			if len(out) > 0 {
				out = append(out, line{})
			}
			out = append(out, line{left: []span{text("installed", dim)}})
		}
		cursor := i == m.cursor
		mark, name := text("  ", plain), text(padTo(a.Name, 12), plain)
		if cursor {
			mark, name = text("› ", blue), text(padTo(a.Name, 12), bold)
		}
		l := line{left: []span{mark}}
		if a.Installed != "" {
			l.left = append(l.left, text("● ", green), name, text(a.Installed, dim))
			if a.update() {
				l.left = append(l.left, text(" → "+a.Latest, amber))
			}
		} else {
			l.left = append(l.left, name, text(padTo(a.Description, max(width-24, 10)), dim), text(orDash(a.Latest, "…"), dim))
		}
		if a.Private {
			l.right = []span{text("private", dim)}
		}
		if cursor {
			l.bg = colorSelect
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		out = append(out, line{left: []span{text("no app matches", dim)}})
	}
	return out
}

func (m *Catalog) detail(width int) []line {
	a, ok := m.chosen()
	if !ok {
		return nil
	}
	version := orDash(a.Latest, a.Installed)
	out := []line{{left: []span{text(a.Name, bold), text("  "+a.Source+" · "+orDash(version, "…"), dim)}}}
	for _, l := range wrap(a.Description, width) {
		out = append(out, line{left: []span{text(l, plain)}})
	}
	d, ok := m.details[a.Name]
	if !ok {
		return append(out, line{}, line{left: []span{text("reading its release …", dim)}})
	}
	if d.Err != "" {
		return append(out, line{}, line{left: []span{text(d.Err, red)}})
	}
	if len(d.Needs) > 0 {
		out = append(out, line{}, line{left: []span{text("needs", dim)}})
		for _, n := range d.Needs {
			mark := text("✓ ", green)
			if !n.Met {
				mark = text("✗ ", red)
			}
			out = append(out, line{left: []span{mark, text(n.Text, plain)}})
		}
	}
	if len(d.Notes) > 0 {
		out = append(out, line{})
		for _, n := range d.Notes {
			for _, l := range wrap(n, width) {
				out = append(out, line{left: []span{text(l, dim)}})
			}
		}
	}
	return out
}
