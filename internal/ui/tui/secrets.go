package tui

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// The secrets screen (design: 13 · Rotate secrets): every secret of a project,
// the riskiest first, never a value; the ones picked are made anew here.

// SecretKind is how a secret gets a new value.
type SecretKind int

const (
	// Generated: damstack makes a new one.
	Generated SecretKind = iota
	// Asked: the person makes one elsewhere and gives it.
	Asked
	// Accounts: new passwords of an app's sign-ins, set in Vault.
	Accounts
)

// SecretRow is a secret as the screen lists it.
type SecretRow struct {
	// Name is the secret's name in vault.yml, or the account set's.
	Name  string
	Label string
	// Owner is the platform or the app it is of.
	Owner string
	Kind  SecretKind
	// Set is when its value was set; zero when not known.
	Set  time.Time
	Seen bool
	// Keep says why it cannot be made anew; empty when it can.
	Keep string
	// NewAt is the page where a new value of an asked secret is made.
	NewAt string
	// Takes is what puts a new value in place, such as damstack deploy.
	Takes string
	// Count is how many sign-ins an account set has.
	Count int
}

// Password is the new password of one sign-in.
type Password struct{ User, Password string }

// Rotated is how making one secret anew went.
type Rotated struct {
	Name      string
	Err       error
	Passwords []Password
}

// SecretActions are what the screen's keys do outside it.
type SecretActions struct {
	// Rotate makes the rows anew, asked ones with values by name. It runs off
	// the screen's loop.
	Rotate func(rows []SecretRow, values map[string]string) []Rotated
	// MarkSeen records a secret as seen in plain text.
	MarkSeen func(name string) error
	// OldAfter keeps the days after which a secret is old.
	OldAfter func(days int) error
	Open     func(url string) error
	Copy     func(text string) error
}

type secretsPhase int

const (
	listing secretsPhase = iota
	reviewing
	rotating
	rotated
)

// Secrets is the secrets screen.
type Secrets struct {
	actions  SecretActions
	project  string
	rows     []SecretRow
	oldAfter int
	now      time.Time
	width    int
	height   int

	cursor, top int
	picked      map[string]bool
	phase       secretsPhase
	flash       string

	// The review: the rows being made anew, the asked one waiting for its
	// value, what was typed, and the values given.
	review []SecretRow
	asking int
	input  string
	values map[string]string

	results []Rotated
	copyAt  int
	// copied are the users whose password was copied; leaving is the key
	// pressed once to leave before all were.
	copied  map[string]bool
	leaving string
	deploy  bool
}

// NewSecrets is the screen of a project's secrets, a secret old after
// oldAfter days.
func NewSecrets(project string, rows []SecretRow, oldAfter int, now time.Time, actions SecretActions) *Secrets {
	m := &Secrets{actions: actions, project: project, oldAfter: oldAfter, now: now, width: 100, height: 30, picked: map[string]bool{}}
	m.rows = slices.Clone(rows)
	m.sort()
	m.cursor = 0
	return m
}

// Deploy is whether the person asked, after making secrets anew, to deploy.
func (m *Secrets) Deploy() bool { return m.deploy }

// Rotated are the rows made anew without an error.
func (m *Secrets) Rotated() []SecretRow {
	var out []SecretRow
	for i, res := range m.results {
		if res.Err == nil && i < len(m.review) {
			out = append(out, m.review[i])
		}
	}
	return out
}

// needsDeploy is whether a deploy has anything to put in place: a secret of
// vault.yml made anew. A sign-in's password is in Vault already.
func (m *Secrets) needsDeploy() bool {
	for _, r := range m.Rotated() {
		if r.Kind != Accounts {
			return true
		}
	}
	return false
}

type rotatedMsg []Rotated

func (m *Secrets) Init() tea.Cmd { return nil }

func (m *Secrets) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case flashMsg:
		m.flash = string(msg)
	case rotatedMsg:
		m.results, m.phase, m.copyAt, m.copied = msg, rotated, 0, nil
		for _, r := range msg {
			if r.Err != nil {
				continue
			}
			if i := m.index(r.Name); i >= 0 {
				m.rows[i].Set, m.rows[i].Seen = m.now, false
			}
		}
		m.picked = map[string]bool{}
		m.sort()
	case tea.PasteMsg:
		if m.phase == reviewing && m.asking >= 0 {
			m.input += strings.TrimSpace(msg.Content)
		}
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		switch m.phase {
		case listing:
			return m, m.listKey(msg.String())
		case reviewing:
			return m, m.reviewKey(msg)
		case rotated:
			return m, m.rotatedKey(msg.String())
		}
	}
	return m, nil
}

