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
	hexTitleTail    = "#928374" // hints / dim / collapse arrows
	hexSessionName  = "#ebdbb2" // session name, not in view
	hexAttached     = "#b8bb26" // attached ● / active window / done ✓
	hexBell         = "#fb4934" // bell ● / errors / kill confirm y
	hexActivity     = "#fabd2f" // activity ~ / filter prompt /
	hexCursorBg     = "#504945" // cursor bar background / dimmed fg
	hexInactiveWin  = "#928374"
	hexFilterCursor = "#ebdbb2"
	hexAgent        = "#d3869b" // detected agent command (design palette "agent accent")
)

var (
	styTitleAccent = lipgloss.NewStyle().Foreground(lipgloss.Color(hexTitleAccent))
	styTitleName   = lipgloss.NewStyle().Foreground(lipgloss.Color(hexTitleName)).Bold(true)
	styTitleTail   = lipgloss.NewStyle().Foreground(lipgloss.Color(hexTitleTail))
	styHint        = lipgloss.NewStyle().Foreground(lipgloss.Color(hexTitleTail))
	styBell        = lipgloss.NewStyle().Foreground(lipgloss.Color(hexBell)).Bold(true)
	styError       = lipgloss.NewStyle().Foreground(lipgloss.Color(hexBell))
	styActivity    = lipgloss.NewStyle().Foreground(lipgloss.Color(hexActivity))
)

// treeTop is the screen line the tree's first row is drawn on. There is no
// title row — the rail is 30 columns of scarce vertical space and a banner
// naming the program you just launched earns none of it — so the tree starts
// at the very top.
const treeTop = 0

// treeHeight is how many lines the tree gets: everything except the blank
// separator and the hint line beneath it.
//
// View() and rowAt() MUST agree on this and on treeTop. They are the same
// layout expressed twice — once to draw and once to hit-test — and when they
// drifted apart, every click landed two rows off.
func (m railModel) treeHeight() int {
	h := m.height
	if h <= 0 {
		h = 24
	}
	if th := h - 2; th >= 1 {
		return th
	}
	return 1
}

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
	treeHeight := m.treeHeight()

	if m.helpView {
		return helpPage(height)
	}

	var b strings.Builder

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

// AttentionSummary is the fleet's unread state: how many sessions carry a bell
// and how many finished a command unseen. The hosting frame renders it in its
// own chrome (solo's bottom bar); the classic rail falls back to its hint line.
func (m Model) AttentionSummary() (bells, done int) { return m.attention() }

func (m railModel) attention() (int, int) {
	var nBell, nDone int
	for _, r := range m.rows {
		if r.depth == 0 { // count once per session, aggregates included
			if r.bell {
				nBell++
			}
			if r.done {
				nDone++
			}
		}
	}
	return nBell, nDone
}

// attentionText renders the summary as styled text plus its display width, or
// ("", 0) when the fleet is quiet — nothing to report means nothing drawn.
func (m railModel) attentionText() (string, int) {
	nBell, nDone := m.attention()
	out, w := "", 0
	if nBell > 0 {
		s := "●" + itoa(nBell)
		out += styBell.Render(s)
		w += len([]rune(s))
	}
	if nDone > 0 {
		s := "✓" + itoa(nDone)
		if out != "" {
			out += " "
			w++
		}
		out += rowStyle(hexAttached, false, false).Render(s)
		w += len([]rune(s))
	}
	return out, w
}

// hintLine renders the bottom row, which becomes a live prompt in filter,
// create, and kill-confirm modes, or an error flash after a failed action.
func (m railModel) hintLine() string {
	switch m.mode {
	case modeFilter:
		return " " + styActivity.Render("/") + m.input.View()
	case modeCreate:
		// Always name the backend being created on: `n` and `z` are different
		// keys, so the prompt must say which one you pressed.
		return " " + styHint.Render("new "+backendLabel(m.createBackend)+": ") + m.input.View()
	case modeGroup:
		return " " + styHint.Render("group: ") + m.input.View()
	case modeKillConfirm:
		// Deleting a group is not killing anything: say so, or the confirm
		// prompt reads like it is about to destroy sessions.
		verb := "kill "
		if m.killGroup {
			verb = "ungroup "
		}
		return " " + styHint.Render(verb+m.killTarget+"? ") + styBell.Render("y") + styHint.Render("/n")
	}
	if m.errorActive() {
		return " " + styError.Render(m.errMsg)
	}
	if m.viewportDead {
		return " " + styHint.Render("↵ re-point viewport")
	}
	// The frame draws the keymap and the attention summary in its bottom bar;
	// duplicating them here would spend the rail's last row on nothing.
	return ""
}

