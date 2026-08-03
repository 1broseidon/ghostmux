package rail

import (
	"testing"
	"time"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

// wallFixtureRows builds a group header plus n fresh members, one ghost, and
// one stale (uncertain backend) member — exactly the mix wallMembers must
// filter down to fresh, non-ghost sessions.
func wallFixtureRows(group string, liveCount int) []railRow {
	rows := []railRow{{isGroup: true, label: group, sess: group}}
	for i := 0; i < liveCount; i++ {
		name := string(rune('a' + i))
		rows = append(rows, railRow{
			flat: true, label: name, sess: name, group: group, validity: rowFresh,
		})
	}
	rows = append(rows,
		railRow{flat: true, label: "gho", sess: "gho", group: group, ghost: true, validity: rowFresh},
		railRow{flat: true, label: "unc", sess: "unc", group: group, validity: rowStale},
	)
	return rows
}

// TestWallMembersFiltersFreshNonGhostAndCaps: member collection reads raw
// rows (fold-independent), admits only fresh non-ghost sessions, and caps at
// wallMemberCap while still reporting the uncapped total.
func TestWallMembersFiltersFreshNonGhostAndCaps(t *testing.T) {
	m := &railModel{rows: wallFixtureRows("work", 2)}
	members, total := m.wallMembers("work")
	if total != 2 || len(members) != 2 || members[0] != "a" || members[1] != "b" {
		t.Fatalf("wallMembers = %v (total %d), want [a b] (total 2)", members, total)
	}

	m = &railModel{rows: wallFixtureRows("big", 8)}
	members, total = m.wallMembers("big")
	if total != 8 {
		t.Fatalf("total = %d, want 8", total)
	}
	if len(members) != wallMemberCap {
		t.Fatalf("capped members = %d, want %d: %v", len(members), wallMemberCap, members)
	}
}

// TestVOnNonGroupFlashesAndDoesNothing: v is group-only; law 4's mouse/enter
// parity has its own key, and v must never attach anything on a leaf.
func TestVOnNonGroupFlashesAndDoesNothing(t *testing.T) {
	vp := &fakeViewport{}
	m := &railModel{
		vp: vp, collapsed: map[string]bool{},
		rows: []railRow{{flat: true, label: "alpha", sess: "alpha", validity: rowFresh}},
	}
	m.toggleWall(m.rows[0])
	if m.infoMsg != "v views a group" {
		t.Fatalf("info = %q, want the non-group flash", m.infoMsg)
	}
	if len(vp.walls) != 0 {
		t.Fatalf("v on a non-group row called PointWall: %v", vp.walls)
	}
}

// TestVOnEmptyGroupFlashesNothingToWall: no live members walls nothing, and
// says so instead of composing an empty session.
func TestVOnEmptyGroupFlashesNothingToWall(t *testing.T) {
	vp := &fakeViewport{}
	rows := wallFixtureRows("work", 0)
	m := &railModel{vp: vp, collapsed: map[string]bool{}, rows: rows}
	m.toggleWall(rows[0])
	if m.infoMsg != "nothing to wall" {
		t.Fatalf("info = %q, want %q", m.infoMsg, "nothing to wall")
	}
	if len(vp.walls) != 0 {
		t.Fatalf("empty group called PointWall: %v", vp.walls)
	}
}

// TestVWallsThenTogglesDown is the toggle semantics: v on a group composes
// the wall and records the exact member set for the ledger; v again, cursor
// on any row, tears it down and drops that record.
func TestVWallsThenTogglesDown(t *testing.T) {
	vp := &fakeViewport{}
	rows := wallFixtureRows("work", 2)
	m := &railModel{vp: vp, collapsed: map[string]bool{}, rows: rows}

	m.toggleWall(rows[0])
	if len(vp.walls) != 1 || vp.walls[0][0] != "work" {
		t.Fatalf("PointWall not called with the group: %v", vp.walls)
	}
	if len(m.walled) != 2 || m.walled[0] != "a" || m.walled[1] != "b" {
		t.Fatalf("walled member record = %v, want [a b]", m.walled)
	}
	if !vp.Lock().Wall || vp.Lock().Sess != "work" {
		t.Fatalf("viewport lock did not report the wall: %+v", vp.Lock())
	}
	if m.infoMsg != "wall · work" {
		t.Fatalf("info = %q", m.infoMsg)
	}

	// Toggle off from an unrelated row: v is a toggle regardless of cursor.
	m.toggleWall(railRow{flat: true, label: "a", sess: "a"})
	if vp.Lock().Wall {
		t.Fatalf("second v did not tear the wall down: %+v", vp.Lock())
	}
	if m.walled != nil {
		t.Fatalf("walled record survived teardown: %v", m.walled)
	}
	if m.infoMsg != "wall closed" {
		t.Fatalf("info = %q", m.infoMsg)
	}
}

// TestVWallCapFlashesFirstNOfTotal: law 5, bounded honestly — more than the
// cap still walls, but says exactly how many were left out.
func TestVWallCapFlashesFirstNOfTotal(t *testing.T) {
	vp := &fakeViewport{}
	rows := wallFixtureRows("big", 8)
	m := &railModel{vp: vp, collapsed: map[string]bool{}, rows: rows}

	m.toggleWall(rows[0])
	if len(vp.walls) != 1 || len(vp.walls[0])-1 != wallMemberCap {
		t.Fatalf("PointWall members = %v, want %d", vp.walls, wallMemberCap)
	}
	if len(m.walled) != wallMemberCap {
		t.Fatalf("walled record = %v, want %d entries", m.walled, wallMemberCap)
	}
	want := "wall: first 6 of 8"
	if m.infoMsg != want {
		t.Fatalf("info = %q, want %q", m.infoMsg, want)
	}
}

// TestVWallRefusedViewFlashesError: if the viewport cannot publish a walled
// lock, the press must say so rather than pretending it walled.
func TestVWallRefusedViewFlashesError(t *testing.T) {
	vp := &fakeViewport{wallBlocked: true}
	rows := wallFixtureRows("work", 2)
	m := &railModel{vp: vp, collapsed: map[string]bool{}, rows: rows}

	m.toggleWall(rows[0])
	if m.errMsg != "view unavailable" {
		t.Fatalf("errMsg = %q, want %q", m.errMsg, "view unavailable")
	}
	if m.walled != nil {
		t.Fatalf("refused wall still recorded members: %v", m.walled)
	}
}

// TestFakeViewportRecordsPointWall pins the rail test double's shape: it
// records exactly what it was asked to compose and publishes a Wall lock, the
// seam the rail's tests above and the ledger both depend on.
func TestFakeViewportRecordsPointWall(t *testing.T) {
	vp := &fakeViewport{}
	vp.PointWall("work", []string{"a", "b"})
	if len(vp.walls) != 1 {
		t.Fatalf("PointWall call not recorded: %v", vp.walls)
	}
	if got := vp.walls[0]; len(got) != 3 || got[0] != "work" || got[1] != "a" || got[2] != "b" {
		t.Fatalf("recorded call = %v, want [work a b]", got)
	}
	if lock := vp.Lock(); !lock.Wall || lock.Sess != "work" {
		t.Fatalf("Lock() = %+v, want a walled lock on work", lock)
	}
}

// wallWindow is a minimal fresh window fixture for ledger tests: one active
// window per member session.
func wallWindow(sess, id string, activityAt int64, bell bool) tmux.Window {
	return tmux.Window{
		Session: sess, SessionID: "$" + id, WindowID: "@" + id, Index: "1",
		Name: "shell", Active: true, ActivityAt: activityAt, Bell: bell,
	}
}

// TestWallAcknowledgesMembersActiveWindows is law 3: shadow display cannot
// clear the origin winlink's flags natively, but the wall's ledger record
// makes every walled member's active window count as viewed — marks and
// unread drain exactly as a normal locked view's do.
func TestWallAcknowledgesMembersActiveWindows(t *testing.T) {
	vp := &fakeViewport{}
	m := &railModel{vp: vp}

	// Off-view baseline: both members ring, unacknowledged.
	m.observeActivity([]tmux.Window{wallWindow("a", "1", 100, true), wallWindow("b", "2", 100, true)})

	// Wall goes up over both.
	m.walled = []string{"a", "b"}
	vp.lock = ViewState{Sess: "work", Wall: true}
	viewed := []tmux.Window{wallWindow("a", "1", 100, true), wallWindow("b", "2", 100, true)}
	m.observeActivity(viewed)
	if viewed[0].Bell || viewed[1].Bell {
		t.Fatalf("walled members' bells were not acknowledged: %+v", viewed)
	}
	if m.activity["@1"].unread || m.activity["@2"].unread {
		t.Fatalf("walled members left marked unread while walled: %+v", m.activity)
	}
}

// TestWallDeparturesSettleAbsorbsRedraw: leaving the wall resizes members
// back, and the existing departure-settle window absorbs that redraw the same
// way it does for a normal locked view — the marks stay drained, not
// re-armed by the panel's own resize.
func TestWallDeparturesSettleAbsorbsRedraw(t *testing.T) {
	clock := time.Unix(1000, 0)
	origNow := activityNow
	activityNow = func() time.Time { return clock }
	t.Cleanup(func() { activityNow = origNow })

	vp := &fakeViewport{}
	m := &railModel{vp: vp}

	m.walled = []string{"a"}
	vp.lock = ViewState{Sess: "work", Wall: true}
	m.observeActivity([]tmux.Window{wallWindow("a", "1", 100, true)})
	if m.activity["@1"].unread {
		t.Fatalf("member not acknowledged while walled: %+v", m.activity)
	}

	// Teardown: the ledger stops treating the member as viewed.
	m.walled = nil
	vp.lock = ViewState{}
	settling := []tmux.Window{wallWindow("a", "1", 101, false)}
	m.observeActivity(settling)
	if m.activity["@1"].unread {
		t.Fatalf("departure settle did not absorb the wall's own redraw: %+v", m.activity)
	}
}
