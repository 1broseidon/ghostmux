package app

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The bottom bar is ghostmux's own chrome, deliberately not a status line.
// A tmux status bar is an identity strip: session name, window list,
// hostname, date, clock — decoration you stop reading on day two. This bar
// spends its width on the two things that change and matter: what you can
// press right now, and what in the fleet wants you.
//
// Visually it refuses the powerline dialect (filled strip + colored pill) that
// makes nested mux/agent chrome blend into it. It is a floating engraved
// caption: a dotted rule lifted off the edges, then spine+wordmark, keymap,
// leader, and attention — no clock. Side inset keeps it from fighting the
// full-bleed statusline of whatever runs in the viewport. The one piece of
// "where am I" that survives is the hostname, and only because attach-anywhere
// means you genuinely may not know which box you are on.
const (
	hexBarGround = "#1d2021" // panel ground — engraved, not a strip
	hexBarFg     = "#928374"
	hexBarKey    = "#fabd2f" // keys pop; their labels do not
	hexBarHost   = "#665c54"
	hexBarLeader = "#3c3836" // dotted rule + leader: texture, not a border
	hexMarkRail  = "#d79921" // spine+wordmark when the rail owns the keys
	hexMarkVp    = "#8ec07c" // spine+wordmark when the viewport owns the keys
	hexBarBell   = "#fb4934"
	hexBarDone   = "#b8bb26"

	// statusInset is the left/right float margin in cells. statusChromeRows is
	// the rule row plus the caption; cramped frames drop the rule.
	statusInset      = 1
	statusChromeRows = 2
)

var (
	styBar        = lipgloss.NewStyle().Background(lipgloss.Color(hexBarGround)).Foreground(lipgloss.Color(hexBarFg))
	styBarKey     = lipgloss.NewStyle().Background(lipgloss.Color(hexBarGround)).Foreground(lipgloss.Color(hexBarKey))
	styBarHost    = lipgloss.NewStyle().Background(lipgloss.Color(hexBarGround)).Foreground(lipgloss.Color(hexBarHost))
	styBarLeader  = lipgloss.NewStyle().Background(lipgloss.Color(hexBarGround)).Foreground(lipgloss.Color(hexBarLeader))
	styWordmark   = lipgloss.NewStyle().Background(lipgloss.Color(hexBarGround)).Foreground(lipgloss.Color(hexMarkRail)).Bold(true)
	styWordmarkVp = lipgloss.NewStyle().Background(lipgloss.Color(hexBarGround)).Foreground(lipgloss.Color(hexMarkVp)).Bold(true)
	styBarBell    = lipgloss.NewStyle().Background(lipgloss.Color(hexBarGround)).Foreground(lipgloss.Color(hexBarBell)).Bold(true)
	styBarDone    = lipgloss.NewStyle().Background(lipgloss.Color(hexBarGround)).Foreground(lipgloss.Color(hexBarDone))
	hostnameStr   = func() string {
		h, err := os.Hostname()
		if err != nil || h == "" {
			return ""
		}
		return h
	}()
)

// barKey is one "key label" pair.
type barKey struct{ key, label string }

// railKeys is the keymap while the rail has focus — the handful you reach for
// every minute. Organize, undo, kill, and create variants live behind `?`.
func railKeys() []barKey {
	return []barKey{
		{"j/k", "move"},
		{"↵", "view"},
		{"h/l", "fold"},
		{"`", "prev"},
		{"n", "new"},
		{"/", "filter"},
		{"?", "keys"},
	}
}

// settingsKeys is the keymap while settings is the frame. The toggle is not
// offered because it does nothing here, and a bar that advertised it would be
// the same lie as a help page naming a dead key.
func settingsKeys() []barKey {
	return []barKey{{"j/k", "section"}, {"↵", "edit"}, {"esc", "back"}}
}

// viewportKeys is the keymap while the viewport has focus: the one key that
// works, then the frame's own answer to "what am I looking at". The inner
// session's status line can lie about identity (tmux's default
// status-left-length truncates "[gm-agent-00]" into "[gm-agent-" and the
// window list glues onto it) — the ▸ label is the lock the frame actually
// holds, so it cannot. It sheds first when width runs out; the toggle is the
// lifeline and sheds last.
func viewportKeys(toggle, viewing string) []barKey {
	keys := []barKey{{toggle, "back to rail"}}
	if viewing != "" {
		keys = append(keys, barKey{"▸", viewing})
	}
	return keys
}

