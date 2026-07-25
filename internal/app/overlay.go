package app

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/1broseidon/ghostmux/internal/rail"
)

// Help is an overlay, not a mode, and that is a rule rather than a taste. The
// two panes have a contract — left selects, right shows what is selected — and
// a flat reference table has nothing to select, so it cannot honor it. Putting
// it in the right pane would leave the rail cursor pointing at a session while
// the pane showed something else entirely.
//
// It is not in the rail either. At 30 columns, 9 of the 17 keymap rows
// truncated — including `ctrl+\  toggle rail ⇄ vi…`, the one row a user whose
// desktop grabbed the chord actually needs intact. The overlay is 56 columns
// wide because that is where nothing truncates (enforced by a test against the
// real table, not a fixture).

// boxLine is one line of an overlay box: already-styled text plus the display
// width it occupies. The width is carried rather than measured because padding
// must count glyphs, never the escape bytes wrapped around them.
type boxLine struct {
	body  string
	width int
}

const (
	hexOverlayBg     = "#1d2021" // one step below the bar: the box sits on top
	hexOverlayBorder = "#504945"
	hexOverlayTitle  = "#fe8019"
	hexOverlayKey    = "#fabd2f"
	hexOverlayDesc   = "#a89984"
	hexOverlayFoot   = "#665c54"

	hexLegendBell  = "#fb4934"
	hexLegendDone  = "#b8bb26"
	hexLegendAct   = "#fabd2f"
	hexLegendView  = "#fe8019"
	hexLegendGhost = "#665c54"

	overlayMaxWidth = 56
)

// compose splices box over base at column x, row y. It is the term package's
// overlayCursor idiom widened from one cell to a rectangle: cut the base line
// left of the box, drop the box line in, cut the base line right of it. Every
// spliced line keeps the base's exact display width, because the frame's whole
// layout rests on that — a line one cell off makes the divider zig-zag.
func compose(base string, box []string, x, y int) string {
	if len(box) == 0 {
		return base
	}
	lines := strings.Split(base, "\n")
	for i, bl := range box {
		row := y + i
		if row < 0 || row >= len(lines) {
			continue // the box hangs off the frame: draw what fits, drop the rest
		}
		line := lines[row]
		width := ansi.StringWidth(line)
		bw := ansi.StringWidth(bl)
		if x < 0 || x >= width {
			continue
		}
		left := ansi.Cut(line, 0, x)
		right := ""
		if x+bw < width {
			right = ansi.Cut(line, x+bw, width)
		}
		// A reset after the box so its background cannot bleed into the frame,
		// and one at the end of the line so nothing bleeds into the next row.
		lines[row] = left + "\x1b[0m" + bl + "\x1b[0m" + right + "\x1b[0m"
	}
	return strings.Join(lines, "\n")
}

// helpBox renders the keys overlay as a list of equal-width lines, ready for
// compose. Width is the frame's, capped at the width where nothing truncates.
func helpBox(frameWidth int) []string {
	w := min(overlayMaxWidth, frameWidth-4)
	if w < 12 {
		return nil // no room for a box that could say anything
	}
	inner := w - 4 // one border + one space of padding on each side

	border := lipgloss.NewStyle().Foreground(lipgloss.Color(hexOverlayBorder)).Background(lipgloss.Color(hexOverlayBg))
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(hexOverlayTitle)).Background(lipgloss.Color(hexOverlayBg)).Bold(true)
	keySty := lipgloss.NewStyle().Foreground(lipgloss.Color(hexOverlayKey)).Background(lipgloss.Color(hexOverlayBg))
	descSty := lipgloss.NewStyle().Foreground(lipgloss.Color(hexOverlayDesc)).Background(lipgloss.Color(hexOverlayBg))
	footSty := lipgloss.NewStyle().Foreground(lipgloss.Color(hexOverlayFoot)).Background(lipgloss.Color(hexOverlayBg))

	var rows []boxLine

	entries := rail.HelpEntries()
	keyw := 0
	for _, e := range entries {
		keyw = max(keyw, len([]rune(e.Key)))
	}
	for _, e := range entries {
		key := padLeft(e.Key, keyw)
		desc := truncateRunes(e.Desc, max(inner-keyw-2, 1))
		rows = append(rows, boxLine{keySty.Render(key) + descSty.Render("  "+desc), keyw + 2 + len([]rune(desc))})
	}

	rows = append(rows, boxLine{})
	rows = append(rows, legendLine(inner))
	if f := rail.ToggleFooter(); f != "" {
		f = truncateRunes(f, inner)
		rows = append(rows, boxLine{footSty.Render(f), len([]rune(f))})
	}
	rows = append(rows, boxLine{})
	closer := truncateRunes("any key closes", inner)
	rows = append(rows, boxLine{footSty.Render(closer), len([]rune(closer))})

	titleText := " ghostmux · keys "
	if len([]rune(titleText)) > w-4 {
		titleText = " keys "
	}
	dashes := w - 2 - 1 - len([]rune(titleText))
	out := []string{border.Render("╭─") + title.Render(titleText) + border.Render(strings.Repeat("─", max(dashes, 0))+"╮")}
	for _, r := range rows {
		out = append(out, border.Render("│ ")+r.body+
			lipgloss.NewStyle().Background(lipgloss.Color(hexOverlayBg)).Render(strings.Repeat(" ", max(inner-r.width, 0)))+
			border.Render(" │"))
	}
	out = append(out, border.Render("╰"+strings.Repeat("─", w-2)+"╯"))
	return out
}

// legendLine is the gutter key: the glyphs the rail draws, in the colors it
// draws them, so the overlay is a legend and not a second vocabulary.
func legendLine(inner int) boxLine {
	glyph := func(hex, g, label string) string {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Background(lipgloss.Color(hexOverlayBg)).Render(g) +
			lipgloss.NewStyle().Foreground(lipgloss.Color(hexOverlayDesc)).Background(lipgloss.Color(hexOverlayBg)).Render(" "+label+"  ")
	}
	parts := []struct{ hex, g, label string }{
		{hexLegendBell, "●", "bell"},
		{hexLegendDone, "✓", "done"},
		{hexLegendAct, "~", "act"},
		{hexLegendView, "▸", "viewing"},
		{hexLegendGhost, "○", "ghost"},
	}
	var b strings.Builder
	w := 0
	for _, p := range parts {
		add := 1 + 1 + len([]rune(p.label)) + 2
		if w+add > inner {
			break
		}
		b.WriteString(glyph(p.hex, p.g, p.label))
		w += add
	}
	return boxLine{b.String(), w}
}

// helpOverlay composes the help box centered over an already-rendered frame.
func helpOverlay(frame string, w, h int) string {
	box := helpBox(w)
	if len(box) == 0 {
		return frame
	}
	bw := ansi.StringWidth(box[0])
	return compose(frame, box, max((w-bw)/2, 0), max((h-len(box))/2, 0))
}

// padLeft right-aligns s in width display columns (keys column).
func padLeft(s string, width int) string {
	if n := width - len([]rune(s)); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// truncateRunes cuts plain text to width runes. The overlay is sized so this
// never fires on a real keymap row — a test enforces that — but a 20-column
// terminal is still allowed to open the panel.
func truncateRunes(s string, width int) string {
	r := []rune(s)
	if width <= 0 {
		return ""
	}
	if len(r) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}
