// Package rail is a persistent left-pane navigator for tmux under ghostty.
// Ambient, glanceable state — the anti-choose-tree: always visible, live
// attention gutter (bell/activity), enter to jump anywhere.
//
//	ghostmux rail        run the TUI in the current pane (inside tmux)
//	ghostmux rail once   print one frame and exit (debugging / agents)
package rail

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

// CmdRail runs `ghostmux rail`.
func CmdRail(args []string) error {
	if len(args) > 0 && args[0] == "once" {
		for _, r := range railRows("") {
			fmt.Println(r.plain())
		}
		return nil
	}
	if os.Getenv("TMUX") == "" {
		return fmt.Errorf("rail runs inside tmux — try `ghostmux up <name>` first, then `ghostmux hub`")
	}

	// Activity tracking is off by default; the gutter needs it. Quiet the
	// on-screen messages — the rail *is* the notification surface.
	tmux.Run("set-option", "-g", "monitor-activity", "on")
	tmux.Run("set-option", "-g", "visual-activity", "off")

	// Hub layout: this pane is the rail; the other pane in this window is
	// the viewport, where selected sessions render as a nested tmux client.
	hub := strings.TrimSpace(tmux.Output("display-message", "-p", "#{session_name}"))
	viewport := findOrCreateViewport()
	// Keep the pane alive between viewport respawns.
	tmux.Run("set-option", "-p", "-t", viewport, "remain-on-exit", "on")

	_, err := tea.NewProgram(
		railModel{hub: hub, viewport: viewport, rows: railRows(hub)},
		tea.WithAltScreen()).Run()
	return err
}
