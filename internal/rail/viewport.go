package rail

import (
	"fmt"
	"strings"
	"time"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

// hubSession is the reserved name of the dedicated session `ghostmux hub`
// builds; the rail excludes it from the tree and tears it down on quit.
const hubSession = "hub"

// viewport is the right-hand pane of the hub: a live nested tmux client the
// rail re-points without ever moving itself.
type viewport struct {
	pane     string // %id of the pane
	idleCmd  string // command that renders the idle placeholder
	lockSess string // session currently rendered, "" = idle
	lockWin  string // window index within lockSess
	detached bool   // user pressed d — heal re-idles instead of re-pointing
}

// point respawns the viewport as a nested tmux client rendering sess (and a
// window, if given). It updates the lock and clears the viewed window's marks.
func (v *viewport) point(sess, window string) {
	if v.pane == "" || sess == "" {
		return
	}
	// TMUX= lets a client attach from inside tmux; the socket args keep the
	// nested client on the server the rail is driving; \; chains a window
	// selection onto the attach.
	attach := fmt.Sprintf("TMUX= tmux%s attach-session -t '=%s'", tmux.ArgvString(), sess)
	if window != "" {
		attach += fmt.Sprintf(" \\; select-window -t '=%s:%s'", sess, window)
	}
	tmux.Run("respawn-pane", "-k", "-t", v.pane, attach)
	v.lockSess, v.lockWin, v.detached = sess, window, false
	if window != "" {
		tmux.SetDone(sess, window, false)
	}
}

// idle respawns the viewport onto the ghostmux idle placeholder and drops any
// session lock.
func (v *viewport) idle() {
	if v.pane == "" {
		return
	}
	tmux.Run("respawn-pane", "-k", "-t", v.pane, v.idleCmd)
	v.lockSess, v.lockWin = "", ""
}

// heal runs on every data tick: if the pane died, re-point it onto its lock,
// or onto idle when the user detached or nothing is locked. It reports whether
// the pane was dead, which drives the transient "↵ re-point viewport" hint.
func (v *viewport) heal() bool {
	if v.pane == "" || !tmux.PaneDead(v.pane) {
		return false
	}
	if v.detached || v.lockSess == "" {
		v.idle()
	} else {
		v.point(v.lockSess, v.lockWin)
	}
	return true
}

// discoverViewport returns the pane the viewport lives in. In the hub session
// the pane is built by `ghostmux hub`, so we wait briefly for it to appear
// (construction race); elsewhere a bare `ghostmux rail` splits its own.
func discoverViewport(sess, mine string) string {
	if v := siblingPane(mine); v != "" {
		return v
	}
	if sess == hubSession {
		for range 40 {
			time.Sleep(50 * time.Millisecond)
			if v := siblingPane(mine); v != "" {
				return v
			}
		}
	}
	return strings.TrimSpace(tmux.Output("split-window", "-h", "-d", "-P", "-F", "#{pane_id}"))
}

// siblingPane returns the first pane in the current window that isn't mine.
func siblingPane(mine string) string {
	for _, id := range tmux.Lines("list-panes", "-F", "#{pane_id}") {
		if id != mine && id != "" {
			return id
		}
	}
	return ""
}
