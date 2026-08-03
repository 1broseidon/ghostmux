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

	// lines is the last observed absolute write position (Σ pane
	// history+cursor); linesSeen is that position when the window was last
	// viewed. Their difference is the unread count — output banked while
	// unseen. Growth is trusted only when the activity timestamp also
	// advanced: a resize reflow rewraps history and moves the totals without
	// any output, and that must never mint unread lines.
	lines     int
	linesSeen int
}

// unreadInfo is what the peek path needs about one window: how many lines are
// banked, which pane to capture them from, and whether that pane is a
// full-screen program (whose line history says less).
type unreadInfo struct {
	count int
	pane  string
	alt   bool
}

// pulseRing is one window's output cadence: eight 8-second buckets counting
// observed activity advances — a fact per tick, not a rate estimate. bucket
// is the unix/8 identity of the newest cell, so stale cells zero on rollover.
type pulseRing struct {
	counts [pulseCells]uint8
	bucket int64
}

const (
	pulseCells      = 8
	pulseBucketSecs = 8
)

func (r *pulseRing) roll(now int64) {
	if r.bucket == 0 {
		r.bucket = now
		return
	}
	for r.bucket < now {
		r.bucket++
		r.counts[r.bucket%pulseCells] = 0
	}
}

func (r *pulseRing) mark(now int64) {
	r.roll(now)
	idx := now % pulseCells
	if r.counts[idx] < pulseBucketSecs {
		r.counts[idx]++
	}
}

// cells returns the ring oldest→newest for rendering.
func (r *pulseRing) cells(now int64) []uint8 {
	r.roll(now)
	out := make([]uint8, 0, pulseCells)
	for i := int64(1); i <= pulseCells; i++ {
		out = append(out, r.counts[(now+i)%pulseCells])
	}
	return out
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
//
// Lines get the same treatment again: viewing drains the banked count, the
// departure settle absorbs the panel's own redraw, and only growth backed by
// an activity advance counts as output. One acknowledgement pattern, three
// kinds of evidence.
func (m *railModel) observeActivity(windows []tmux.Window) {
	lock := ViewState{}
	if m.vp != nil {
		lock = m.vp.Lock()
	}
	walled := m.walledSet()

	type observation struct {
		last   int64
		viewed bool
		bell   bool
		lines  int
		pane   string
		alt    bool
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
		value.viewed = value.viewed || isViewed(lock, walled, window.Session, window.Index, window.Active)
		value.bell = value.bell || window.Bell
		if value.pane == "" {
			lines := 0
			for _, pane := range window.Panes {
				lines += pane.Lines
				if pane.Active || value.pane == "" {
					value.pane, value.alt = pane.ID, pane.Alt
				}
			}
			value.lines = lines
		}
		observed[window.WindowID] = value
	}

	now := activityNow()
	pulseNow := now.Unix() / pulseBucketSecs
	if m.pulse == nil {
		m.pulse = map[string]*pulseRing{}
	}
	next := make(activityLedger, len(observed))
	peek := make(map[string]unreadInfo, len(observed))
	for id, current := range observed {
		previous, known := m.activity[id]
		entry := activityLedgerEntry{
			last: current.last, viewed: current.viewed,
			lines: current.lines, linesSeen: current.lines,
		}
		if known {
			entry.unread = previous.unread
			entry.bellAck = previous.bellAck
			entry.settleUntil = previous.settleUntil
			entry.linesSeen = previous.linesSeen
			if current.last < previous.last {
				// A stable tmux window ID should not regress. Retaining the high-water
				// mark avoids manufacturing activity from a clock anomaly.
				entry.last = previous.last
			}
			if previous.viewed && !current.viewed {
				entry.settleUntil = now.Add(viewSettleWindow)
			}
			advanced := current.last > previous.last
			delta := current.lines - previous.lines
			switch {
			case delta < 0:
				// Clear, pane death, or reflow shrink: rebaseline, never negative.
				if entry.linesSeen > current.lines {
					entry.linesSeen = current.lines
				}
			case delta > 0 && !advanced:
				// The totals moved but the window provably emitted nothing:
				// a resize reflow rewrapped history. Absorb.
				entry.linesSeen += delta
			}
			if !current.viewed && advanced {
				if now.Before(entry.settleUntil) {
					// Departure redraw: the panel's own leaving produced this
					// output. Absorb it — marks, bell ack, and banked lines —
					// or the panel rings its own doorbell on every exit.
					if entry.bellAck > 0 {
						entry.bellAck = entry.last
					}
					if delta > 0 {
						entry.linesSeen += delta
					}
				} else {
					entry.unread = true
				}
			}
			ring, ok := m.pulse[id]
			if !ok {
				ring = &pulseRing{}
				m.pulse[id] = ring
			}
			if advanced {
				ring.mark(pulseNow)
			} else {
				ring.roll(pulseNow)
			}
		}
		if current.viewed {
			entry.unread = false
			entry.linesSeen = entry.lines
			if current.bell {
				entry.bellAck = entry.last
			}
		}
		if entry.linesSeen > entry.lines {
			entry.linesSeen = entry.lines
		}
		next[id] = entry
		if banked := entry.lines - entry.linesSeen; banked > 0 {
			peek[id] = unreadInfo{count: banked, pane: current.pane, alt: current.alt}
		}
	}
	m.activity = next
	m.unread = peek
	for id := range m.pulse {
		if _, ok := next[id]; !ok {
			delete(m.pulse, id)
		}
	}

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
		if banked := state.lines - state.linesSeen; banked > 0 && !state.viewed {
			windows[i].Unread = banked
		}
		if ring, ok := m.pulse[windows[i].WindowID]; ok {
			windows[i].Pulse = ring.cells(pulseNow)
		}
	}
}