// emptyStateBody renders the mockup screen-4 rail body when the tree is
// empty (Task 7).
func emptyStateBody(height int) string {
	lines := []string{
		"",
		"  no sessions yet",
		"",
		"  " + newKeyHint("n", "new session"),
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

	// Three levels in 30 columns: a group folder, its sessions, their windows.
	// Indents stay tight (1/3/5) because every column spent here is a column
	// taken from the name, which is the part you actually read.
	indent, arrow := " ", ""
	switch {
	case r.isGroup:
		arrow = "▾ "
		if r.collapsed {
			arrow = "▸ "
		}
	case r.isWin:
		indent = "     " // ungrouped windows
		if r.group != "" {
			indent = "       "
		}
	default: // session row
		if r.group != "" {
			indent = "   "
		}
		if r.flat {
			arrow = "  " // no disclosure; keep names column-aligned with ▾ rows
		} else {
			arrow = "▾ "
			if r.collapsed {
				arrow = "▸ "
			}
		}
	}
	prefixWidth := len([]rune(indent)) + len([]rune(arrow))
	labelWidth := railWidth - prefixWidth - railMarksWidth
	if labelWidth < 1 {
		labelWidth = 1
	}

	suffix := ""
	// A folded group must still report what it holds, or folding would hide
	// exactly what the rail exists to surface.
	if r.isGroup && r.collapsed && r.count > 0 {
		suffix = " " + itoa(r.count)
	}
	if !r.isGroup && !r.isWin && r.attached {
		suffix = " ●"
	}
	nameWidth := labelWidth - len([]rune(suffix))
	if nameWidth < 1 {
		nameWidth = 1
	}
	name := truncateLabel(r.label, nameWidth)

	// Flat rows show the window's foreground command dim after the name,
	// truncated into whatever space the name and suffix leave over. No
	// liveness animation: pane_current_command can't distinguish a working
	// process from an idle one, and implying activity would mislead.
	cmdStr := ""
	if r.flat && r.cmd != "" {
		rest := labelWidth - len([]rune(name)) - len([]rune(suffix))
		if rest >= 5 { // " · " + at least 2 chars of command
			cmdStr = truncateLabel(r.cmd, rest-3)
		}
	}

	// Everything left of the marks pads to labelWidth so the marks land
	// flush right at the rail edge, every row, every time. Widths are
	// measured on the RAW strings — styling happens after.
	cmdRaw := ""
	if cmdStr != "" {
		cmdRaw = " · " + cmdStr
	}
	used := len([]rune(name)) + len([]rune(suffix)) + len([]rune(cmdRaw))
	pad := strings.Repeat(" ", max0(labelWidth-used))

	dimFg := func(fg string) string {
		if dim {
			return hexCursorBg
		}
		return fg
	}

	var arrowStyled string
	if arrow != "" {
		arrowStyled = rowStyle(dimFg(hexTitleTail), false, cursor).Render(arrow)
	}
	nameFg, nameBold := nameStyle(r)
	if dim {
		nameFg, nameBold = hexCursorBg, false
	}
	nameStyled := rowStyle(nameFg, nameBold, cursor).Render(name)
	suffixStyled := ""
	if suffix != "" {
		suffixStyled = rowStyle(dimFg(hexAttached), false, cursor).Render(suffix)
	}
	cmdStyled := ""
	if cmdStr != "" {
		// Ambient agent detection: a recognized agent command renders in the
		// agent accent — observed from the slot, not declared by a name.
		cmdFg := hexInactiveWin
		if isAgentCmd(r.cmd) {
			cmdFg = hexAgent
		}
		cmdStyled = rowStyle(dimFg(hexInactiveWin), false, cursor).Render(" · ") +
			rowStyle(dimFg(cmdFg), false, cursor).Render(cmdStr)
	}
	padStyled := rowStyle(hexSessionName, false, cursor).Render(pad)

	marks := padMarksLeft(r.gutter(), railMarksWidth)
	marksStyled := renderMarks(marks, dim, blinkPhase, cursor)

	indentStyled := rowStyle(hexTitleTail, false, cursor).Render(indent)

	return indentStyled + arrowStyled + nameStyled + suffixStyled + cmdStyled + padStyled + marksStyled
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
