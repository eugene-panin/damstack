package tui

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/eugene-panin/damstack/internal/event"
)

// The smallest window the screen is drawn in; below it, it says so.
const (
	minWidth  = 80
	minHeight = 20
)

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type stepState int

const (
	pending stepState = iota
	running
	stepDone
	skipped
	failed
)

type stepView struct {
	event.PlannedStep
	state       stepState
	took        time.Duration
	done, total int
}

type taskView struct {
	name    string
	status  event.TaskStatus
	took    time.Duration
	detail  string
	started time.Time
}

// pane is what the right side shows.
type pane int

const (
	tasksPane pane = iota
	logPane
	planPane
	detailsPane
)

// Actions are what the screen's keys do to the work outside it.
type Actions struct {
	// Stop asks the work to stop after the step running now.
	Stop func()
	// Abort stops it at once.
	Abort func()
	// SSH is the command that opens a shell on the server, run with the
	// screen set aside.
	SSH func() *exec.Cmd
	// Open opens an address in the browser; Check says whether it answers
	// (its HTTP status, or why not); CopyToken puts the token of that name in
	// the clipboard; QR is the command that shows the WireGuard configuration
	// as a QR code.
	Open      func(url string) error
	Check     func(url string) string
	CopyToken func(name string) error
	QR        func() *exec.Cmd
	// Copy puts text in the clipboard.
	Copy func(text string) error
}

// Deploy is the deploy screen (design: 2 · Deploy progress): the steps on the
// left, the step running now on the right, the keys below.
type Deploy struct {
	actions Actions

	run   event.RunStarted
	steps []stepView
	cur   int

	kind, what, log string
	tasks           []taskView
	plan            []string
	notes           []string
	waiting         *event.Waiting

	started time.Time
	now     time.Time
	frame   int
	width   int
	height  int

	pane     pane
	logTail  []string
	confirm  *confirmMsg
	stopping bool
	aborting bool
	finished bool
	err      error
	failure  *event.StepFailed

	// The done screen: what the run set up, how each address answers, the
	// one chosen, and a word on what a key just did.
	summary *event.RunDone
	// retry: the person asked, on the failure screen, to run again.
	retry  bool
	checks map[string]string
	cursor int
	flash  string
}

// NewDeploy is the deploy screen, its keys doing actions.
func NewDeploy(actions Actions) *Deploy {
	now := time.Now()
	return &Deploy{actions: actions, cur: -1, started: now, now: now, width: 100, height: 30}
}

// Messages the screen takes besides Bubble Tea's own.
type (
	eventMsg   struct{ event.Event }
	tickMsg    time.Time
	finishMsg  struct{ err error }
	logMsg     []string
	confirmMsg struct {
		question string
		answer   chan<- bool
	}
)

func tick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *Deploy) Init() tea.Cmd { return tick() }

func (m *Deploy) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		m.now = time.Time(msg)
		m.frame++
		cmds := []tea.Cmd{tick()}
		if m.pane == logPane && m.frame%8 == 0 && m.log != "" {
			cmds = append(cmds, readLog(m.log))
		}
		return m, tea.Batch(cmds...)
	case logMsg:
		m.logTail = msg
	case eventMsg:
		m.apply(msg.Event)
	case confirmMsg:
		m.confirm = &msg
	case finishMsg:
		m.finished, m.err = true, msg.err
		switch {
		case msg.err == nil && m.summary != nil:
			return m, m.checkLinks()
		case msg.err == nil:
			return m, tea.Tick(800*time.Millisecond, func(time.Time) tea.Msg { return tea.QuitMsg{} })
		}
	case checkedMsg:
		if m.checks == nil {
			m.checks = map[string]string{}
		}
		m.checks[msg.url] = msg.status
	case flashMsg:
		m.flash = string(msg)
	case tea.KeyPressMsg:
		switch {
		case m.done():
			return m, m.doneKey(msg.String())
		case m.failedScreen():
			return m, m.failureKey(msg.String())
		}
		return m, m.key(msg.String())
	}
	return m, nil
}

