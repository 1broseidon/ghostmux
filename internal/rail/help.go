package rail

import (
	"fmt"
	"os"
	"strings"
)

// keyHelp is one line of the keymap: a key (as shown to the user) and its
// description. keyHelpTable is the single source of truth for the rail's
// keymap — both `?` help's `rail help` printer and the doc comments on
// Update()'s dispatch derive from it (Task 10; enforced by
// TestKeyHelpCoversBoundKeys in rail_test.go).
type keyHelp struct{ key, desc string }

var keyHelpTable = []keyHelp{
	{"j/k, ↓/↑", "move cursor"},
	{"g/G", "first / last row"},
	{"↵", "view in pane →"},
	{"l/→", "focus viewport"},
	{"ctrl+\\", "toggle rail ⇄ viewport"},
	{"tab", "collapse / expand session"},
	{"n", "new session"},
	{"x", "kill selected (y/n confirm)"},
	{"/", "filter rows"},
	{"r", "refresh now (auto: 1s)"},
	{"d", "detach inner client"},
	{"?", "help"},
	{"q", "quit rail"},
}

// cmdHelp is `ghostmux rail help`: prints the screen-6 keymap (DESIGN.md §
// SCREEN 6) in truecolor, then blocks on a single stdin byte — the popup
// closes on any keypress, `display-popup` tears down the pane on exit.
// `tmux display-popup` draws the border; this does not draw a second box.
func cmdHelp() error {
	const (
		key   = "\x1b[38;2;250;189;47;1m" // #fabd2f bold
		desc  = "\x1b[0m"
		gray  = "\x1b[38;2;146;131;116m" // #928374
		bell  = "\x1b[38;2;251;73;52m"
		done  = "\x1b[38;2;184;187;38m"
		act   = "\x1b[38;2;250;189;47m"
		view  = "\x1b[38;2;254;128;25m"
		title = "\x1b[38;2;142;192;124;1m" // #8ec07c bold
		reset = "\x1b[0m"
	)
	var b strings.Builder
	fmt.Fprintf(&b, "\n  %sghostmux rail — keys%s\n\n", title, reset)
	for _, k := range keyHelpTable {
		fmt.Fprintf(&b, "  %s%9s%s  %s%s%s\n", key, k.key, reset, desc, k.desc, reset)
	}
	fmt.Fprintf(&b, "\n  %sgutter:%s  %s●%s bell   %s✓%s done   %s~%s activity   %s▸%s viewing\n",
		gray, reset, bell, reset, done, reset, act, reset, view, reset)
	fmt.Fprintf(&b, "  %sinner tmux prefix: ctrl+b ctrl+b%s\n", gray, reset)
	os.Stdout.WriteString(b.String())
	buf := make([]byte, 1)
	os.Stdin.Read(buf) // block until one byte (any key closes the popup); EOF (e.g. </dev/null) returns immediately
	return nil
}

// helpPage renders the in-pane help view that `?` toggles — sized for the
// 30-col rail, keys from keyHelpTable (the single source of truth),
// descriptions truncated to fit rather than wrap.
func helpPage(_ int) string {
	const keyCol = 9
	descWidth := railWidth - keyCol - 4
	var b strings.Builder
	b.WriteString(" " + styTitleAccent.Render("▍") + styTitleName.Render("ghostmux") + styTitleTail.Render(" ▸ keys") + "\n\n")
	for _, k := range keyHelpTable {
		key := truncateLabel(k.key, keyCol)
		b.WriteString(" " + styActivity.Render(fmt.Sprintf("%*s", keyCol, key)) +
			"  " + styHint.Render(truncateLabel(k.desc, descWidth)) + "\n")
	}
	b.WriteString("\n " + styHint.Render("gutter:") + " " +
		styBell.Render("●") + styHint.Render("bell ") +
		rowStyle(hexAttached, false, false).Render("✓") + styHint.Render("done") + "\n")
	b.WriteString("         " + styActivity.Render("~") + styHint.Render("act  ") +
		styTitleAccent.Render("▸") + styHint.Render("viewing") + "\n")
	b.WriteString(" " + styHint.Render("hub: ctrl+b → inner tmux") + "\n")
	b.WriteString("\n " + styHint.Render("? / esc close"))
	return b.String()
}
