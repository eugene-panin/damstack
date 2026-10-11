package tui

import (
	"image/color"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// span is a piece of a line in one style.
type span struct {
	text  string
	style style
}

// line is what one row of a screen shows: spans from the left, spans pushed to
// the right, and a background for the whole row (nil for none). Rows never
// wrap: whatever does not fit is cut, so a frame keeps its shape whatever the
// window's width.
type line struct {
	left  []span
	right []span
	bg    color.Color
}

func text(t string, s style) span { return span{t, s} }

func (l line) String(width int) string {
	if width <= 0 {
		return ""
	}
	// Widths are measured on what is drawn: a key's padding takes cells too.
	var right strings.Builder
	for _, s := range l.right {
		right.WriteString(render(s.style, s.text, l.bg))
	}
	rightWidth := ansi.StringWidth(right.String())
	if rightWidth > 0 && rightWidth+2 > width {
		right.Reset() // too narrow for both: the left side wins
		rightWidth = 0
	}
	leftRoom := width
	if rightWidth > 0 {
		leftRoom = width - rightWidth - 1
	}
	var b strings.Builder
	used := 0
	for _, s := range l.left {
		if used >= leftRoom {
			break
		}
		drawn := render(s.style, s.text, l.bg)
		if w := ansi.StringWidth(drawn); used+w > leftRoom {
			drawn = ansi.Truncate(drawn, leftRoom-used, "…")
		}
		b.WriteString(drawn)
		used += ansi.StringWidth(drawn)
	}
	b.WriteString(render(plain, strings.Repeat(" ", width-used-rightWidth), l.bg))
	b.WriteString(right.String())
	return b.String()
}

// wrap cuts text into lines of at most width cells, at spaces where it can.
func wrap(t string, width int) []string {
	if width <= 0 {
		return nil
	}
	var out []string
	for _, para := range strings.Split(t, "\n") {
		w := ansi.Wrap(para, width, " /")
		out = append(out, strings.Split(w, "\n")...)
	}
	return out
}
