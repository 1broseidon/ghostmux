package rail

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Gruvbox Dark Hard hex table, per SPEC.md §4 — the ONLY colors allowed in
// the rail. Hex, not ANSI numbers: ghostty renders truecolor.
const (
	hexTitleAccent  = "#fe8019" // ▍ / running / in-view ▸
	hexTitleName    = "#8ec07c" // "ghostmux" / in-view session name
	hexTitleTail    = "#928374" // " ▸ rail" / hints / dim / collapse arrows
	hexSessionName  = "#ebdbb2" // session name, not in view
	hexAttached     = "#b8bb26" // attached ● / active window / done ✓
	hexBell         = "#fb4934" // bell ● / errors / kill confirm y
	hexActivity     = "#fabd2f" // activity ~ / filter prompt /
	hexCursorBg     = "#504945" // cursor bar background / dimmed fg
	hexInactiveWin  = "#928374"
	hexFilterCursor = "#ebdbb2"
)

var (
	styTitleAccent = lipgloss.NewStyle().Foreground(lipgloss.Color(hexTitleAccent))
	styTitleName   = lipgloss.NewStyle().Foreground(lipgloss.Color(hexTitleName)).Bold(true)
	styTitleTail   = lipgloss.NewStyle().Foreground(lipgloss.Color(hexTitleTail))
	styHint        = lipgloss.NewStyle().Foreground(lipgloss.Color(hexTitleTail))
	styBell        = lipgloss.NewStyle().Foreground(lipgloss.Color(hexBell)).Bold(true)
	styError       = lipgloss.NewStyle().Foreground(lipgloss.Color(hexBell))
	styActivity    = lipgloss.NewStyle().Foreground(lipgloss.Color(hexActivity))
	styDim         = lipgloss.NewStyle().Foreground(lipgloss.Color(hexCursorBg))
)

// rowStyle returns the styling primitive for one colored run of text within a
// row: fg color, optional bold, and — for the selected row — the cursor bar's
// bg #504945 kept UNDER every glyph's own color (never Reverse).
func rowStyle(fg string, bold, cursor bool) lipgloss.Style {
	s := lipgloss.NewStyle().Foreground(lipgloss.Color(fg))
	if bold {
		s = s.Bold(true)
	}
	if cursor {
		s = s.Background(lipgloss.Color(hexCursorBg))
	}
	return s
}

func (m railModel) View() string {
	height := m.height
	if height <= 0 {
		height = 24
	}
	treeHeight := height - 4
	if treeHeight < 1 {
		treeHeight = 1
	}

	if m.helpFallback {
		return helpFallbackPage(height)
	}

	var b strings.Builder
	b.WriteString(titleLine() + "\n\n")

	vis := m.visible()
	if len(vis) == 0 {
		b.WriteString(emptyStateBody(treeHeight))
	} else {
		b.WriteString(treeBody(vis, m.cursor, m.blinkPhase, m.filterQuery, treeHeight))
	}
	b.WriteString("\n")
	b.WriteString(m.hintLine())
	return b.String()
}

// titleLine renders row 1: ▍ ghostmux ▸ rail.
func titleLine() string {
	return " " + styTitleAccent.Render("▍") + styTitleName.Render("ghostmux") + styTitleTail.Render(" ▸ rail")
}

// hintLine renders the bottom row, which becomes a live prompt in filter,
// create, and kill-confirm modes, or an error flash after a failed action.
func (m railModel) hintLine() string {
	switch m.mode {
	case modeFilter:
		return " " + styActivity.Render("/") + rowStyle(hexSessionName, false, false).Render(m.filterQuery) + blockCursor(m.blinkPhase)
	case modeCreate:
		return " " + styHint.Render("new session: ") + rowStyle(hexSessionName, false, false).Render(m.createInput) + blockCursor(m.blinkPhase)
	case modeKillConfirm:
		return " " + styHint.Render("kill "+m.killTarget+"? ") + styBell.Render("y") + styHint.Render("/n")
	}
	if m.errorActive() {
		return " " + styError.Render(m.errMsg)
	}
	if m.viewportDead {
		return " " + styHint.Render("↵ re-point viewport")
	}
	return " " + styHint.Render("j/k move · ↵ view · ? help")
}

// blockCursor is the ▉ input-prompt cursor, blinking with the shared blink
// phase (hidden on phase 2, matching the bell cadence).
func blockCursor(phase int) string {
	if phase == 2 {
		return " "
	}
	return rowStyle(hexSessionName, false, false).Render("▉")
}

// emptyStateBody renders the mockup screen-4 rail body when the tree is
// empty (Task 7).
func emptyStateBody(height int) string {
	lines := []string{
		"",
		"  no sessions yet",
		"",
		"  " + newKeyHint("n", "new session"),
		"  " + newKeyHint("a", "agent session"),
		"",
		"  sessions made anywhere",
		"  (tmux new, ambient, ssh)",
		"  appear here live",
	}
	var b strings.Builder
	for i := 0; i < height; i++ {
		if i < len(lines) {
			b.WriteString(lines[i])
		}
		b.WriteString("\n")
	}
	return b.String()
}

func newKeyHint(key, desc string) string {
	return rowStyle(hexActivity, true, false).Render(key) + rowStyle(hexSessionName, false, false).Render("  "+desc)
}

const railMarksWidth = 3

