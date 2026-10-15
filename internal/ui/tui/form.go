package tui

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// The form (design: 9 · Install, 11b · Install keys, 10 · Review, and the
// wizard they share): a stack's questions page by page, as its sections group
// them, then the keys it asks for, then a review; nothing changes until the
// review is confirmed.

// FormQuestion is a question of an install as the form asks it.
type FormQuestion struct {
	Name    string
	Prompt  string
	Type    string
	Options []string
	// Section is the page it is on; Note is said before it.
	Section string
	Note    string
	// Advanced is a question hidden until asked for, its default kept.
	Advanced bool
	// When is the condition on earlier answers under which it is asked.
	When string
}

// FormSecret is a key an install asks for.
type FormSecret struct {
	Name     string
	Ask      string
	When     string
	Optional bool
	// NewAt is the page where a new one is made; For the services that get it.
	NewAt string
	For   []string
}

// FormActions are what the form asks outside it.
type FormActions struct {
	// Parse reads the answer typed to a question; Holds is whether a
	// condition holds for the answers so far.
	Parse func(q FormQuestion, raw string) (any, error)
	Holds func(when string, answers map[string]any) bool
	// Default is a question's default for the answers so far, as text.
	Default func(q FormQuestion, answers map[string]any) string
	// Check runs a question's checks, those that ask the network too, on an
	// answer; Secret on a key given.
	Check  func(q FormQuestion, answer any, answers map[string]any) error
	Secret func(s FormSecret, value string, answers map[string]any) error
	Open   func(url string) error
}

// FormNeed is a line of what an install needs, met or not.
type FormNeed struct {
	Text string
	Met  bool
}

type pageKind int

const (
	needsPage pageKind = iota
	questionsPage
	keysPage
	reviewPage
)

type formPage struct {
	title     string
	kind      pageKind
	questions []FormQuestion
}

// Form is the form of an install.
type Form struct {
	actions  FormActions
	title    string
	app      string
	needs    []FormNeed
	side     []string
	pages    []formPage
	secrets  []FormSecret
	raw      map[string]string
	errs     map[string]string
	advanced map[int]bool
	visited  map[int]bool
	page     int
	focus    int
	checking bool
	done     bool
	width    int
	height   int
	flash    string
}

// NewForm is the form of installing an app: title says what, needs what it
// needs, side what to know (such as how long it takes).
func NewForm(title, app string, needs []FormNeed, side []string, qs []FormQuestion, secrets []FormSecret, actions FormActions) *Form {
	m := &Form{actions: actions, title: title, app: app, needs: needs, side: side, raw: map[string]string{}, errs: map[string]string{},
		advanced: map[int]bool{}, visited: map[int]bool{}, width: 100, height: 30}
	m.pages = append(m.pages, formPage{title: "What it needs", kind: needsPage})
	for _, q := range qs {
		title := q.Section
		if title == "" {
			title = "Settings"
		}
		if n := len(m.pages); m.pages[n-1].kind == questionsPage && m.pages[n-1].title == title {
			m.pages[n-1].questions = append(m.pages[n-1].questions, q)
		} else {
			m.pages = append(m.pages, formPage{title: title, kind: questionsPage, questions: []FormQuestion{q}})
		}
	}
	for _, s := range secrets {
		if s.Ask != "" {
			m.secrets = append(m.secrets, s)
		}
	}
	if len(m.secrets) > 0 {
		m.pages = append(m.pages, formPage{title: "Keys", kind: keysPage})
	}
	m.pages = append(m.pages, formPage{title: "Review", kind: reviewPage})
	m.enter(0)
	return m
}

// Result is the answers and keys given, and whether the person confirmed the
// review.
func (m *Form) Result() (answers map[string]any, keys map[string]string, ok bool) {
	return m.answers(), m.keys(), m.done
}

