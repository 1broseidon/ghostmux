// Package rail is the session navigator: the left column of the ghostmux
// panel. Ambient, glanceable state — the anti-choose-tree: always visible,
// live attention gutter (bell/activity/done), enter to view anything.
//
// The rail is the brain, not the frame. It owns rows, marks, modes and keys;
// where a selection is rendered is the Viewport interface's problem.
//
//	ghostmux rail once           print one frame and exit (debugging / agents)
//	ghostmux rail once --filter  print one frame with filter-dimmed rows
//	ghostmux rail once --marks   print one line of marks per row
package rail

import (
	"fmt"
)

// CmdRail runs `ghostmux rail <sub>` — debugging entry points only. The panel
// itself is `ghostmux`, which composes Model directly.
func CmdRail(args []string) error {
	if len(args) > 0 && args[0] == "once" {
		return cmdOnce(args[1:])
	}
	return fmt.Errorf("usage: ghostmux rail once [--filter q] [--marks]")
}

// cmdOnce is `ghostmux rail once [--filter q]`: one plain frame and exit,
// the headless acceptance/debugging entry point (Task 8). Plain mode never
// touches lipgloss/color — only `--filter` changes the output, by prefixing
// non-matching rows with "·"; row order and positions are unchanged either
// way.
func cmdOnce(args []string) error {
	query := ""
	marks := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--filter" && i+1 < len(args):
			query = args[i+1]
			i++
		case args[i] == "--marks":
			marks = true
		}
	}
	rows := railRows("", ViewState{})
	rows = append(rows, auxRows(auxSessions(), ViewState{})...)
	for _, r := range rows {
		switch {
		case marks:
			fmt.Println(r.marks())
		case query != "":
			fmt.Println(r.plainFiltered(query))
		default:
			fmt.Println(r.plain())
		}
	}
	return nil
}

// IdleLine is one line of the idle placeholder (DESIGN.md screen 4).
type IdleLine struct {
	Text   string
	Accent bool // leading ▸ rendered in orange, rest dim
}

// IdleLines is the idle placeholder's content, rendered by the frame in
// process when the viewport holds no session.
func IdleLines() []IdleLine {
	return []IdleLine{
		{"▸ ghostmux", true},
		{"the rail is watching", false},
		{"tmux new -s work  →  it appears", false},
	}
}
