package rail

import "github.com/1broseidon/ghostmux/internal/tmux"

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

	next := make(activityLedger, len(observed))
	for id, current := range observed {
		previous, known := m.activity[id]
		entry := activityLedgerEntry{last: current.last}
		if known {
			entry.unread = previous.unread
			entry.bellAck = previous.bellAck
			if current.last < previous.last {
				// A stable tmux window ID should not regress. Retaining the high-water
				// mark avoids manufacturing activity from a clock anomaly.
				entry.last = previous.last
			}
			if !current.viewed && current.last > previous.last {
				entry.unread = true
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