// answers are the questions answered so far, parsed; a question whose
// condition does not hold is left out.
func (m *Form) answers() map[string]any {
	out := map[string]any{}
	for _, p := range m.pages {
		for _, q := range p.questions {
			if !m.actions.Holds(q.When, out) {
				continue
			}
			raw, ok := m.raw[q.Name]
			if !ok {
				continue
			}
			if v, err := m.actions.Parse(q, raw); err == nil {
				out[q.Name] = v
			}
		}
	}
	return out
}

func (m *Form) keys() map[string]string {
	out := map[string]string{}
	for _, s := range m.liveSecrets() {
		if v := m.raw["secret:"+s.Name]; v != "" {
			out[s.Name] = v
		}
	}
	return out
}

func (m *Form) liveSecrets() []FormSecret {
	answers := m.answers()
	var out []FormSecret
	for _, s := range m.secrets {
		if m.actions.Holds(s.When, answers) {
			out = append(out, s)
		}
	}
	return out
}

// enter goes to page i, its questions given their defaults.
func (m *Form) enter(i int) {
	m.page, m.focus, m.flash = i, 0, ""
	m.visited[i] = true
	if m.actions.Default == nil {
		return
	}
	answers := m.answers()
	for _, q := range m.pages[i].questions {
		if _, ok := m.raw[q.Name]; !ok {
			m.raw[q.Name] = m.actions.Default(q, answers)
		}
	}
}

// row is a line of a page that takes the focus: a question, a key, or the
// switch of the advanced questions.
type row struct {
	question *FormQuestion
	secret   *FormSecret
	advanced bool
}

func (m *Form) rows() []row {
	p := m.pages[m.page]
	var out []row
	switch p.kind {
	case keysPage:
		for _, s := range m.liveSecrets() {
			out = append(out, row{secret: &s})
		}
	case questionsPage:
		answers := m.answers()
		var adv []row
		for i := range p.questions {
			q := &p.questions[i]
			if !m.actions.Holds(q.When, answers) {
				continue
			}
			if q.Advanced {
				adv = append(adv, row{question: q})
			} else {
				out = append(out, row{question: q})
			}
		}
		if len(adv) > 0 {
			out = append(out, row{advanced: true})
			if m.advanced[m.page] {
				out = append(out, adv...)
			}
		}
	}
	return out
}

type formCheckedMsg struct {
	page int
	errs map[string]string
}

func (m *Form) Init() tea.Cmd { return nil }

func (m *Form) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case flashMsg:
		m.flash = string(msg)
	case formCheckedMsg:
		m.checking = false
		if msg.page != m.page {
			return m, nil
		}
		m.errs = msg.errs
		if len(msg.errs) == 0 {
			m.enter(min(m.page+1, len(m.pages)-1))
		} else {
			m.flash = "fix what is in red first"
		}
	case tea.PasteMsg:
		if r := m.focused(); r != nil && m.typed(r) {
			name := m.name(r)
			m.raw[name] += strings.TrimSpace(msg.Content)
		}
	case tea.KeyPressMsg:
		if m.checking && msg.String() != "ctrl+c" {
			return m, nil
		}
		return m, m.key(msg)
	}
	return m, nil
}

func (m *Form) focused() *row {
	rs := m.rows()
	if m.focus < 0 || m.focus >= len(rs) {
		return nil
	}
	return &rs[m.focus]
}

// move puts the focus on row i; an empty question there takes its default
// for the answers given since the page opened, such as a host name from a
// domain.
func (m *Form) move(i int) {
	m.focus = i
	if r := m.focused(); r != nil && r.question != nil && m.raw[r.question.Name] == "" && m.actions.Default != nil {
		m.raw[r.question.Name] = m.actions.Default(*r.question, m.answers())
	}
}

// typed is whether a row takes what is typed: a text or list question, or a
// key.
func (m *Form) typed(r *row) bool {
	if r.secret != nil {
		return true
	}
	return r.question != nil && r.question.Type != "enum" && r.question.Type != "bool"
}

func (m *Form) name(r *row) string {
	if r.secret != nil {
		return "secret:" + r.secret.Name
	}
	return r.question.Name
}

