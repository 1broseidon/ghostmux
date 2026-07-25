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
	styHint     = lipgloss.NewStyle().Foreground(lipgloss.Color(hexTitleTail))
	styBell     = lipgloss.NewStyle().Foreground(lipgloss.Color(hexBell)).Bold(true)
	styError    = lipgloss.NewStyle().Foreground(lipgloss.Color(hexBell))
	styActivity = lipgloss.NewStyle().Foreground(lipgloss.Color(hexActivity))
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
		// One key, four destructions: say which one. Ungrouping is not killing,
		// forgetting a declaration is not killing, and deleting a serialized
		// session is not killing either — a prompt that said "kill" for all of
		// them would be asking consent for something that isn't happening.
		return " " + styHint.Render(m.killKind.verb()+" "+m.killTarget+"? ") +
			styBell.Render("y") + styHint.Render("/n")
	}
	if m.errorActive() {
		return " " + styError.Render(m.errMsg)
	}
	if m.viewportDead {
		return " " + styHint.Render("↵ re-point viewport")
	}
	// A ghost is the one row whose keys mean something different, and it is
	// also the row where guessing is expensive — ↵ is about to CREATE
	// something. Spend the last line saying where, and what x would take away.
	if vis := m.visible(); m.cursor >= 0 && m.cursor < len(vis) && vis[m.cursor].ghost {
		return " " + styHint.Render(truncateLabel(ghostHint(vis[m.cursor]), railWidth-1))
	}
	// The frame draws the keymap and the attention summary in its bottom bar;
	// duplicating them here would spend the rail's last row on nothing.
	return ""
}

// ghostHint is the ghost row's hint text: what ↵ and x will really do to THIS
// ghost. The two backends get different words because they do different things
// — tmux starts a new session, zellij resurrects its own serialized one.
func ghostHint(r railRow) string {
	if r.backend != "" {
		return "↵ resurrect · x delete"
	}
	const head, tail = "↵ start in ", " · x forget"
	// The dir is what gets shortened, never the verbs: a hint that truncates
	// "forget" into "for" is worse than one that doesn't say where.
	room := railWidth - 1 - len([]rune(head)) - len([]rune(tail))
	if r.dir == "" || room < 3 {
		return "↵ start · x forget"
	}
	return head + truncateLeft(r.dir, room) + tail
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
	// A ghost is dim for the same reason a filtered-out row is: it is present
	// but not what is happening. It reuses the filter's dim rather than
	// inventing a colour, because the rail's palette is fixed and a fourth
	// shade of grey would say nothing the position and the ○ don't already.
	dim := r.ghost || (filterQuery != "" && !matchesFilter(r, filterQuery))
	dimHex := ghostDimHex(r.ghost, cursor)

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
	// exactly what the rail exists to surface. Live and dead are counted
	// apart — "2 ○1" is a different fleet from "3", and a folder that rounds
	// them together is lying about what pressing S would do.
	if r.isGroup && r.collapsed {
		if r.count > 0 {
			suffix = " " + itoa(r.count)
		}
		if r.ghostCount > 0 {
			suffix += " ○" + itoa(r.ghostCount)
		}
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

	// A declaration ghost spends the same slot on the dir it would be summoned
	// into. Seeing where the session will be created BEFORE pressing ↵ is what
	// separates a promise from a surprise; the tail is kept because the head is
	// the same ~/Projects on every row.
	dirStr := ""
	if r.ghost && cmdStr == "" && r.dir != "" {
		rest := labelWidth - len([]rune(name)) - len([]rune(suffix))
		if rest >= 3 { // " " + at least 2 chars of path tail
			dirStr = truncateLeft(r.dir, rest-1)
		}
	}

	// Everything left of the marks pads to labelWidth so the marks land
	// flush right at the rail edge, every row, every time. Widths are
	// measured on the RAW strings — styling happens after.
	cmdRaw := ""
	if cmdStr != "" {
		cmdRaw = " · " + cmdStr
	}
	dirRaw := ""
	if dirStr != "" {
		dirRaw = " " + dirStr
	}
	used := len([]rune(name)) + len([]rune(suffix)) + len([]rune(cmdRaw)) + len([]rune(dirRaw))
	pad := strings.Repeat(" ", max0(labelWidth-used))

	dimFg := func(fg string) string {
		if dim {
			return dimHex
		}
		return fg
	}

	var arrowStyled string
	if arrow != "" {
		arrowStyled = rowStyle(dimFg(hexTitleTail), false, cursor).Render(arrow)
	}
	nameFg, nameBold := nameStyle(r)
	if dim {
		nameFg, nameBold = dimHex, false
	}
	// A group whose every member is a ghost is a declaration, not a fleet: dim
	// the folder itself so the eye skips it exactly as it skips its rows.
	if r.isGroup && r.count == 0 && r.ghostCount > 0 {
		nameFg, nameBold = ghostDimHex(true, cursor), false
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
	dirStyled := ""
	if dirStr != "" {
		dirStyled = rowStyle(dimHex, false, cursor).Render(dirRaw)
	}
	padStyled := rowStyle(hexSessionName, false, cursor).Render(pad)

	marks := padMarksLeft(r.gutter(), railMarksWidth)
	dimMark := ""
	if dim {
		dimMark = dimHex
	}
	marksStyled := renderMarks(marks, dimMark, blinkPhase, cursor)

	indentStyled := rowStyle(hexTitleTail, false, cursor).Render(indent)

	return indentStyled + arrowStyled + nameStyled + suffixStyled + cmdStyled + dirStyled + padStyled + marksStyled
}

// ghostDimHex is the dim a ghost renders in: the filter's #504945 — except on
// the cursor bar, whose BACKGROUND is #504945, where the row would vanish
// entirely. Filter-dimmed rows never hit this (the cursor skips them); a ghost
// is a row you select on purpose, so it has to survive being selected.
func ghostDimHex(ghost, cursor bool) string {
	if ghost && cursor {
		return hexTitleTail
	}
	return hexCursorBg
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
// blinkPhase==2). dimHex is "" for a normal row, or the colour every glyph is
// overridden to on a dimmed one — a filtered-out row or a ghost.
func renderMarks(padded string, dimHex string, blinkPhase int, cursor bool) string {
	dim := dimHex != ""
	var b strings.Builder
	for _, ch := range padded {
		switch ch {
		case ' ':
			b.WriteString(rowStyle(hexSessionName, false, cursor).Render(" "))
		case '○':
			// The ghost glyph is never anything but dim: it reports an absence,
			// and an absence must not compete with a live session's marks.
			fg := dimHex
			if fg == "" {
				fg = hexCursorBg
			}
			b.WriteString(rowStyle(fg, false, cursor).Render("○"))
		case '●':
			if dim {
				b.WriteString(rowStyle(dimHex, false, cursor).Render("●"))
			} else if blinkPhase == 2 {
				b.WriteString(rowStyle(hexSessionName, false, cursor).Render(" "))
			} else {
				b.WriteString(rowStyle(hexBell, true, cursor).Render("●"))
			}
		case '✓':
			fg := hexAttached
			if dim {
				fg = dimHex
			}
			b.WriteString(rowStyle(fg, false, cursor).Render("✓"))
		case '~':
			fg := hexActivity
			if dim {
				fg = dimHex
			}
			b.WriteString(rowStyle(fg, false, cursor).Render("~"))
		case '▸':
			fg := hexTitleAccent
			if dim {
				fg = dimHex
			}
			b.WriteString(rowStyle(fg, false, cursor).Render("▸"))
		default:
			b.WriteString(string(ch))
		}
	}
	return b.String()
}