func (m *Secrets) index(name string) int {
	return slices.IndexFunc(m.rows, func(r SecretRow) bool { return r.Name == name })
}

func (m *Secrets) old(r SecretRow) bool {
	return !r.Set.IsZero() && m.now.Sub(r.Set) > time.Duration(m.oldAfter)*24*time.Hour
}

// risk orders the rows: seen ones first, then old ones, then the rest, and
// those that cannot be made anew last; the oldest first within each.
func (m *Secrets) risk(r SecretRow) int {
	switch {
	case r.Keep != "":
		return 3
	case r.Seen:
		return 0
	case m.old(r):
		return 1
	}
	return 2
}

func (m *Secrets) sort() {
	cur := ""
	if m.cursor < len(m.rows) {
		cur = m.rows[m.cursor].Name
	}
	slices.SortStableFunc(m.rows, func(a, b SecretRow) int {
		if d := m.risk(a) - m.risk(b); d != 0 {
			return d
		}
		if c := a.Set.Compare(b.Set); c != 0 {
			return c
		}
		return strings.Compare(a.Label, b.Label)
	})
	if i := m.index(cur); i >= 0 {
		m.cursor = i
	}
}

// policies are the days the p key goes through.
var policies = []int{30, 90, 180, 365}

func (m *Secrets) listKey(k string) tea.Cmd {
	m.flash = ""
	switch k {
	case "q", "esc":
		return tea.Quit
	case "up", "k":
		m.cursor = max(m.cursor-1, 0)
	case "down", "j":
		m.cursor = min(m.cursor+1, len(m.rows)-1)
	case "space", " ":
		if len(m.rows) == 0 {
			return nil
		}
		r := m.rows[m.cursor]
		if r.Keep != "" {
			m.flash = "can't rotate " + r.Label + ": " + r.Keep
			return nil
		}
		m.picked[r.Name] = !m.picked[r.Name]
		m.cursor = min(m.cursor+1, len(m.rows)-1)
	case "s":
		if len(m.rows) == 0 {
			return nil
		}
		r := &m.rows[m.cursor]
		if m.actions.MarkSeen != nil {
			if err := m.actions.MarkSeen(r.Name); err != nil {
				m.flash = err.Error()
				return nil
			}
		}
		r.Seen = true
		m.flash = r.Label + " is marked as seen"
		m.sort()
	case "p":
		i := slices.Index(policies, m.oldAfter)
		m.oldAfter = policies[(i+1)%len(policies)]
		if m.actions.OldAfter != nil {
			if err := m.actions.OldAfter(m.oldAfter); err != nil {
				m.flash = err.Error()
				return nil
			}
		}
		m.flash = fmt.Sprintf("a secret is old after %d days", m.oldAfter)
		m.sort()
	case "enter":
		m.review = nil
		for _, r := range m.rows {
			if m.picked[r.Name] {
				m.review = append(m.review, r)
			}
		}
		if len(m.review) == 0 {
			m.flash = "space selects the secrets to rotate"
			return nil
		}
		m.phase, m.values, m.input, m.asking = reviewing, map[string]string{}, "", -1
		return m.nextAsked(0)
	}
	return nil
}

// nextAsked moves to the first asked row from i without a value, and opens
// the page where its new value is made.
func (m *Secrets) nextAsked(from int) tea.Cmd {
	m.asking, m.input = -1, ""
	for i := from; i < len(m.review); i++ {
		if r := m.review[i]; r.Kind == Asked {
			m.asking = i
			return m.open(r.NewAt)
		}
	}
	return nil
}