func (m *Form) options(q *FormQuestion) []string {
	if q.Type == "bool" {
		return []string{"yes", "no"}
	}
	return q.Options
}

func (m *Form) key(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	r := m.focused()
	switch k {
	case "ctrl+c", "esc":
		return tea.Quit
	case "up":
		m.move(max(m.focus-1, 0))
		return nil
	case "down":
		m.move(min(m.focus+1, max(len(m.rows())-1, 0)))
		return nil
	case "shift+tab":
		if m.page > 0 {
			m.enter(m.page - 1)
		}
		return nil
	case "tab":
		return m.next()
	case "ctrl+o":
		if r != nil && r.secret != nil && r.secret.NewAt != "" && m.actions.Open != nil {
			link := r.secret.NewAt
			return func() tea.Msg {
				if err := m.actions.Open(link); err != nil {
					return flashMsg(err.Error())
				}
				return flashMsg("opened " + link)
			}
		}
		return nil
	case "enter":
		switch {
		case m.pages[m.page].kind == reviewPage:
			m.done = true
			return tea.Quit
		case r != nil && r.advanced:
			m.advanced[m.page] = !m.advanced[m.page]
			return nil
		case m.focus >= len(m.rows())-1:
			return m.next()
		}
		m.move(m.focus + 1)
		return nil
	}
	if r == nil {
		return nil
	}
	if r.question != nil && !m.typed(r) {
		opts := m.options(r.question)
		i := slices.Index(opts, m.raw[r.question.Name])
		switch k {
		case "left", "right", "space", " ":
			step := 1
			if k == "left" {
				step = len(opts) - 1
			}
			m.raw[r.question.Name] = opts[(max(i, 0)+step)%len(opts)]
			delete(m.errs, r.question.Name)
		}
		return nil
	}
	if m.typed(r) {
		name := m.name(r)
		switch k {
		case "backspace":
			if v := []rune(m.raw[name]); len(v) > 0 {
				m.raw[name] = string(v[:len(v)-1])
			}
		default:
			if msg.Text != "" {
				m.raw[name] += msg.Text
			}
		}
		delete(m.errs, name)
	}
	return nil
}

// next checks the page, off the screen's loop, and goes on when it is right.
func (m *Form) next() tea.Cmd {
	p := m.pages[m.page]
	if p.kind == needsPage {
		for _, n := range m.needs {
			if !n.Met {
				m.flash = "it cannot be installed: " + n.Text
				return nil
			}
		}
	}
	if p.kind == reviewPage {
		return nil
	}
	page, answers := m.page, m.answers()
	type item struct {
		q   *FormQuestion
		s   *FormSecret
		raw string
	}
	var items []item
	for _, r := range m.allRows() {
		items = append(items, item{r.question, r.secret, m.raw[m.name(&r)]})
	}
	m.checking = true
	parse, check, secret := m.actions.Parse, m.actions.Check, m.actions.Secret
	return func() tea.Msg {
		errs := map[string]string{}
		for _, it := range items {
			switch {
			case it.q != nil:
				v, err := parse(*it.q, it.raw)
				if err == nil && check != nil {
					err = check(*it.q, v, answers)
				}
				if err != nil {
					errs[it.q.Name] = err.Error()
				}
			case it.s != nil:
				switch {
				case it.raw == "" && it.s.Optional:
				case it.raw == "":
					errs["secret:"+it.s.Name] = "needed: the app does not run without it"
				case secret != nil:
					if err := secret(*it.s, it.raw, answers); err != nil {
						errs["secret:"+it.s.Name] = err.Error()
					}
				}
			}
		}
		return formCheckedMsg{page, errs}
	}
}

// allRows are the rows of the page, the advanced ones too though hidden.
func (m *Form) allRows() []row {
	shown := m.advanced[m.page]
	m.advanced[m.page] = true
	defer func() { m.advanced[m.page] = shown }()
	var out []row
	for _, r := range m.rows() {
		if !r.advanced {
			out = append(out, r)
		}
	}
	return out
}

