package rail

import (
	"github.com/1broseidon/ghostmux/internal/tmux"
)

// gmViewPrefix names the transient grouped sessions the viewport attaches
// through when the target is attached elsewhere (e.g. over ssh): same
// windows, independent size and current-window, so the other client never
// fights the viewport. destroy-unattached makes them self-cleaning.
const gmViewPrefix = "gm-view-"

// ViewState is the viewport's current lock: what it is showing, used to
// compute inView marks and to suppress the done mark on a session the user
// is watching.
type ViewState struct {
	Sess    string
	Win     string // "" = whole session (its active window)
	Backend string // "" = tmux; e.g. "zellij"
}

// Viewport is the seam between the rail's brain and whatever renders its
// selections. The panel drives an embedded terminal on a pty; the interface
// exists so the rail never learns how its selections are displayed.
type Viewport interface {
	// Point renders a tmux session (and window, if given); grouped attaches
	// through a transient gm-view-* session group.
	Point(sess, win string, grouped bool)
	// PointAux renders a non-tmux backend's session (e.g. zellij).
	PointAux(backend, sess string)
	// Idle renders the idle placeholder and drops the lock.
	Idle()
	// Detach is the `d` key: idle, and stay idle across heals.
	Detach()
	// Heal runs per data tick; it re-points a dead viewport onto its lock
	// (or idle) and reports whether it was dead — the transient hint line.
	Heal() bool
	// Lock is the current view lock, driving inView marks + done suppression.
	Lock() ViewState
	// AttachTarget is the session the viewport's client is actually attached
	// to: the gm-view-* shadow when grouped, else the session itself.
	AttachTarget() string
	// FocusViewport moves input focus to the viewport (the `l`/`→` key).
	FocusViewport()
	// SyncActiveWindow follows the viewport client's own window focus: ctrl+b
	// navigation inside the inner session changes its active window — the lock
	// tracks it so ▸, heal, and the cursor all point at what the viewport
	// actually shows. Aux backends have no observable focus; no-op for them.
	SyncActiveWindow(windows []tmux.Window)
	// OnKill reacts to a session kill: cleans up the gm-view-* shadow (which
	// would otherwise keep the killed session's windows alive in its group)
	// and idles if the kill took the lock.
	OnKill(sess, backend string)
}

// AttachArgv is the argument vector of the tmux client command that renders
// sess (and win, if given); the panel execs it on a pty. grouped attaches
// through a transient gm-view-* session group, so a client already attached
// elsewhere keeps its own size and current window. destroy-unattached makes
// the group self-cleaning: it dies with the pty child.
func AttachArgv(sess, win string, grouped bool) []string {
	target := sess
	var argv []string
	if grouped {
		target = gmViewPrefix + sess
		argv = append([]string{"tmux"}, tmux.Argv(
			"new-session", "-A", "-s", target, "-t", "="+sess,
			";", "set-option", "destroy-unattached", "on",
			";", "set-option", "status-left", "["+sess+"] ")...)
	} else {
		argv = append([]string{"tmux"}, tmux.Argv("attach-session", "-t", "="+sess)...)
	}
	if win != "" {
		argv = append(argv, ";", "select-window", "-t", "="+target+":"+win)
	}
	return argv
}

// GroupedName is the transient session AttachArgv creates when grouped — the
// name the frame must use as the attach target (and tear down on kill).
func GroupedName(sess string) string { return gmViewPrefix + sess }

// AuxAttachArgv is the attach command for a non-tmux backend's session, or
// nil for an unknown backend. No grouped machinery — backends without
// session groups simply attach directly.
func AuxAttachArgv(backend, sess string) []string {
	switch backend {
	case "zellij":
		return []string{"zellij", "attach", sess}
	default:
		return nil
	}
}
