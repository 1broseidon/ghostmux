package rail

import (
	"errors"
	"testing"
	"time"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

func activityWindow(session, id, index string, at int64) tmux.Window {
	return tmux.Window{
		Session: session, SessionID: "$1", WindowID: id, Index: index,
		Name: "shell", Active: true, ActivityAt: at,
	}
}

func TestActivityLedgerBaselineAdvanceAndAcknowledgement(t *testing.T) {
	vp := &fakeViewport{}
	m := railModel{vp: vp}

	baseline := []tmux.Window{activityWindow("alpha", "@7", "1", 100)}
	m.observeActivity(baseline)
	if baseline[0].Activity || m.activity["@7"].unread {
		t.Fatalf("baseline invented unread activity: window=%+v ledger=%+v", baseline[0], m.activity)
	}

	advanced := []tmux.Window{activityWindow("alpha", "@7", "1", 101)}
	m.observeActivity(advanced)
	if !advanced[0].Activity || !m.activity["@7"].unread {
		t.Fatalf("off-view advance was not marked: window=%+v ledger=%+v", advanced[0], m.activity)
	}

	vp.lock = ViewState{Sess: "alpha", Win: "1"}
	viewed := []tmux.Window{activityWindow("alpha", "@7", "1", 102)}
	m.observeActivity(viewed)
	if viewed[0].Activity || m.activity["@7"].unread {
		t.Fatalf("viewed window was not acknowledged: window=%+v ledger=%+v", viewed[0], m.activity)
	}
}

func TestActivityLedgerUsesStableIDsAcrossRenameIndexAndLinks(t *testing.T) {
	vp := &fakeViewport{}
	m := railModel{vp: vp}
	m.observeActivity([]tmux.Window{activityWindow("alpha", "@9", "1", 50)})

	// Session aliases and indices are presentation. The stable window ID carries
	// the high-water mark through either changing.
	renamed := []tmux.Window{activityWindow("renamed", "@9", "4", 51)}
	m.observeActivity(renamed)
	if !renamed[0].Activity {
		t.Fatal("stable ID lost unread state across session/index change")
	}

	// A linked/owned-view duplicate is one ledger observation. Viewing any
	// alias acknowledges the stable window and prevents duplicate artifacts.
	vp.lock = ViewState{Sess: "renamed", Win: "4"}
	linked := []tmux.Window{
		activityWindow("renamed", "@9", "4", 52),
		activityWindow("gm-view-owned", "@9", "1", 52),
	}
	linked[1].SessionID = "$2"
	m.observeActivity(linked)
	if linked[0].Activity || linked[1].Activity || m.activity["@9"].unread || len(m.activity) != 1 {
		t.Fatalf("linked duplicate was not acknowledged once: windows=%+v ledger=%+v", linked, m.activity)
	}
}

func TestActivityLedgerClearsVanishedIDsAndBaselinesReappearance(t *testing.T) {
	m := railModel{vp: &fakeViewport{}}
	m.observeActivity([]tmux.Window{
		activityWindow("alpha", "@1", "1", 10),
		activityWindow("alpha", "@2", "2", 20),
	})
	m.observeActivity([]tmux.Window{activityWindow("alpha", "@2", "2", 21)})
	if _, exists := m.activity["@1"]; exists || len(m.activity) != 1 {
		t.Fatalf("vanished stable ID retained: %+v", m.activity)
	}

	reappeared := []tmux.Window{activityWindow("beta", "@1", "7", 999)}
	m.observeActivity(reappeared)
	if reappeared[0].Activity {
		t.Fatal("newly observed/reappeared ID was not baselined")
	}
}

func TestActivityLedgerHonorsNativeFlag(t *testing.T) {
	vp := &fakeViewport{lock: ViewState{Sess: "alpha", Win: "1"}}
	m := railModel{vp: vp}
	window := activityWindow("alpha", "@3", "1", 10)
	window.Activity = true
	windows := []tmux.Window{window}
	m.observeActivity(windows)
	if !windows[0].Activity || m.activity["@3"].unread {
		t.Fatalf("native activity flag was cleared or became ledger unread: %+v %+v", windows[0], m.activity)
	}
}

func TestActivityLedgerOutageKeepsLastGoodStateStaleSafe(t *testing.T) {
	m := railModel{vp: &fakeViewport{}, collapsed: map[string]bool{}}
	baseline := []tmux.Window{activityWindow("alpha", "@4", "1", 10)}
	m.observeActivity(baseline)
	advanced := []tmux.Window{activityWindow("alpha", "@4", "1", 11)}
	m.observeActivity(advanced)
	m.tmuxCache = tmuxCache{
		enabled: true, hasSnapshot: true, lastErr: errors.New("transport down"),
		snapshot: tmux.Snapshot{
			Sessions: []tmux.Session{{Name: "alpha", SessionID: "$1"}},
			Windows:  advanced,
		},
	}

	before := m.activity["@4"]
	m.rebuildRows() // outage path deliberately does not call observeActivity
	if m.activity["@4"] != before {
		t.Fatalf("outage changed ledger: before=%+v after=%+v", before, m.activity["@4"])
	}
	if len(m.rows) != 1 || m.rows[0].validity != rowStale || m.rows[0].gutter() != "?" {
		t.Fatalf("last-good outage row was not stale-safe: %+v", m.rows)
	}

	recovered := []tmux.Window{activityWindow("alpha", "@4", "1", 12)}
	m.observeActivity(recovered)
	if !recovered[0].Activity {
		t.Fatal("recovery lost activity advanced across outage")
	}
}

// TestAgentQuietAge: the age is evidence rendering — empty without a
// timestamp, empty under a minute, coarse units above it.
func TestAgentQuietAge(t *testing.T) {
	now := time.Unix(1000000, 0)
	for _, tc := range []struct {
		at   int64
		want string
	}{
		{0, ""},
		{999970, ""},    // 30s: recent output is not news
		{999000, "16m"}, // 1000s ago
		{1000000 - 7200, "2h"},
		{1000000 - 3*86400, "3d"},
	} {
		if got := agentQuietAge(now, tc.at); got != tc.want {
			t.Errorf("agentQuietAge(%d) = %q, want %q", tc.at, got, tc.want)
		}
	}
}

// TestBellAckSuppressesUnclearableGroupedAttachFlag is the "bell that keeps
// coming back" regression: a grouped (gm-view-*) attach can never clear the
// origin winlink's bell flag, so without an ack the same seen bell resurfaces
// every time the viewport points elsewhere. Viewing records the ack; the
// persistent flag stays suppressed until output newer than the ack arrives —
// and the departure redraw the panel itself causes does not count as newer.
func TestBellAckSuppressesUnclearableGroupedAttachFlag(t *testing.T) {
	clock := time.Unix(5000, 0)
	origNow := activityNow
	activityNow = func() time.Time { return clock }
	t.Cleanup(func() { activityNow = origNow })

	bellWindow := func(at int64, bell bool) []tmux.Window {
		w := activityWindow("beastie", "@3", "1", at)
		w.Bell = bell
		return []tmux.Window{w}
	}
	vp := &fakeViewport{}
	m := railModel{vp: vp}

	// Unseen bell rings.
	rung := bellWindow(100, true)
	m.observeActivity(rung)
	if !rung[0].Bell {
		t.Fatalf("unseen bell was suppressed: %+v ledger=%+v", rung[0], m.activity)
	}

	// Viewing the belled window records the acknowledgement.
	vp.lock = ViewState{Sess: "beastie", Win: "1"}
	m.observeActivity(bellWindow(100, true))
	if m.activity["@3"].bellAck != 100 {
		t.Fatalf("viewing did not ack the bell: ledger=%+v", m.activity)
	}

	// Point elsewhere; the flag persists with nothing new — suppressed.
	vp.lock = ViewState{Sess: "other", Win: "1"}
	stale := bellWindow(100, true)
	m.observeActivity(stale)
	if stale[0].Bell {
		t.Fatalf("seen grouped-attach bell resurfaced: %+v ledger=%+v", stale[0], m.activity)
	}

	// Output arriving just after departure is the resize redraw the panel
	// caused (a TUI repaints when the window resizes back). Absorbed: no
	// bell, and the ack advances with it.
	clock = clock.Add(time.Second)
	redraw := bellWindow(120, true)
	m.observeActivity(redraw)
	if redraw[0].Bell || redraw[0].Activity {
		t.Fatalf("departure redraw rang the panel's own doorbell: %+v ledger=%+v", redraw[0], m.activity)
	}

	// Past the settle window, genuinely new output re-arms the bell.
	clock = clock.Add(viewSettleWindow)
	fresh := bellWindow(150, true)
	m.observeActivity(fresh)
	if !fresh[0].Bell {
		t.Fatalf("new output did not re-arm the bell: %+v ledger=%+v", fresh[0], m.activity)
	}

	// A natively cleared flag needs no ledger help and stays clear.
	cleared := bellWindow(150, false)
	m.observeActivity(cleared)
	if cleared[0].Bell {
		t.Fatalf("cleared flag re-invented a bell: %+v", cleared[0])
	}
}

// TestDepartureRedrawDoesNotMarkActivity: the same absorption for ~ — leaving
// a window resizes it, the program repaints, and that output must not mark
// the row unread. Later output does.
func TestDepartureRedrawDoesNotMarkActivity(t *testing.T) {
	clock := time.Unix(9000, 0)
	origNow := activityNow
	activityNow = func() time.Time { return clock }
	t.Cleanup(func() { activityNow = origNow })

	vp := &fakeViewport{lock: ViewState{Sess: "beastie", Win: "1"}}
	m := railModel{vp: vp}
	m.observeActivity([]tmux.Window{activityWindow("beastie", "@5", "1", 100)})

	// Leave; the redraw lands within the settle window.
	vp.lock = ViewState{Sess: "other", Win: "1"}
	clock = clock.Add(time.Second)
	redraw := []tmux.Window{activityWindow("beastie", "@5", "1", 130)}
	m.observeActivity(redraw)
	if redraw[0].Activity || m.activity["@5"].unread {
		t.Fatalf("departure redraw marked unread: %+v ledger=%+v", redraw[0], m.activity)
	}

	// Output after the settle window is real news again.
	clock = clock.Add(viewSettleWindow)
	later := []tmux.Window{activityWindow("beastie", "@5", "1", 160)}
	m.observeActivity(later)
	if !later[0].Activity || !m.activity["@5"].unread {
		t.Fatalf("post-settle output was not marked: %+v ledger=%+v", later[0], m.activity)
	}
}

func bankWindow(at int64, lines int, bell bool) []tmux.Window {
	w := activityWindow("bank", "@8", "1", at)
	w.Bell = bell
	w.Panes = []tmux.PaneStat{{ID: "%5", Lines: lines, Active: true}}
	return []tmux.Window{w}
}

// TestUnreadBanksLinesGatedOnActivity is the Unread ledger contract: growth
// counts only when the window provably emitted output (activity advanced),
// reflow growth is absorbed, shrink rebaselines, viewing drains, and the
// departure settle absorbs the panel's own redraw.
func TestUnreadBanksLinesGatedOnActivity(t *testing.T) {
	clock := time.Unix(50000, 0)
	origNow := activityNow
	activityNow = func() time.Time { return clock }
	t.Cleanup(func() { activityNow = origNow })

	vp := &fakeViewport{}
	m := railModel{vp: vp}

	// First sight baselines: no unread invented from history that predates us.
	first := bankWindow(10, 100, false)
	m.observeActivity(first)
	if first[0].Unread != 0 {
		t.Fatalf("first sight banked lines: %+v", first[0])
	}

	// Output while unseen: 30 new lines backed by an activity advance.
	grew := bankWindow(11, 130, false)
	m.observeActivity(grew)
	if grew[0].Unread != 30 {
		t.Fatalf("unseen output not banked: unread=%d want 30", grew[0].Unread)
	}

	// Reflow: totals moved with NO activity advance — absorbed, not banked.
	reflow := bankWindow(11, 145, false)
	m.observeActivity(reflow)
	if reflow[0].Unread != 30 {
		t.Fatalf("reflow minted unread: unread=%d want 30", reflow[0].Unread)
	}

	// Viewing drains the bank.
	vp.lock = ViewState{Sess: "bank", Win: "1"}
	m.observeActivity(bankWindow(12, 150, false))
	vp.lock = ViewState{Sess: "other", Win: "9"}
	clock = clock.Add(viewSettleWindow + time.Second)
	after := bankWindow(12, 150, false)
	m.observeActivity(after)
	if after[0].Unread != 0 {
		t.Fatalf("view did not drain: unread=%d", after[0].Unread)
	}

	// Departure redraw: growth within the settle window is the panel's own.
	vp.lock = ViewState{Sess: "bank", Win: "1"}
	m.observeActivity(bankWindow(13, 150, false))
	vp.lock = ViewState{Sess: "other", Win: "9"}
	clock = clock.Add(time.Second)
	redraw := bankWindow(14, 160, false)
	m.observeActivity(redraw)
	if redraw[0].Unread != 0 {
		t.Fatalf("departure redraw banked lines: unread=%d", redraw[0].Unread)
	}

	// Real output after the settle banks again; a shrink (clear) wipes the
	// bank rather than going negative or lying about content that is gone.
	clock = clock.Add(viewSettleWindow)
	fresh := bankWindow(15, 200, false)
	m.observeActivity(fresh)
	if fresh[0].Unread != 40 {
		t.Fatalf("post-settle output not banked: unread=%d want 40", fresh[0].Unread)
	}
	cleared := bankWindow(15, 50, false)
	m.observeActivity(cleared)
	if cleared[0].Unread != 0 {
		t.Fatalf("shrink went negative or kept a stale bank: unread=%d", cleared[0].Unread)
	}
}

// TestPulseRingCadence: marks land in the newest bucket, cells render
// oldest→newest, and rollover zeroes what time passed by.
func TestPulseRingCadence(t *testing.T) {
	r := &pulseRing{}
	r.mark(100)
	r.mark(100)
	cells := r.cells(100)
	if cells[len(cells)-1] != 2 {
		t.Fatalf("newest bucket = %v, want trailing 2", cells)
	}
	cells = r.cells(103)
	if cells[len(cells)-1] != 0 || cells[len(cells)-4] != 2 {
		t.Fatalf("rolled cells = %v, want 2 four from the end", cells)
	}
	if got := r.cells(200); got[0] != 0 || got[len(got)-1] != 0 {
		t.Fatalf("distant rollover kept stale counts: %v", got)
	}
}

// TestSparklineRendersOnlyWhenAlive: silence earns no pixels.
func TestSparklineRendersOnlyWhenAlive(t *testing.T) {
	if got := sparkline([]uint8{0, 0, 0, 0, 0, 0, 0, 0}); got != "" {
		t.Fatalf("flat pulse rendered %q", got)
	}
	got := sparkline([]uint8{0, 1, 4, 8, 2, 0, 0, 0})
	if got == "" || len([]rune(got)) != 8 {
		t.Fatalf("live pulse = %q", got)
	}
	if []rune(got)[3] != '█' || []rune(got)[0] != ' ' {
		t.Fatalf("levels wrong: %q", got)
	}
}
