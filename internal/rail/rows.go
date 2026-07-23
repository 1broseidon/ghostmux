package rail

import (
	"fmt"
	"strings"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

// railRow is one line of the rail: a session or one of its windows.
type railRow struct {
	depth    int // 0 session, 1 window, 2 pane
	label    string
	gutter   string // attention marks: ● bell, ~ activity
	active   bool   // tmux's notion of current
	attached bool   // session rows only
	sess     string
	window   string // window index, window/pane rows
	paneID   string // pane rows only
}

func (r railRow) plain() string {
	mark := " "
	if r.active {
		mark = "*"
	}
	return fmt.Sprintf("%s%s%-2s %s", strings.Repeat("  ", r.depth), mark, r.gutter, r.label)
}

// railRows lists sessions and their windows, excluding the hub session the
// rail itself lives in (rendering the hub inside its own viewport would be
// an infinite mirror).
func railRows(hub string) []railRow {
	var rows []railRow
	sessions := tmux.Sessions()
	windows := tmux.Windows()

	for _, s := range sessions {
		if s.Name == hub {
			continue
		}
		rows = append(rows, railRow{
			depth: 0, label: s.Name, sess: s.Name, attached: s.Attached,
		})
		for _, w := range windows {
			if w.Session != s.Name {
				continue
			}
			gutter := ""
			if w.Bell {
				gutter += "●"
			}
			if w.Activity {
				gutter += "~"
			}
			rows = append(rows, railRow{
				depth: 1, label: w.Index + ":" + w.Name, gutter: gutter,
				active: w.Active, sess: s.Name, window: w.Index,
			})
		}
	}
	return rows
}
