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

// gmViewPrefix names the transient grouped sessions the viewport attaches
// through when the target is attached elsewhere (e.g. over ssh): same
// windows, independent size and current-window, so the other client never
// fights the viewport. destroy-unattached makes them self-cleaning.
const gmViewPrefix = "gm-view-"

// viewport is the right-hand pane of the hub: a live nested tmux client the
// rail re-points without ever moving itself.
type viewport struct {
	pane        string // %id of the pane
	idleCmd     string // command that renders the idle placeholder
	lockSess    string // session currently rendered, "" = idle
	lockWin     string // window index within lockSess
	lockBackend string // "" = tmux; e.g. "zellij"
	detached    bool   // user pressed d — heal re-idles instead of re-pointing
	grouped     bool   // attached via a gm-view-* grouped session (tmux only)
}

// attachTarget is the session name the viewport's client is actually attached
// to: the grouped shadow when grouped, otherwise the session itself.
func (v viewport) attachTarget() string {
	if v.grouped {
		return gmViewPrefix + v.lockSess
	}
	return v.lockSess
}

// point respawns the viewport as a nested tmux client rendering sess (and a
// window, if given). grouped attaches through a transient session group so an
// ssh (or any outside) client on the same session keeps its own size and
// focus. It updates the lock and clears the viewed window's marks.
func (v *viewport) point(sess, window string, grouped bool) {
	if v.pane == "" || sess == "" {
		return
	}
	// TMUX= lets a client attach from inside tmux; the socket args keep the
	// nested client on the server the rail is driving; \; chains commands
	// onto the attach.
	target := sess
	var attach string
	if grouped {
		target = gmViewPrefix + sess
		attach = fmt.Sprintf(
			"TMUX= tmux%s new-session -A -s '%s' -t '=%s' \\; set-option destroy-unattached on \\; set-option status-left '[%s] '",
			tmux.ArgvString(), target, sess, sess)
	} else {
		attach = fmt.Sprintf("TMUX= tmux%s attach-session -t '=%s'", tmux.ArgvString(), sess)
	}
	if window != "" {
		attach += fmt.Sprintf(" \\; select-window -t '=%s:%s'", target, window)
	}
	tmux.Run("respawn-pane", "-k", "-t", v.pane, attach)
	v.lockSess, v.lockWin, v.detached, v.grouped = sess, window, false, grouped
	v.lockBackend = ""
	if window != "" {
		tmux.SetDone(sess, window, false)
	}
}

// pointAux respawns the viewport onto a non-tmux backend's session. The
// attach command is per-backend; no grouped machinery — backends without
// session groups simply attach directly.
func (v *viewport) pointAux(backend, sess string) {
	if v.pane == "" || sess == "" {
		return
	}
	var attach string
	switch backend {
	case "zellij":
		attach = fmt.Sprintf("zellij attach '%s'", sess)
	default:
		return
	}
	tmux.Run("respawn-pane", "-k", "-t", v.pane, attach)
	v.lockSess, v.lockWin, v.lockBackend = sess, "", backend
	v.detached, v.grouped = false, false
}

// idle respawns the viewport onto the ghostmux idle placeholder and drops any
// session lock.
func (v *viewport) idle() {
	if v.pane == "" {
		return
	}
	tmux.Run("respawn-pane", "-k", "-t", v.pane, v.idleCmd)
	v.lockSess, v.lockWin, v.lockBackend = "", "", ""
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
	} else if v.lockBackend != "" {
		v.pointAux(v.lockBackend, v.lockSess)
	} else {
		v.point(v.lockSess, v.lockWin, v.grouped)
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