// viewingLabel is the frame's own answer to "what is in the viewport": the
// exact session (and window) the viewport lock holds. Empty when idle.
func (m soloModel) viewingLabel() string {
	lock := m.vp.Lock()
	if lock.Sess == "" {
		return ""
	}
	if lock.Win != "" {
		return lock.Sess + ":" + lock.Win
	}
	return lock.Sess
}

// statusRows is how many frame rows the footer chrome consumes.
func (m soloModel) statusRows() int {
	if m.h > 0 && m.h < 4 {
		return 1 // too short for a floating rule; keep the caption
	}
	return statusChromeRows
}

// statusLine renders the footer chrome: optional floating dotted rule, then the
// caption. Every line is exactly width columns.
func (m soloModel) statusLine(width int) string {
	if width <= 0 {
		return ""
	}
	inset := statusInset
	if width < inset*2+8 {
		inset = 0 // narrow: full bleed rather than a crushed float
	}
	inner := width - inset*2
	caption := m.captionLine(inner)
	if m.statusRows() == 1 {
		return floatPad(caption, width, inset)
	}
	return floatPad(dottedRule(inner), width, inset) + "\n" + floatPad(caption, width, inset)
}

// captionLine is the engraved identity + keys + attention row at exactly
// width columns (the caller's inner width after inset).
func (m soloModel) captionLine(width int) string {
	if width <= 0 {
		return ""
	}
	mark, keys := styWordmark, railKeys()
	switch {
	case m.settings != nil:
		keys = settingsKeys()
	case m.focus == focusViewport:
		mark, keys = styWordmarkVp, viewportKeys(m.toggleLabel, m.viewingLabel())
	}
	left := mark.Render("▎gmx")
	lw := lipgloss.Width(left)

	bells, done := m.rail.AttentionSummary()
	right, rw := attentionCluster(bells, done)

	// Keys fill the middle, dropped from the right as the terminal narrows —
	// a truncated key hint is worse than an absent one. Remaining gap is plain
	// ground: the floating rule above already carries the dotted texture, so a
	// second ┈ run between keys and attention would just be noise.
	for n := len(keys); n >= 0; n-- {
		mid, mw := renderKeys(keys[:n])
		gap := width - lw - mw - rw
		if gap >= 0 {
			return left + mid + styBar.Render(strings.Repeat(" ", gap)) + right
		}
	}
	return truncate(left, width)
}

// dottedRule is the floating separator above the caption — one ┈ band so the
// footer reads as lifted off the frame, not another status strip.
func dottedRule(width int) string {
	if width <= 0 {
		return ""
	}
	return styBarLeader.Render(strings.Repeat("┈", width))
}

// floatPad places content in an inset band, filling the outer columns with
// panel ground so the caption floats clear of the frame edges.
func floatPad(content string, width, inset int) string {
	if width <= 0 {
		return ""
	}
	if inset <= 0 {
		if ansiWidth := lipgloss.Width(content); ansiWidth < width {
			return content + styBar.Render(strings.Repeat(" ", width-ansiWidth))
		}
		return truncate(content, width)
	}
	inner := width - inset*2
	body := content
	if lipgloss.Width(body) != inner {
		body = truncate(body, inner)
		if pad := inner - lipgloss.Width(body); pad > 0 {
			body += styBar.Render(strings.Repeat(" ", pad))
		}
	}
	edge := styBar.Render(strings.Repeat(" ", inset))
	return edge + body + edge
}

// attentionCluster is the right instrument: ●/✓ census and @host. Counts are
// passive telemetry — j/k to the gutter mark; no dedicated cycle chord.
func attentionCluster(bells, done int) (string, int) {
	var b strings.Builder
	w := 0
	if bells > 0 {
		frag := " ●" + itoa(bells)
		b.WriteString(styBarBell.Render(frag))
		w += len([]rune(frag))
	}
	if done > 0 {
		frag := " ✓" + itoa(done)
		b.WriteString(styBarDone.Render(frag))
		w += len([]rune(frag))
	}
	if hostnameStr != "" {
		frag := "  @" + hostnameStr + " "
		b.WriteString(styBarHost.Render(frag))
		w += len([]rune(frag))
	} else if w > 0 {
		b.WriteString(styBar.Render(" "))
		w++
	}
	return b.String(), w
}

// renderKeys lays out key/label pairs and reports their display width.
func renderKeys(keys []barKey) (string, int) {
	var b strings.Builder
	w := 0
	for _, k := range keys {
		b.WriteString(styBarKey.Render("  " + k.key))
		b.WriteString(styBar.Render(" " + k.label))
		w += 2 + len([]rune(k.key)) + 1 + len([]rune(k.label))
	}
	return b.String(), w
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}