func (m *Deploy) key(k string) tea.Cmd {
	if m.confirm != nil {
		switch k {
		case "y", "enter":
			m.confirm.answer <- true
			m.confirm = nil
		case "n", "esc":
			m.confirm.answer <- false
			m.confirm = nil
		}
		return nil
	}
	switch k {
	case "ctrl+c":
		if m.stopping || m.finished {
			m.aborting = true
			if m.actions.Abort != nil {
				m.actions.Abort()
			}
			return tea.Quit
		}
		m.stop()
	case "q":
		if m.finished {
			return tea.Quit
		}
		m.stop()
	case "l":
		m.toggle(logPane)
		if m.pane == logPane && m.log != "" {
			return readLog(m.log)
		}
	case "p":
		m.toggle(planPane)
	case "d":
		m.toggle(detailsPane)
	case "s":
		if m.actions.SSH != nil {
			return tea.ExecProcess(m.actions.SSH(), func(error) tea.Msg { return nil })
		}
	}
	return nil
}

func (m *Deploy) toggle(p pane) {
	if m.pane == p {
		m.pane = tasksPane
	} else {
		m.pane = p
	}
}

func (m *Deploy) stop() {
	if m.stopping {
		return
	}
	m.stopping = true
	if m.actions.Stop != nil {
		m.actions.Stop()
	}
}

// apply takes in what the work said.
func (m *Deploy) apply(e event.Event) {
	switch e := e.(type) {
	case event.RunStarted:
		m.run = e
		m.steps = m.steps[:0]
		for _, s := range e.Steps {
			m.steps = append(m.steps, stepView{PlannedStep: s})
		}
		m.waiting = nil
	case event.Waiting:
		m.waiting = &e
	case event.StepStarted:
		m.cur = e.N - 1
		if s := m.step(); s != nil {
			s.state = running
		}
		m.kind, m.what, m.log = e.Kind, e.What, e.Log
		m.tasks, m.plan, m.notes, m.logTail = nil, nil, nil, nil
	case event.StepSkipped:
		if s := m.at(e.N - 1); s != nil {
			s.state = skipped
		}
	case event.StepDone:
		if s := m.at(e.N - 1); s != nil {
			s.state, s.took = stepDone, e.Took
		}
	case event.StepFailed:
		if s := m.at(e.N - 1); s != nil {
			s.state = failed
		}
		m.failure = &e
	case event.TaskPlan:
		m.plan = e.Names
	case event.Task:
		m.task(e)
	case event.Note:
		m.notes = append(m.notes, e.Text)
	case event.Heading:
		m.notes = append(m.notes, e.Text)
	case event.RunDone:
		m.summary = &e
	}
}

func (m *Deploy) at(i int) *stepView {
	if i < 0 || i >= len(m.steps) {
		return nil
	}
	return &m.steps[i]
}

func (m *Deploy) step() *stepView { return m.at(m.cur) }

func (m *Deploy) task(e event.Task) {
	if s := m.step(); s != nil {
		s.done, s.total = e.Done, e.Total
	}
	for i := len(m.tasks) - 1; i >= 0; i-- {
		if m.tasks[i].name == e.Name {
			t := &m.tasks[i]
			t.status, t.detail = e.Status, e.Detail
			if e.Took > 0 {
				t.took = e.Took
			} else if e.Status != event.TaskRunning {
				t.took = m.now.Sub(t.started)
			}
			return
		}
	}
	m.tasks = append(m.tasks, taskView{name: e.Name, status: e.Status, took: e.Took, detail: e.Detail, started: m.now})
}

func readLog(path string) tea.Cmd {
	return func() tea.Msg {
		data, err := os.ReadFile(path)
		if err != nil {
			return logMsg(nil)
		}
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		if len(lines) > 200 {
			lines = lines[len(lines)-200:]
		}
		return logMsg(lines)
	}
}

