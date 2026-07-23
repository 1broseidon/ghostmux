package rail

import (
	"fmt"
	"os"
	"strings"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

// findOrCreateViewport returns the other pane in the rail's window,
// splitting a fresh one if the rail is alone.
func findOrCreateViewport() string {
	mine := os.Getenv("TMUX_PANE")
	for _, id := range tmux.Lines("list-panes", "-F", "#{pane_id}") {
		if id != mine && id != "" {
			return id
		}
	}
	return strings.TrimSpace(tmux.Output("split-window", "-h", "-d", "-P", "-F", "#{pane_id}"))
}

// jump renders the selected session into the viewport pane as a nested
// tmux client. The rail never moves; enter only re-points the viewport.
func (m railModel) jump() {
	if m.cursor >= len(m.rows) || m.viewport == "" {
		return
	}
	r := m.rows[m.cursor]
	// TMUX= lets a client attach from inside tmux; \; chains a window
	// selection after the attach when a window row was chosen.
	attach := fmt.Sprintf("TMUX= tmux attach-session -t '=%s'", r.sess)
	if r.window != "" {
		attach += fmt.Sprintf(" \\; select-window -t '=%s:%s'", r.sess, r.window)
	}
	tmux.Run("respawn-pane", "-k", "-t", m.viewport, attach)
}
