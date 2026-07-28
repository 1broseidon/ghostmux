package rail

import (
	"time"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

// activityNow is the ledger's clock; injectable so tests control the settle
// window deterministically.
var activityNow = time.Now

// viewSettleWindow is how long output after the viewport leaves a window is
// attributed to the panel itself. Leaving resizes the window for whatever
// client remains, and a TUI redraws on resize — output the panel caused, not
// the program speaking. Real events inside this window are swallowed; that is
// the bounded price of not ringing our own doorbell on every departure.
const viewSettleWindow = 2 * time.Second

// activityLedger is panel-local acknowledgement state. tmux's timestamp is
// stable across linked-session aliases, so one entry represents the window
// rather than whichever session/index row happened to expose it.
type activityLedger map[string]activityLedgerEntry

type activityLedgerEntry struct {
	last   int64
	unread bool
	// bellAck is the activity high-water mark at the moment a native bell was
	// last on screen in the viewport. A grouped (gm-view-*) attach can never
	// clear the origin winlink's flag — no client displays the origin's own
	// winlink — so the flag alone cannot distinguish "still ringing" from
	// "seen already". The ack records the seeing; the timestamp advancing
	// past it is the evidence that something new happened since.
	bellAck int64
	// viewed records whether the previous observation saw this window in the
	// viewport; the true→false edge is the moment the panel left it, which
	// starts the settle window below.
	viewed      bool
	settleUntil time.Time
}

// observeActivity baselines new IDs, marks timestamp advances that happened
// off-view, acknowledges any stable ID currently viewed through any alias, and
// drops vanished IDs. Native window_activity_flag values are only ORed with the
// ledger and are therefore still honored when users enabled tmux monitoring.
//
// Bells get the same treatment: viewing a belled window records an ack, and
// while tmux's flag persists with no output newer than that ack, the bell is
// suppressed — you saw it, and nothing has happened since. New output past
// the ack re-arms it.
func (m *railModel) observeActivity(windows []tmux.Window) {
	lock := ViewState{}
	if m.vp != nil {
		lock = m.vp.Lock()
	}

	type observation struct {
		last   int64
		viewed bool
		bell   bool
	}
	observed := make(map[string]observation, len(windows))
	for _, window := range windows {
		if window.WindowID == "" {
			continue
		}
		value := observed[window.WindowID]
		if window.ActivityAt > value.last {
			value.last = window.ActivityAt
		}
		value.viewed = value.viewed || isViewed(lock, window.Session, window.Index, window.Active)
		value.bell = value.bell || window.Bell
		observed[window.WindowID] = value
	}

	now := activityNow()
	next := make(activityLedger, len(observed))
	for id, current := range observed {
		previous, known := m.activity[id]
		entry := activityLedgerEntry{last: current.last, viewed: current.viewed}
		if known {
			entry.unread = previous.unread
			entry.bellAck = previous.bellAck
			entry.settleUntil = previous.settleUntil
			if current.last < previous.last {
				// A stable tmux window ID should not regress. Retaining the high-water
				// mark avoids manufacturing activity from a clock anomaly.
				entry.last = previous.last
			}
			if previous.viewed && !current.viewed {
				entry.settleUntil = now.Add(viewSettleWindow)
			}
			if !current.viewed && current.last > previous.last {
				if now.Before(entry.settleUntil) {
					// Departure redraw: the panel's own leaving produced this
					// output. Absorb it — including into the bell ack, or the
					// unclearable grouped-attach flag would re-arm itself.
					if entry.bellAck > 0 {
						entry.bellAck = entry.last
					}
				} else {
					entry.unread = true
				}
			}
		}
		if current.viewed {
			entry.unread = false
			if current.bell {
				entry.bellAck = entry.last
			}
		}
		next[id] = entry
	}
	m.activity = next

	for i := range windows {
		state, ok := next[windows[i].WindowID]
		if !ok {
			continue
		}
		if state.unread {
			windows[i].Activity = true
		}
		if windows[i].Bell && state.bellAck > 0 && state.last <= state.bellAck {
			// The flag persists but nothing new arrived since it was seen: an
			// unclearable grouped-attach bell, not a fresh one.
			windows[i].Bell = false
		}
	}
}