func (m *Secrets) open(link string) tea.Cmd {
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

func (m *Secrets) reviewKey(msg tea.KeyPressMsg) tea.Cmd {
	k := msg.String()
	if k == "esc" {
		m.phase, m.review, m.asking, m.input = listing, nil, -1, ""
		return nil
	}
	if m.asking >= 0 {
		switch k {
		case "enter":
			if m.input == "" {
				m.flash = "paste the new value first"
				return nil
			}
			m.values[m.review[m.asking].Name] = m.input
			m.flash = ""
			return m.nextAsked(m.asking + 1)
		case "backspace":
			if m.input != "" {
				m.input = m.input[:len(m.input)-1]
			}
		case "ctrl+o":
			return m.open(m.review[m.asking].NewAt)
		default:
			if msg.Text != "" {
				m.input += msg.Text
			}
		}
		return nil
	}
	if k != "enter" {
		return nil
	}
	m.phase = rotating
	rows, values, rotate := slices.Clone(m.review), m.values, m.actions.Rotate
	return func() tea.Msg {
		if rotate == nil {
			return rotatedMsg(nil)
		}
		return rotatedMsg(rotate(rows, values))
	}
}

// passwords are the new passwords the rotation made, in order.
func (m *Secrets) passwords() []Password {
	var out []Password
	for _, r := range m.results {
		out = append(out, r.Passwords...)
	}
	return out
}

func (m *Secrets) rotatedKey(k string) tea.Cmd {
	m.flash = ""
	ps := m.passwords()
	// The new passwords are nowhere else: leaving before each is copied
	// takes the same key twice.
	if k == "q" || k == "esc" || (k == "d" && m.needsDeploy()) {
		if left := len(ps) - len(m.copied); left > 0 && m.leaving != k {
			m.leaving = k
			m.flash = fmt.Sprintf("%s not copied yet are lost once you leave · %s again leaves", plural(left, "password"), k)
			return nil
		}
	}
	m.leaving = ""
	switch k {
	case "q":
		return tea.Quit
	case "esc":
		m.phase, m.review, m.results, m.copied = listing, nil, nil, nil
	case "up", "k":
		m.copyAt = max(m.copyAt-1, 0)
	case "down", "j":
		m.copyAt = min(m.copyAt+1, max(len(ps)-1, 0))
	case "enter":
		if len(ps) == 0 || m.actions.Copy == nil {
			return nil
		}
		p := ps[m.copyAt]
		if err := m.actions.Copy(p.Password); err != nil {
			m.flash = err.Error()
			return nil
		}
		if m.copied == nil {
			m.copied = map[string]bool{}
		}
		m.copied[p.User] = true
		m.flash = "the password of " + p.User + " is in the clipboard: send it before copying the next"
		m.copyAt = min(m.copyAt+1, len(ps)-1)
	case "d":
		if !m.needsDeploy() {
			return nil
		}
		m.deploy = true
		return tea.Quit
	}
	return nil
}

func (m *Secrets) View() tea.View {
	v := tea.NewView(m.Frame())
	v.AltScreen = true
	v.BackgroundColor = colorBg
	v.ForegroundColor = colorFg
	return v
}

// Frame is the screen's text: exactly height rows of exactly width cells.
func (m *Secrets) Frame() string {
	w, h := m.width, m.height
	if w < minWidth || h < minHeight {
		return tooSmall(w, h)
	}
	inner := w - 4
	seen, old := 0, 0
	for _, r := range m.rows {
		switch {
		case r.Seen:
			seen++
		case m.old(r):
			old++
		}
	}
	var counts []span
	if seen > 0 {
		counts = append(counts, text(fmt.Sprintf("%d seen", seen), red))
	}
	if old > 0 {
		if len(counts) > 0 {
			counts = append(counts, text("  ", plain))
		}
		counts = append(counts, text(fmt.Sprintf("%d old", old), amber))
	}
	top := []line{
		{left: []span{text("Secrets", bold), text(fmt.Sprintf(" · %d · values never shown here", len(m.rows)), dim)}, right: counts},
		{left: []span{text(strings.Repeat("─", inner), rule)}},
		m.columns(inner, line{left: []span{text("    ", plain), text("secret", dim)}}, "owner", "age", "why", dim),
	}
	var panel []line
	if m.phase != listing {
		panel = append([]line{{}}, m.panel(inner)...)
	}
	bottom := []line{
		{left: []span{text("Seen in plain text: printed by a tool, pasted in a chat, or left in a file outside vault.yml.", dim)}},
		{left: []span{text(strings.Repeat("─", inner), rule)}},
		m.footer(inner),
	}
	if m.flash != "" {
		bottom[0] = line{left: []span{text(m.flash, blue)}}
	}
	room := h - len(top) - len(panel) - len(bottom) - 1
	rows := append(top, m.list(inner, max(room, 1))...)
	rows = append(rows, panel...)
	for len(rows) < h-len(bottom) {
		rows = append(rows, line{})
	}
	rows = append(rows[:h-len(bottom)], bottom...)
	out := make([]string, len(rows))
	margin := line{}.String(2)
	for i, r := range rows {
		out[i] = margin + r.String(inner) + margin
	}
	return strings.Join(out, "\n")
}

func tooSmall(w, h int) string {
	msg := fmt.Sprintf("damstack needs a window of %d×%d at least; this one is %d×%d.", minWidth, minHeight, w, h)
	rows := make([]string, h)
	for i := range rows {
		rows[i] = line{}.String(w)
	}
	if h > 0 {
		rows[h/2] = line{left: []span{text(msg, dim)}}.String(w)
	}
	return strings.Join(rows, "\n")
}

// columns lays a row out as the design's columns: the secret, then owner, age
// and why at fixed places.
func (m *Secrets) columns(width int, first line, owner, age, why string, st style, extra ...span) line {
	first.left = append(first.left, text(strings.Repeat(" ", max(nameWidth(width)+4-lineWidth(first), 1)), plain))
	l := first
	l.left = append(l.left, text(padTo(owner, 13), st), text(padTo(age, 10), st), text(why, st))
	l.left = append(l.left, extra...)
	return l
}

// nameWidth is the width of the secret's column, the design's 32 when the
// window allows.
func nameWidth(width int) int { return min(32, width-4-13-10-20) }

func lineWidth(l line) int { return width(l.left) }

// list is the rows that fit in height, the cursor's among them.
func (m *Secrets) list(width, height int) []line {
	if len(m.rows) == 0 {
		return []line{{left: []span{text("    no secrets yet: they come with the first deploy", dim)}}}
	}
	show := len(m.rows)
	if show > height {
		show = height - 1
	}
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+show {
		m.top = m.cursor - show + 1
	}
	m.top = min(m.top, len(m.rows)-show)
	var out []line
	for i := m.top; i < m.top+show; i++ {
		out = append(out, m.row(i, width))
	}
	if rest := len(m.rows) - show; rest > 0 {
		out = append(out, line{left: []span{text(fmt.Sprintf("    + %d more · sorted by risk", rest), dim)}})
	}
	return out
}

func (m *Secrets) row(i, width int) line {
	r := m.rows[i]
	box := text("[ ]", dim)
	if m.picked[r.Name] {
		box = text("[x]", blue)
	}
	label := r.Label
	if r.Kind == Accounts && r.Count > 0 {
		label = fmt.Sprintf("%s (%d)", label, r.Count)
	}
	if n := nameWidth(width); len([]rune(label)) > n-1 {
		label = string([]rune(label)[:n-2]) + "…"
	}
	name := text(label, plain)
	cursor := i == m.cursor && m.phase == listing
	if cursor {
		name = text(label, bold)
	}
	why := text("—", dim)
	switch {
	case r.Seen && r.Keep != "":
		why = text("seen · can't rotate: "+r.Keep, red)
	case r.Seen:
		why = text("seen in plain text", red)
	case r.Keep != "":
		why = text("can't rotate: "+r.Keep, amber)
	case m.old(r):
		why = text(fmt.Sprintf("older than %d days", m.oldAfter), amber)
	}
	l := m.columns(width, line{left: []span{box, text(" ", plain), name}}, r.Owner, age(m.now, r.Set), "", plain, why)
	if cursor {
		l.bg = colorSelect
	}
	return l
}

// age is how long ago t was, in the design's words: 1 day, 9 days, 3 months.
func age(now, t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := now.Sub(t)
	days := int(d.Hours() / 24)
	switch {
	case days < 1:
		return "today"
	case days == 1:
		return "1 day"
	case days < 60:
		return fmt.Sprintf("%d days", days)
	case days < 730:
		return fmt.Sprintf("%d months", days/30)
	}
	return fmt.Sprintf("%d years", days/365)
}

// panel is the box of what making the picked secrets anew does, then how it
// went.
func (m *Secrets) panel(width int) []line {
	var rows []line
	title := []span{text(fmt.Sprintf("Rotating the %d selected", len(m.review)), bold)}
	result := map[string]Rotated{}
	for _, r := range m.results {
		result[r.Name] = r
	}
	switch m.phase {
	case rotating:
		title = append(title, text("  ·  working", amber))
	case rotated:
		failed := 0
		for _, r := range m.results {
			if r.Err != nil {
				failed++
			}
		}
		title = []span{text(fmt.Sprintf("Rotated %d of %d", len(m.results)-failed, len(m.review)), bold)}
	}
	rows = append(rows, line{})
	takes := []string{}
	for i, r := range m.review {
		if r.Takes != "" && !slices.Contains(takes, r.Takes) {
			takes = append(takes, r.Takes)
		}
		mark, st, what := text("·", dim), dim, m.plan(r)
		if res, ok := result[r.Name]; ok {
			switch {
			case res.Err != nil:
				mark, st, what = text("✗", red), red, r.Label+": "+res.Err.Error()
			default:
				mark, what = text("✓", green), m.done(r, res)
			}
		} else if r.Kind != Generated && m.phase == reviewing {
			mark = text("!", amber)
		}
		for j, part := range wrap(what, width-6) {
			lead := text("  ", plain)
			if j == 0 {
				lead = text(mark.text+" ", mark.style)
			}
			rows = append(rows, line{left: []span{lead, text(part, st)}})
		}
		if m.phase == reviewing && i == m.asking {
			dots := strings.Repeat("•", min(len(m.input), 24))
			rows = append(rows, line{left: []span{text("    the new value: ", dim), text(dots, plain), text("▌", blue)}})
		}
	}
	if m.phase == rotated {
		ps := m.passwords()
		if len(ps) > 0 {
			rows = append(rows, line{}, line{left: []span{text("New passwords, never shown: ↵ copies one to send its person", dim)}})
			for i, p := range ps {
				l := line{left: []span{text("    ", plain), text(p.User, plain)}}
				if i == m.copyAt {
					l.left = []span{text("  › ", blue), text(p.User, bold)}
				}
				if m.copied[p.User] {
					l.right = []span{text("copied", green)}
				}
				rows = append(rows, l)
			}
		}
	}
	if len(takes) > 0 {
		rows = append(rows, line{}, line{left: []span{text("puts them in place: "+strings.Join(takes, " · "), dim)}})
	}
	return box(width, title, rows)
}

// plan says what making r anew does.
func (m *Secrets) plan(r SecretRow) string {
	switch r.Kind {
	case Asked:
		where := "where it is made"
		if u, err := url.Parse(r.NewAt); err == nil && u.Host != "" {
			where = "at " + u.Host
		}
		return r.Label + ": a new one is made " + where + " by you; damstack opens the page"
	case Accounts:
		return r.Label + ": a new password is set in Vault; the old one stops working at once, no deploy"
	}
	return r.Label + ": a new one is made here"
}

// done says what making r anew did.
func (m *Secrets) done(r SecretRow, res Rotated) string {
	switch r.Kind {
	case Accounts:
		return r.Label + ": set in Vault, the old one no longer works"
	case Asked:
		return r.Label + ": the new one is kept in vault.yml"
	}
	return r.Label + ": a new one is kept in vault.yml"
}

func (m *Secrets) footer(width int) line {
	var keys []span
	switch m.phase {
	case listing:
		keys = []span{text("space", key), text(" select   ", dim), text("↵", key), text(" rotate selected   ", dim),
			text("s", key), text(" mark one as seen   ", dim), text("p", key), text(" rotation policy   ", dim), text("esc", key), text(" back", dim)}
	case reviewing:
		if m.asking >= 0 {
			keys = []span{text("paste the new value, then ", dim), text("↵", key), text("   ", dim), text("ctrl+o", key), text(" open the page again   ", dim), text("esc", key), text(" back", dim)}
		} else {
			keys = []span{text("↵", key), text(" rotate them   ", dim), text("esc", key), text(" back", dim)}
		}
	case rotating:
		keys = []span{text("making them anew…", amber)}
	case rotated:
		keys = []span{text("esc", key), text(" back   ", dim), text("q", key), text(" quit", dim)}
		if m.needsDeploy() {
			keys = append([]span{text("d", key), text(" deploy now   ", dim)}, keys...)
		}
		if len(m.passwords()) > 0 {
			keys = append([]span{text("↵", key), text(" copy password   ", dim)}, keys...)
		}
	}
	return line{left: keys}
}
