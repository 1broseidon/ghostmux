package app

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/1broseidon/ghostmux/internal/rail"
)

// The bottom bar is ghostmux's own chrome, deliberately not a status line.
// A tmux/zellij status bar is an identity strip: session name, window list,
// hostname, date, clock — decoration you stop reading on day two. This bar
// spends its width on the two things that change and matter: what you can
// press right now, and what in the fleet wants you.
//
// So: an identity block, a live keymap that follows focus and mode, and the
// attention summary right-aligned. No clock. The one piece of "where am I"
// that survives is the hostname, and only because attach-anywhere means you
// genuinely may not know which box you are on.
const (
	hexBarBg    = "#282828" // one step darker than the rail: the bar recedes
	hexBarFg    = "#928374"
	hexBarKey   = "#fabd2f" // keys pop; their labels do not
	hexBarHost  = "#665c54"
	hexBlockFg  = "#1d2021"
	hexBlockBg  = "#d79921" // gmx block, gold
	hexBarBell  = "#fb4934"
	hexBarDone  = "#b8bb26"
	hexBarFocus = "#8ec07c" // viewport-focused: the bar says whose keys these are
)

var (
	styBar      = lipgloss.NewStyle().Background(lipgloss.Color(hexBarBg)).Foreground(lipgloss.Color(hexBarFg))
	styBarKey   = lipgloss.NewStyle().Background(lipgloss.Color(hexBarBg)).Foreground(lipgloss.Color(hexBarKey))
	styBarHost  = lipgloss.NewStyle().Background(lipgloss.Color(hexBarBg)).Foreground(lipgloss.Color(hexBarHost))
	styBlock    = lipgloss.NewStyle().Background(lipgloss.Color(hexBlockBg)).Foreground(lipgloss.Color(hexBlockFg)).Bold(true)
	styBlockVp  = lipgloss.NewStyle().Background(lipgloss.Color(hexBarFocus)).Foreground(lipgloss.Color(hexBlockFg)).Bold(true)
	styBarBell  = lipgloss.NewStyle().Background(lipgloss.Color(hexBarBg)).Foreground(lipgloss.Color(hexBarBell)).Bold(true)
	styBarDone  = lipgloss.NewStyle().Background(lipgloss.Color(hexBarBg)).Foreground(lipgloss.Color(hexBarDone))
	hostnameStr = func() string {
		h, err := os.Hostname()
		if err != nil || h == "" {
			return ""
		}
		return h
	}()
)

// barKey is one "key label" pair.
type barKey struct{ key, label string }

// railKeys is the keymap while the rail has focus — the eight you actually
// reach for. The full list stays behind `?`.
func railKeys(toggle string) []barKey {
	keys := []barKey{
		{"j/k", "move"},
		{"↵", "view"},
		{"⇥", "fold"},
		{"n", "new"},
	}
	if zellijInstalled() {
		keys = append(keys, barKey{"z", "zellij"})
	}
	return append(keys,
		barKey{"x", "kill"},
		barKey{"/", "filter"},
		barKey{toggle, "session"},
		barKey{"?", "keys"},
	)
}

// settingsKeys is the keymap while settings is the frame. The toggle is not
// offered because it does nothing here, and a bar that advertised it would be
// the same lie as a help page naming a dead key.
func settingsKeys() []barKey {
	return []barKey{{"j/k", "section"}, {"↵", "edit"}, {"esc", "back"}}
}

// viewportKeys is the keymap while the viewport has focus. There is exactly
// one: everything else belongs to the program you are looking at, and saying
// so is the honest thing for the bar to do.
func viewportKeys(toggle string) []barKey {
	return []barKey{{toggle, "back to rail"}}
}

// statusLine renders the bottom bar at exactly width columns.
func (m soloModel) statusLine(width int) string {
	if width <= 0 {
		return ""
	}
	block, keys := styBlock, railKeys(m.toggleLabel)
	switch {
	case m.settings != nil:
		keys = settingsKeys()
	case m.focus == focusViewport:
		block, keys = styBlockVp, viewportKeys(m.toggleLabel)
	}
	left := block.Render(" gmx ")
	lw := lipgloss.Width(left)

	// Attention first: it is the only thing here that is news.
	right, rw := "", 0
	if bells, done := m.rail.AttentionSummary(); bells > 0 || done > 0 {
		if bells > 0 {
			right += styBarBell.Render(" ●" + itoa(bells))
			rw += 2 + len(itoa(bells))
		}
		if done > 0 {
			right += styBarDone.Render(" ✓" + itoa(done))
			rw += 2 + len(itoa(done))
		}
	}
	if hostnameStr != "" {
		right += styBarHost.Render("  " + hostnameStr + " ")
		rw += 3 + len(hostnameStr)
	}

	// Keys fill the middle, dropped from the right as the terminal narrows —
	// a truncated key hint is worse than an absent one.
	for n := len(keys); n >= 0; n-- {
		mid, mw := renderKeys(keys[:n])
		if lw+mw+rw <= width {
			return left + mid + styBar.Render(strings.Repeat(" ", width-lw-mw-rw)) + right
		}
	}
	return truncate(left, width)
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

// zellijInstalled mirrors the rail's own detection so the bar never offers a
// key that would only produce an error.
func zellijInstalled() bool { return rail.HasBackend("zellij") }

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
