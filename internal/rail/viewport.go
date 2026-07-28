package rail

import (
	"github.com/1broseidon/ghostmux/internal/tmux"
)

// ViewState is the viewport's current lock: what it is showing, used to
// compute inView marks and to suppress the done mark on a session the user
// is watching.
type ViewState struct {
	Sess string
	Win  string // "" = whole session (its active window)
}

// Viewport is the seam between the rail's brain and whatever renders its
// selections. The panel drives an embedded terminal on a pty; the interface
// exists so the rail never learns how its selections are displayed.
type Viewport interface {
	// Point renders a tmux session (and window, if given); grouped attaches
	// through a uniquely owned transient session group.
	Point(sess, win string, grouped bool)
	// PointRemote runs ssh to a declared remote workspace (PROTOTYPE):
	// `ssh -t host -- tmux new-session -A -s session`. The lock records the
	// reach name; the panel proves nothing about the remote side.
	PointRemote(name, host, session string)
	// Idle renders the idle placeholder and drops the lock.
	Idle()
	// Detach is the `d` key: idle, and stay idle across heals.
	Detach()
	// Heal runs per data tick. It re-points a dead viewport only after a typed
	// probe proves the lock still exists, idles only after authoritative
	// absence, and returns an error without changing the view on backend outage.
	Heal() (dead bool, err error)
	// Lock is the current view lock, driving inView marks + done suppression.
	Lock() ViewState
	// AttachTarget is the session the viewport's client is actually attached
	// to: its owned shadow when grouped, else the session itself.
	AttachTarget() string
	// FocusViewport moves input focus to the viewport (the `l`/`→` key).
	FocusViewport()
	// SyncActiveWindow follows the viewport client's own window focus: ctrl+b
	// navigation inside the inner session changes its active window — the lock
	// tracks it so ▸, heal, and the cursor all point at what the viewport
	// actually shows.
	SyncActiveWindow(windows []tmux.Window)
	// OnKill reacts to a session kill: cleans up the owned shadow (which
	// would otherwise keep the killed session's windows alive in its group)
	// and idles if the kill took the lock.
	OnKill(sess string)
}
