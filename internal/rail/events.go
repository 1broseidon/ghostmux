package rail

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

// hookIndex is the reserved global-hook array slot ghostmux owns. Index-scoped
// hooks never clobber a user's own hooks at other indices (D6).
const hookIndex = 133

// refreshSignal is the tmux wait-for channel the hooks pulse and the rail's
// listener blocks on.
const refreshSignal = "ghostmux-refresh"

// refreshHooks are the global hooks that should nudge the rail to reload; each
// is installed at index [133] running a run-shell that signals refreshSignal.
var refreshHooks = []string{
	"alert-bell",
	"alert-activity",
	"session-created",
	"session-closed",
	"window-linked",
	"window-unlinked",
	"window-renamed",
	// fires when a session's current window changes — this is how the rail
	// live-tracks ctrl+b navigation happening inside the viewport's client
	"session-window-changed",
}

// refreshMsg is posted by the wait-for listener when any refresh hook fires.
type refreshMsg struct{}

// installHooks sets the 7 refresh hooks at index [133]. The run-shell command
// embeds the same socket args the rail uses (tmux.ArgvString) so the wait-for
// signal reaches the SAME server the hooks fire on — the hooks run inside the
// tmux server, not this process.
func installHooks() {
	cmd := fmt.Sprintf("run-shell 'tmux%s wait-for -S %s'", tmux.ArgvString(), refreshSignal)
	for _, h := range refreshHooks {
		tmux.Run("set-hook", "-g", fmt.Sprintf("%s[%d]", h, hookIndex), cmd)
	}
}

// removeHooks unsets exactly index [133] of each refresh hook, leaving any user
// hooks at other indices intact.
func removeHooks() {
	for _, h := range refreshHooks {
		tmux.Run("set-hook", "-gu", fmt.Sprintf("%s[%d]", h, hookIndex))
	}
}

// waitLoop blocks on `tmux wait-for ghostmux-refresh` and posts a refreshMsg
// for every signal, until ctx is cancelled. The blocking child is spawned with
// CommandContext so cancelling ctx kills it — the goroutine and its tmux
// process never outlive the rail.
func waitLoop(ctx context.Context, p *tea.Program) {
	for {
		if ctx.Err() != nil {
			return
		}
		cmd := exec.CommandContext(ctx, "tmux", tmux.Argv("wait-for", refreshSignal)...)
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return // cancelled: expected on quit
			}
			time.Sleep(100 * time.Millisecond) // transient (server gone?); back off
			continue
		}
		p.Send(refreshMsg{})
	}
}

// debugRefresh logs the refresh source and a timestamp to stderr when
// GHOSTMUX_DEBUG=1, used by the event-latency acceptance check.
func debugRefresh(source string) {
	if os.Getenv("GHOSTMUX_DEBUG") == "1" {
		fmt.Fprintf(os.Stderr, "ghostmux refresh source=%s t=%s\n", source, time.Now().Format(time.RFC3339Nano))
	}
}
