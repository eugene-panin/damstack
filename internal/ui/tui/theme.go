// Package tui is damstack's terminal interface: full screens drawn from the
// events of the work, as the design draws them. Every screen is a Bubble Tea
// model, a pure function of the messages it got, so its frames are tested at
// fixed sizes.
package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// The palette of the design: meaning, not decoration. Green is done, amber
// running, red failed, blue waits for you. Each colour is the design's on a
// terminal with true colour, and one picked by hand from the 256 and the 16
// of terminals that have only those (Terminal.app among them): left to
// automatic rounding, the rules' grey comes out navy.
var (
	colorBg, colorFg, colorDim                  color.Color
	colorGreen, colorAmber, colorRed, colorBlue color.Color
	colorWhite, colorRule, colorKeyBg           color.Color
	colorKeyFg, colorSelect                     color.Color
)

// palette is each colour as ANSI, ANSI 256 and true colour.
var palette = []struct {
	into                *color.Color
	ansi, ansi256, true string
}{
	{&colorBg, "0", "233", "#0E1014"},
	{&colorFg, "7", "253", "#D8DBE2"},
	{&colorDim, "8", "245", "#8A93A3"},
	{&colorGreen, "2", "78", "#6BD49A"},
	{&colorAmber, "3", "215", "#F0B14A"},
	{&colorRed, "1", "210", "#F27474"},
	{&colorBlue, "4", "111", "#7AB8FF"},
	{&colorWhite, "15", "231", "#FFFFFF"},
	{&colorRule, "8", "236", "#2A2F3A"},
	{&colorKeyBg, "8", "236", "#262B36"},
	{&colorKeyFg, "15", "254", "#E6E8EE"},
	{&colorSelect, "0", "235", "#1E2633"},
}

func init() { UseProfile(colorprofile.TrueColor) }

// UseProfile picks the colours for what the terminal can show, as
// colorprofile.Detect tells. Call it before a screen is shown.
func UseProfile(p colorprofile.Profile) {
	pick := lipgloss.Complete(p)
	for _, c := range palette {
		*c.into = pick(lipgloss.Color(c.ansi), lipgloss.Color(c.ansi256), lipgloss.Color(c.true))
	}
	styles = map[style]lipgloss.Style{
		plain: lipgloss.NewStyle().Foreground(colorFg),
		dim:   lipgloss.NewStyle().Foreground(colorDim),
		green: lipgloss.NewStyle().Foreground(colorGreen),
		amber: lipgloss.NewStyle().Foreground(colorAmber),
		red:   lipgloss.NewStyle().Foreground(colorRed),
		blue:  lipgloss.NewStyle().Foreground(colorBlue),
		bold:  lipgloss.NewStyle().Foreground(colorWhite).Bold(true),
		key:   lipgloss.NewStyle().Foreground(colorKeyFg).Background(colorKeyBg).Padding(0, 1),
		rule:  lipgloss.NewStyle().Foreground(colorRule),
	}
}

// style is the text style of a part of a line.
type style int

const (
	plain style = iota
	dim
	green
	amber
	red
	blue
	bold // white and bold: what is in focus
	key  // a key in the line of keys
	rule
	raw // already drawn: passed as it is
)

var styles map[style]lipgloss.Style

// render draws text in a style, on bg when bg is not nil.
func render(s style, text string, bg color.Color) string {
	if s == raw {
		return text
	}
	st := styles[s]
	if bg != nil && s != key {
		st = st.Background(bg)
	}
	return st.Render(text)
}
