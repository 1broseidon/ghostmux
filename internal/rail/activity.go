package rail

import "github.com/1broseidon/ghostmux/internal/tmux"

// activityLedger is panel-local acknowledgement state. tmux's timestamp is
// stable across linked-session aliases, so one entry represents the window
// rather than whichever session/index row happened to expose it.
type activityLedger map[string]activityLedgerEntry

type activityLedgerEntry struct {
	last   int64
	unread bool
}

// observeActivity baselines new IDs, marks timestamp advances that happened
// off-view, acknowledges any stable ID currently viewed through any alias, and
// drops vanished IDs. Native window_activity_flag values are only ORed with the
// ledger and are therefore still honored when users enabled tmux monitoring.
func (m *railModel) observeActivity(windows []tmux.Window) {
	lock := ViewState{}
	if m.vp != nil {
		lock = m.vp.Lock()
	}

	type observation struct {
		last   int64
		viewed bool
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
		observed[window.WindowID] = value
	}

	next := make(activityLedger, len(observed))
	for id, current := range observed {
		previous, known := m.activity[id]
		entry := activityLedgerEntry{last: current.last}
		if known {
			entry.unread = previous.unread
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
		}
		next[id] = entry
	}
	m.activity = next

	for i := range windows {
		if state, ok := next[windows[i].WindowID]; ok && state.unread {
			windows[i].Activity = true
		}
	}
}