// treeBody renders the scrollable session/window tree, rows 3..height-2.
func treeBody(vis []railRow, cursor, blinkPhase int, filterQuery string, height int) string {
	start, end, moreUp, moreDown := scrollWindow(len(vis), height, cursor)
	var b strings.Builder
	printed := 0
	if moreUp > 0 {
		b.WriteString(scrollIndicator("↑", moreUp) + "\n")
		printed++
	}
	for i := start; i < end; i++ {
		b.WriteString(renderRow(vis[i], i == cursor, blinkPhase, filterQuery) + "\n")
		printed++
	}
	if moreDown > 0 {
		b.WriteString(scrollIndicator("↓", moreDown) + "\n")
		printed++
	}
	for ; printed < height; printed++ {
		b.WriteString("\n")
	}
	return b.String()
}

func scrollIndicator(arrow string, n int) string {
	return "  " + styHint.Render(arrow+" "+itoa(n)+" more…")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

// renderRow renders one tree row within the fixed railWidth budget: an
// indent/arrow prefix, a truncated label, and up to two right-aligned gutter
// marks (never truncated). Filter-dimmed rows render entirely in #504945
// (marks too), in place — no reflow.
func renderRow(r railRow, cursor bool, blinkPhase int, filterQuery string) string {
	dim := filterQuery != "" && !matchesFilter(r, filterQuery)

	indent := "     " // window rows: 5-col indent
	arrow := ""
	if r.depth == 0 {
		arrow = "▾ "
		if r.collapsed {
			arrow = "▸ "
		}
		indent = " "
	}
	prefixWidth := len([]rune(indent)) + len([]rune(arrow))
	labelWidth := railWidth - prefixWidth - railMarksWidth
	if labelWidth < 1 {
		labelWidth = 1
	}

	suffix := ""
	if r.depth == 0 && r.attached {
		suffix = " ●"
	}
	nameWidth := labelWidth - len([]rune(suffix))
	if nameWidth < 1 {
		nameWidth = 1
	}
	name := truncateLabel(r.label, nameWidth)
	label := name + suffix
	label = label + strings.Repeat(" ", max0(labelWidth-len([]rune(label))))

	var arrowStyled string
	if arrow != "" {
		arrowStyled = rowStyle(hexTitleTail, false, cursor).Render(arrow)
		if dim {
			arrowStyled = rowStyle(hexCursorBg, false, cursor).Render(arrow)
		}
	}

	nameFg, nameBold := nameStyle(r)
	if dim {
		nameFg, nameBold = hexCursorBg, false
	}
	nameStyled := rowStyle(nameFg, nameBold, cursor).Render(name)

	suffixStyled := ""
	if suffix != "" {
		suffixFg := hexAttached
		if dim {
			suffixFg = hexCursorBg
		}
		suffixStyled = rowStyle(suffixFg, false, cursor).Render(suffix)
	}
	pad := strings.Repeat(" ", max0(labelWidth-len([]rune(label))))
	padStyled := rowStyle(hexSessionName, false, cursor).Render(pad)

	marks := padMarksLeft(r.gutter(), railMarksWidth)
	marksStyled := renderMarks(marks, dim, blinkPhase, cursor)

	indentStyled := rowStyle(hexTitleTail, false, cursor).Render(indent)

	return indentStyled + arrowStyled + nameStyled + suffixStyled + padStyled + marksStyled
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// nameStyle picks the session/window name's fg color and weight per
// SPEC.md §4 (in-view sessions render #8ec07c bold; active windows #b8bb26;
// everything else its default).
func nameStyle(r railRow) (fg string, bold bool) {
	switch {
	case r.depth == 0 && r.inView:
		return hexTitleName, true
	case r.depth == 0:
		return hexSessionName, true
	case r.active:
		return hexAttached, false
	default:
		return hexInactiveWin, false
	}
}

func padMarksLeft(s string, width int) string {
	n := width - len([]rune(s))
	if n <= 0 {
		return s
	}
	return strings.Repeat(" ", n) + s
}

// renderMarks colors each gutter glyph individually (bell blinks and hides on
// blinkPhase==2; dim overrides every glyph to #504945 under an active,
// non-matching filter).
func renderMarks(padded string, dim bool, blinkPhase int, cursor bool) string {
	var b strings.Builder
	for _, ch := range padded {
		switch ch {
		case ' ':
			b.WriteString(rowStyle(hexSessionName, false, cursor).Render(" "))
		case '●':
			if dim {
				b.WriteString(rowStyle(hexCursorBg, false, cursor).Render("●"))
			} else if blinkPhase == 2 {
				b.WriteString(rowStyle(hexSessionName, false, cursor).Render(" "))
			} else {
				b.WriteString(rowStyle(hexBell, true, cursor).Render("●"))
			}
		case '✓':
			fg := hexAttached
			if dim {
				fg = hexCursorBg
			}
			b.WriteString(rowStyle(fg, false, cursor).Render("✓"))
		case '~':
			fg := hexActivity
			if dim {
				fg = hexCursorBg
			}
			b.WriteString(rowStyle(fg, false, cursor).Render("~"))
		case '▸':
			fg := hexTitleAccent
			if dim {
				fg = hexCursorBg
			}
			b.WriteString(rowStyle(fg, false, cursor).Render("▸"))
		default:
			b.WriteString(string(ch))
		}
	}
	return b.String()
}
