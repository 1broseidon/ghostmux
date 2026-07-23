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
	{"tab", "collapse / expand session"},
	{"n", "new session"},
	{"a", "new agent session (gm-agent-NN)"},
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

// helpFallbackPage renders a full-rail help page when `tmux display-popup`
// fails (no tmux, old tmux, popup denied) — Update() swaps View()'s body to
// this instead of quitting or leaving the user stranded (Task 10).
func helpFallbackPage(height int) string {
	var b strings.Builder
	b.WriteString(titleLine() + "\n\n")
	b.WriteString("  " + styTitleName.Render("keys") + "\n\n")
	for _, k := range keyHelpTable {
		b.WriteString(fmt.Sprintf("  %9s  %s\n", styActivity.Render(k.key), styHint.Render(k.desc)))
	}
	b.WriteString("\n  " + styHint.Render("gutter: ● bell  ✓ done  ~ activity  ▸ viewing") + "\n")
	b.WriteString("  " + styHint.Render("inner tmux prefix: ctrl+b ctrl+b") + "\n")
	b.WriteString("\n" + styHint.Render("  press ? to close"))
	return b.String()
}