// View draws the screen as the design does, for the window's size.
func (m *Deploy) View() tea.View {
	v := tea.NewView(m.Frame())
	v.AltScreen = true
	v.BackgroundColor = colorBg
	v.ForegroundColor = colorFg
	return v
}

// Frame is the screen's text for the window's size: exactly height rows of
// exactly width cells.
func (m *Deploy) Frame() string {
	w, h := m.width, m.height
	if w >= minWidth && h >= minHeight {
		switch {
		case m.done():
			return m.doneFrame()
		case m.failedScreen():
			return m.failureFrame()
		}
	}
	if w < minWidth || h < minHeight {
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
	pad := 2
	inner := w - 2*pad
	rows := []line{m.header(), {left: []span{text(strings.Repeat("─", inner), rule)}}}
	bodyHeight := h - 4
	rows = append(rows, m.body(inner, bodyHeight)...)
	rows = append(rows, line{left: []span{text(strings.Repeat("─", inner), rule)}}, m.footer())
	out := make([]string, len(rows))
	margin := line{}.String(pad)
	for i, r := range rows {
		out[i] = margin + r.String(inner) + margin
	}
	return strings.Join(out, "\n")
}

func (m *Deploy) header() line {
	l := line{left: []span{text("damstack", bold), text(" "+orDash(m.run.Command, "deploy")+" ", dim), text(m.run.Project, bold)}}
	meta := m.run.Stack
	if m.run.Server != "" {
		meta += " · " + m.run.Server
	}
	if meta != "" {
		l.left = append(l.left, text("  "+meta, dim))
	}
	l.right = []span{text(clock2(m.now.Sub(m.started)), amber)}
	if est := m.estimate(); est > 0 {
		l.right = append(l.right, text(" / ~"+short(est), dim))
	}
	return l
}

// body is the left column of steps and the right pane, side by side.
func (m *Deploy) body(width, height int) []line {
	left := min(max(width*38/100, 30), 46)
	right := width - left - 5
	lefts := m.stepsColumn(left, height)
	rights := m.rightPane(right, height)
	rows := make([]line, height)
	for i := range rows {
		rows[i] = line{left: []span{
			text(lefts[i], raw), text("  ", plain), text("│", rule), text("  ", plain), text(rights[i], raw),
		}}
	}
	return rows
}

// The columns are drawn to strings of exact width first, then joined.

func (m *Deploy) stepsColumn(width, height int) []string {
	var rows []string
	add := func(l line) { rows = append(rows, l.String(width)) }
	appsDone := false
	for i := range m.steps {
		s := &m.steps[i]
		if strings.Contains(s.Name, "/") {
			if !appsDone {
				appsDone = true
				for _, l := range m.appsRows(width) {
					add(l)
				}
			}
			continue
		}
		for _, l := range m.stepRows(s, width) {
			add(l)
		}
	}
	if len(m.steps) == 0 && m.waiting != nil {
		add(line{left: []span{text("waiting for the lock", amber)}})
	}
	logRows := []string{}
	if m.log != "" {
		for i, part := range wrap(m.log, width-5) {
			label := "     "
			if i == 0 {
				label = "log  "
			}
			logRows = append(logRows, line{left: []span{text(label+part, dim)}}.String(width))
		}
		if len(logRows) > 2 {
			logRows = logRows[:2]
		}
	}
	for len(rows) < height-len(logRows) {
		rows = append(rows, line{}.String(width))
	}
	rows = append(rows[:height-len(logRows)], logRows...)
	return rows
}

func (m *Deploy) stepRows(s *stepView, width int) []line {
	switch s.state {
	case running:
		count := ""
		if s.total > 0 {
			count = fmt.Sprintf("%d/%d", s.done, s.total)
		}
		rows := []line{{bg: colorSelect, left: []span{text("● ", amber), text(s.Title, bold)}, right: []span{text(count, amber)}}}
		if s.total > 0 {
			bar := width - 8
			filled := bar * s.done / s.total
			rows = append(rows, line{left: []span{
				text("  "+strings.Repeat("█", filled), amber), text(strings.Repeat("░", bar-filled), dim),
				text(fmt.Sprintf(" %d%%", 100*s.done/s.total), dim),
			}})
		}
		return rows
	case stepDone:
		return []line{{left: []span{text("✓ ", green), text(s.Title, plain)}, right: []span{text(clock(s.took), dim)}}}
	case skipped:
		return []line{{left: []span{text("✓ ", green), text(s.Title, plain)}, right: []span{text("once", dim)}}}
	case failed:
		return []line{{left: []span{text("✗ ", red), text(s.Title, red)}}}
	}
	return []line{{left: []span{text("○ "+s.Title, dim)}, right: []span{text(approx(s.Takes), dim)}}}
}

// appsRows are the apps as one group: a row for the group, then each app as a
// row of its own, as a step is drawn, under it.
func (m *Deploy) appsRows(width int) []line {
	var apps []line
	state := pending
	n := 0
	for i := range m.steps {
		s := m.steps[i]
		app, _, ok := strings.Cut(s.Name, "/")
		if !ok {
			continue
		}
		n++
		s.Title = app
		for _, l := range m.stepRows(&s, width-2) {
			l.left = append([]span{text("  ", plain)}, l.left...)
			apps = append(apps, l)
		}
		switch {
		case s.state == failed:
			state = failed
		case s.state == running && state != failed:
			state = running
		}
	}
	allDone := true
	for _, s := range m.steps {
		if strings.Contains(s.Name, "/") && s.state != stepDone && s.state != skipped {
			allDone = false
		}
	}
	head := line{left: []span{text("○ Apps", dim)}, right: []span{text(strconv.Itoa(n), dim)}}
	switch {
	case state == failed:
		head.left = []span{text("✗ ", red), text("Apps", red)}
	case state == running:
		head.left = []span{text("● ", amber), text("Apps", plain)}
	case allDone:
		head.left = []span{text("✓ ", green), text("Apps", plain)}
	}
	return append([]line{head}, apps...)
}

func (m *Deploy) rightPane(width, height int) []string {
	var rows []line
	switch {
	case m.finished && m.err == nil:
		rows = []line{{left: []span{text("✓ ", green), text(m.run.Project+" is up", bold)}, right: []span{text("took "+clock(m.now.Sub(m.started)), dim)}}}
	case m.finished:
		rows = m.failedRows(width)
	case m.pane == logPane:
		rows = m.logRows(height)
	case m.pane == planPane:
		rows = m.planRows()
	case m.pane == detailsPane:
		rows = m.detailRows()
	default:
		rows = m.taskRows(height)
	}
	out := make([]string, height)
	for i := range out {
		if i < len(rows) {
			out[i] = rows[i].String(width)
		} else {
			out[i] = line{}.String(width)
		}
	}
	return out
}

func (m *Deploy) title() []line {
	s := m.step()
	if s == nil {
		if m.waiting != nil {
			who := "someone else"
			if m.waiting.Holder != "" {
				who = m.waiting.Holder
			}
			return []line{{left: []span{text("‖  ", amber), text("Waiting for the lock", bold)}}, {left: []span{text(who, dim)}}}
		}
		return []line{{left: []span{text("Starting", bold)}}}
	}
	meta := []string{}
	for _, p := range []string{m.kind, m.what} {
		if p != "" {
			meta = append(meta, p)
		}
	}
	changed, ok := 0, 0
	for _, t := range m.tasks {
		switch t.status {
		case event.TaskChanged:
			changed++
		case event.TaskOK:
			ok++
		}
	}
	if changed > 0 || ok > 0 {
		meta = append(meta, fmt.Sprintf("changed %d", changed), fmt.Sprintf("ok %d", ok))
	} else if len(m.notes) > 0 {
		meta = append(meta, m.notes[len(m.notes)-1])
	}
	return []line{{left: []span{text(s.Title, bold)}}, {left: []span{text(strings.Join(meta, " · "), dim)}}}
}

func (m *Deploy) taskRows(height int) []line {
	rows := append(m.title(), line{})
	room := height - len(rows)
	var finished, current []line
	seen := map[string]bool{}
	for _, t := range m.tasks {
		seen[t.name] = true
		if t.status == event.TaskSkipped {
			continue
		}
		switch t.status {
		case event.TaskRunning:
			current = append(current, line{
				left:  []span{text(spinner[m.frame%len(spinner)]+" ", amber), text(t.name, bold)},
				right: []span{text(orDash(t.detail, seconds(m.now.Sub(t.started))), dim)},
			})
		case event.TaskFailed:
			finished = append(finished, line{left: []span{text("✗ ", red), text(t.name, red)}, right: []span{text(seconds(t.took), dim)}})
		case event.TaskChanged:
			finished = append(finished, line{left: []span{text("✓ ", green), text(t.name, plain)}, right: []span{text("changed", amber)}})
		default:
			finished = append(finished, line{left: []span{text("✓ ", green), text(t.name, plain)}, right: []span{text(orDash(t.detail, seconds(t.took)), dim)}})
		}
	}
	var upcoming []string
	for _, name := range m.plan {
		if !seen[name] {
			upcoming = append(upcoming, name)
		}
	}
	more := 0
	if s := m.step(); s != nil && s.total > 0 {
		more = max(s.total-s.done-len(current)-len(upcoming), 0)
	}
	show := min(len(upcoming), 5)
	extra := len(upcoming) - show + more
	tail := len(current) + show
	if extra > 0 {
		tail++
	}
	keep := max(room-tail, 0)
	if len(finished) > keep {
		finished = finished[len(finished)-keep:]
	}
	rows = append(rows, finished...)
	rows = append(rows, current...)
	for _, name := range upcoming[:show] {
		rows = append(rows, line{left: []span{text("· "+name, dim)}})
	}
	if extra > 0 {
		rows = append(rows, line{left: []span{text(fmt.Sprintf("  + %d more", extra), dim)}})
	}
	return rows
}

func (m *Deploy) logRows(height int) []line {
	rows := []line{{left: []span{text("log  ", bold), text(m.log, dim)}}, {}}
	lines := m.logTail
	if room := height - len(rows); len(lines) > room {
		lines = lines[len(lines)-room:]
	}
	for _, l := range lines {
		rows = append(rows, line{left: []span{text(l, dim)}})
	}
	return rows
}

func (m *Deploy) planRows() []line {
	rows := append(m.title(), line{})
	status := map[string]event.TaskStatus{}
	for _, t := range m.tasks {
		status[t.name] = t.status
	}
	for _, name := range m.plan {
		st, ok := status[name]
		switch {
		case !ok:
			rows = append(rows, line{left: []span{text("· "+name, dim)}})
		case st == event.TaskRunning:
			rows = append(rows, line{left: []span{text("● ", amber), text(name, bold)}})
		case st == event.TaskFailed:
			rows = append(rows, line{left: []span{text("✗ ", red), text(name, red)}})
		default:
			rows = append(rows, line{left: []span{text("✓ ", green), text(name, plain)}})
		}
	}
	if len(m.plan) == 0 {
		rows = append(rows, line{left: []span{text("no plan for this step yet", dim)}})
	}
	return rows
}

func (m *Deploy) detailRows() []line {
	rows := append(m.title(), line{})
	s := m.step()
	if s != nil {
		rows = append(rows,
			line{left: []span{text("step   ", dim), text(fmt.Sprintf("%d of %d · %s", s.N, s.Total, s.Name), plain)}},
			line{left: []span{text("takes  ", dim), text(orDash(s.Takes, "not said"), plain)}},
			line{left: []span{text("tasks  ", dim), text(fmt.Sprintf("%d of %d", s.done, s.total), plain)}},
		)
	}
	if m.log != "" {
		rows = append(rows, line{left: []span{text("log    ", dim), text(m.log, plain)}})
	}
	if len(m.notes) > 0 {
		rows = append(rows, line{})
		for _, n := range m.notes {
			rows = append(rows, line{left: []span{text(n, dim)}})
		}
	}
	return rows
}

func (m *Deploy) failedRows(width int) []line {
	title := "the deploy stopped"
	if m.failure != nil {
		title = m.failure.Title + " failed"
	}
	rows := []line{{left: []span{text("✗ ", red), text(title, bold)}}, {}}
	if m.err != nil {
		for _, l := range wrap(m.err.Error(), width) {
			rows = append(rows, line{left: []span{text(l, plain)}})
		}
	}
	if m.failure != nil && len(m.failure.Tail) > 0 {
		rows = append(rows, line{}, line{left: []span{text("last lines of the log", dim)}})
		tail := m.failure.Tail
		if len(tail) > 8 {
			tail = tail[len(tail)-8:]
		}
		for _, l := range tail {
			rows = append(rows, line{left: []span{text(l, dim)}})
		}
	}
	return rows
}

func (m *Deploy) footer() line {
	if m.confirm != nil {
		return line{left: []span{text(m.confirm.question, blue), text("   ", plain),
			text("y", key), text(" yes   ", dim), text("n", key), text(" no", dim)}}
	}
	if m.finished {
		if m.err != nil {
			return line{left: []span{text("l", key), text(" log   ", dim), text("s", key), text(" ssh   ", dim), text("q", key), text(" quit", dim)}}
		}
		return line{left: []span{text("done", dim)}}
	}
	keys := []span{
		text("l", key), text(" log   ", dim), text("d", key), text(" details   ", dim), text("p", key), text(" plan   ", dim),
		text("s", key), text(" ssh   ", dim),
	}
	// The design's line, then shorter ones for narrower windows: the first
	// that fits whole is shown.
	var variants [][]span
	if m.stopping {
		variants = [][]span{
			append(slices.Clone(keys), text("stopping after this step · ctrl+c again stops at once", amber)),
			append(slices.Clone(keys), text("stopping after this step", amber)),
		}
	} else {
		variants = [][]span{
			append(slices.Clone(keys), text("q", key), text(" quit — this step finishes, deploy resumes from here", dim)),
			append(slices.Clone(keys), text("q", key), text(" quit after this step", dim)),
			append(slices.Clone(keys), text("q", key), text(" quit", dim)),
		}
	}
	for _, v := range variants {
		if width(v) <= m.width-4 {
			return line{left: v}
		}
	}
	return line{left: variants[len(variants)-1]}
}

// width is how many cells spans take once drawn.
func width(spans []span) int {
	n := 0
	for _, s := range spans {
		n += ansi.StringWidth(render(s.style, s.text, nil))
	}
	return n
}

// estimate is what the steps take, as their stacks say.
func (m *Deploy) estimate() time.Duration {
	var d time.Duration
	for _, s := range m.steps {
		d += takes(s.Takes)
	}
	return d
}

// takes reads a stack's "6 min", "1 min" or "10 s".
func takes(t string) time.Duration {
	f := strings.Fields(t)
	if len(f) != 2 {
		return 0
	}
	n, err := strconv.Atoi(f[0])
	if err != nil {
		return 0
	}
	switch f[1] {
	case "min", "mins", "minutes", "minute":
		return time.Duration(n) * time.Minute
	case "s", "sec", "seconds", "second":
		return time.Duration(n) * time.Second
	}
	return 0
}

func approx(t string) string {
	if d := takes(t); d > 0 {
		if d >= time.Minute {
			return "~" + short(d)
		}
		return short(d)
	}
	return ""
}

// clock is a duration as minutes and seconds: 4:12, or 04:12 for the header.
func clock(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

func short(d time.Duration) string {
	if d >= time.Minute {
		return fmt.Sprintf("%dm", int(d.Round(time.Minute).Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func seconds(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return clock(d)
}

func orDash(s, otherwise string) string {
	if s == "" {
		return otherwise
	}
	return s
}

// clock2 is a duration as two-digit minutes and seconds, for the header: 04:12.
func clock2(d time.Duration) string {
	d = d.Round(time.Second)
	return fmt.Sprintf("%02d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}
