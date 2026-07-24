package rail

import (
	"fmt"
	"strings"
)

// keyHelp is one line of the keymap: a key (as shown to the user) and its
// description. keyHelpRows is the single source of truth for the rail's
// keymap — the `?` page and the frame's bottom bar both derive from it
// (enforced by TestKeyHelpCoversBoundKeys).
type keyHelp struct{ key, desc string }

// The toggle is the only key whose name the rail does not choose: the frame
// intercepts it in process (GHOSTMUX_TOGGLE), and a desktop environment may
// have grabbed the default out from under it. The frame reports what it
// actually reserved so `?` can never lie about the keymap — a help page that
// names a dead key is worse than no help page.
var (
	toggleLabel = `ctrl+\` // shown in the keymap table (first key)
	toggleAll   = `ctrl+\` // shown in the footer (every accepted key)
)

// SetToggleKeys records the key(s) the hosting frame reserved for the
// rail ⇄ viewport toggle. Called once at startup, before any help render.
func SetToggleKeys(keys ...string) {
	if len(keys) == 0 || keys[0] == "" {
		return
	}
	toggleLabel, toggleAll = keys[0], strings.Join(keys, " or ")
}

// keyHelpRows is the single source of truth for the rail's keymap — both the
// `?` page and `rail help` render it (enforced by TestKeyHelpCoversBoundKeys).
func keyHelpRows() []keyHelp {
	return []keyHelp{
		{"j/k, ↓/↑", "move cursor"},
		{"g/G", "first / last row"},
		{"↵", "view in pane →"},
		{"l/→", "focus viewport"},
		{toggleLabel, "toggle rail ⇄ viewport"},
		{"tab", "fold group / session"},
		{"a", "new group"},
		{"J/K", "move into / within group"},
		{"n", "new tmux session"},
		{"z", "new zellij session"},
		{"x", "kill session / ungroup"},
		{"/", "filter rows"},
		{"r", "refresh now (auto: 1s)"},
		{"d", "detach inner client"},
		{"?", "help"},
		{"q", "quit rail"},
	}
}

// helpPage renders the in-pane help view that `?` toggles — sized for the
// 30-col rail, keys from keyHelpRows (the single source of truth),
// descriptions truncated to fit rather than wrap.
func helpPage(_ int) string {
	const keyCol = 9
	descWidth := railWidth - keyCol - 4
	var b strings.Builder
	b.WriteString(" " + styTitleAccent.Render("▍") + styTitleName.Render("ghostmux") + styTitleTail.Render(" ▸ keys") + "\n\n")
	for _, k := range keyHelpRows() {
		key := truncateLabel(k.key, keyCol)
		b.WriteString(" " + styActivity.Render(fmt.Sprintf("%*s", keyCol, key)) +
			"  " + styHint.Render(truncateLabel(k.desc, descWidth)) + "\n")
	}
	b.WriteString("\n " + styHint.Render("gutter:") + " " +
		styBell.Render("●") + styHint.Render("bell ") +
		rowStyle(hexAttached, false, false).Render("✓") + styHint.Render("done") + "\n")
	b.WriteString("         " + styActivity.Render("~") + styHint.Render("act  ") +
		styTitleAccent.Render("▸") + styHint.Render("viewing") + "\n")
	if toggleAll != toggleLabel {
		b.WriteString(" " + styHint.Render(truncateLabel("toggle: "+toggleAll, railWidth-2)) + "\n")
	}
	b.WriteString(" " + styHint.Render("ctrl+b → inner tmux") + "\n")
	b.WriteString("\n " + styHint.Render("? / esc close"))
	return b.String()
}