func (m *Form) View() tea.View {
	v := tea.NewView(m.Frame())
	v.AltScreen = true
	v.BackgroundColor = colorBg
	v.ForegroundColor = colorFg
	return v
}

// Frame is the screen's text: exactly height rows of exactly width cells.
func (m *Form) Frame() string {
	w, h := m.width, m.height
	if w < minWidth || h < minHeight {
		return tooSmall(w, h)
	}
	inner := w - 4
	rows := []line{
		{left: []span{text(m.title, bold)}, right: []span{text(fmt.Sprintf("%d / %d", m.page+1, len(m.pages)), dim)}},
		{left: []span{text(strings.Repeat("─", inner), rule)}},
	}
	bottom := []line{m.flashLine(), {left: []span{text(strings.Repeat("─", inner), rule)}}, m.footer()}
	body := h - len(rows) - len(bottom)
	leftW := min(max(inner*24/100, 18), 26)
	rightW := inner - leftW - 5
	left, right := m.steps(leftW), m.pageRows(rightW)
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

func (m *Form) steps(width int) []line {
	var out []line
	for i, p := range m.pages {
		mark, st := text("○ ", dim), dim
		switch {
		case i == m.page:
			mark, st = text("● ", amber), bold
		case i < m.page || m.visited[i]:
			mark, st = text("✓ ", green), plain
		}
		out = append(out, line{left: []span{mark, text(p.title, st)}})
	}
	out = append(out, line{})
	for _, s := range m.side {
		for _, l := range wrap(s, width) {
			out = append(out, line{left: []span{text(l, dim)}})
		}
	}
	return out
}

func (m *Form) pageRows(width int) []line {
	p := m.pages[m.page]
	out := []line{{left: []span{text(p.title, bold)}}, {}}
	switch p.kind {
	case needsPage:
		for _, n := range m.needs {
			mark := text("✓ ", green)
			if !n.Met {
				mark = text("✗ ", red)
			}
			out = append(out, line{left: []span{mark, text(n.Text, plain)}})
		}
		if len(m.needs) == 0 {
			out = append(out, line{left: []span{text("nothing besides the platform", dim)}})
		}
		return out
	case reviewPage:
		return append(out, m.review(width)...)
	}
	rs := m.rows()
	for i, r := range rs {
		focus := i == m.focus
		switch {
		case r.advanced:
			var names []string
			for _, q := range p.questions {
				if q.Advanced {
					names = append(names, strings.ReplaceAll(q.Name, "_", " "))
				}
			}
			arrow := "▸"
			if m.advanced[m.page] {
				arrow = "▾"
			}
			l := line{left: []span{text(arrow+" advanced  ", plain), text(strings.Join(names, ", ")+" (defaults kept)", dim)}}
			if focus {
				l.bg = colorSelect
			}
			out = append(out, l)
		case r.question != nil:
			out = append(out, m.questionRows(r.question, focus, width)...)
		case r.secret != nil:
			out = append(out, m.secretRows(r.secret, focus, width)...)
		}
		out = append(out, line{})
	}
	return out
}

func (m *Form) questionRows(q *FormQuestion, focus bool, width int) []line {
	var out []line
	if q.Note != "" {
		for _, l := range wrap(q.Note, width) {
			out = append(out, line{left: []span{text(l, dim)}})
		}
	}
	out = append(out, line{left: []span{text(q.Prompt, plain)}})
	value := m.raw[q.Name]
	if m.typed(&row{question: q}) {
		l := line{left: []span{text(value, bold)}}
		if focus {
			l.left = append(l.left, text("▌", blue))
			l.bg = colorSelect
		}
		out = append(out, l)
	} else {
		for _, o := range m.options(q) {
			l := line{left: []span{text("  ", plain), text(o, dim)}}
			if o == value {
				l.left = []span{text("› ", blue), text(o, bold)}
				if focus {
					l.bg = colorSelect
				}
			}
			out = append(out, l)
		}
	}
	if e := m.errs[q.Name]; e != "" {
		out = append(out, line{left: []span{text("✗ "+e, red)}})
	}
	return out
}

func (m *Form) secretRows(s *FormSecret, focus bool, width int) []line {
	name := "secret:" + s.Name
	head := []span{text(keyLabel(s.Ask), plain)}
	if s.Optional {
		head = append(head, text("  · optional", dim))
	}
	l := line{left: head}
	if len(s.For) > 0 {
		l.right = []span{text(strings.Join(s.For, " "), dim)}
	}
	out := []line{l}
	v := m.raw[name]
	value := line{left: []span{text(strings.Repeat("•", min(len([]rune(v)), width-4)), plain)}}
	if v == "" {
		value.left = []span{text("not set", dim)}
		if s.Optional {
			value.left = []span{text("not set · it runs without it", dim)}
		}
	}
	if focus {
		value.left = append(value.left, text("▌", blue))
		value.bg = colorSelect
	}
	out = append(out, value)
	if e := m.errs[name]; e != "" {
		out = append(out, line{left: []span{text("✗ "+e, red)}})
	}
	return out
}

// keyLabel is the name of a key in what it is asked with.
func keyLabel(ask string) string {
	if i := strings.IndexAny(ask, "(,"); i > 0 {
		ask = ask[:i]
	}
	return strings.TrimSpace(ask)
}

func (m *Form) review(width int) []line {
	out := []line{{left: []span{text("Settings", bold), text("  written to stack.yaml under apps."+m.app, dim)}}}
	answers := m.answers()
	for _, p := range m.pages {
		for _, q := range p.questions {
			v, ok := answers[q.Name]
			if !ok || q.Advanced {
				continue
			}
			out = append(out, line{left: []span{text(padTo(strings.ReplaceAll(q.Name, "_", " "), 22), dim), text(answerText(v), plain)}})
		}
	}
	if len(m.secrets) > 0 {
		keys := m.keys()
		names := slices.Sorted(maps.Keys(keys))
		labels := make([]string, len(names))
		for i, n := range names {
			for _, s := range m.secrets {
				if s.Name == n {
					labels[i] = keyLabel(s.Ask)
				}
			}
		}
		out = append(out, line{}, line{left: []span{text("Keys", bold), text(fmt.Sprintf("  %d of %d set, kept encrypted in vault.yml", len(keys), len(m.liveSecrets())), dim)}})
		for _, l := range wrap(strings.Join(labels, ", "), width) {
			out = append(out, line{left: []span{text(l, plain)}})
		}
	}
	return append(out, line{}, line{left: []span{text("then: ", dim), text("the deploy screen, "+m.app+" only", plain)}})
}

func answerText(v any) string {
	switch v := v.(type) {
	case bool:
		if v {
			return "yes"
		}
		return "no"
	case []any:
		parts := make([]string, len(v))
		for i, x := range v {
			parts[i] = fmt.Sprint(x)
		}
		return strings.Join(parts, ", ")
	}
	return fmt.Sprint(v)
}

func (m *Form) flashLine() line {
	switch {
	case m.checking:
		return line{left: []span{text("checking …", amber)}}
	case m.flash != "":
		return line{left: []span{text(m.flash, blue)}}
	}
	return line{}
}

func (m *Form) footer() line {
	switch m.pages[m.page].kind {
	case reviewPage:
		return line{left: []span{text("↵", key), text(" install   ", dim), text("shift-tab", key), text(" back   ", dim), text("esc", key), text(" leave, nothing installed", dim)}}
	case keysPage:
		return line{left: []span{text("↑↓", key), text(" choose   ", dim), text("tab", key), text(" next   ", dim), text("ctrl+o", key), text(" where to get it   ", dim),
			text("shift-tab", key), text(" back   ", dim), text("esc", key), text(" leave", dim)}}
	}
	return line{left: []span{text("↑↓", key), text(" choose   ", dim), text("←→", key), text(" option   ", dim), text("tab", key), text(" next   ", dim),
		text("shift-tab", key), text(" back   ", dim), text("esc", key), text(" leave", dim)}}
}
