package rail

import (
	"strings"
	"testing"

	"github.com/1broseidon/ghostmux/internal/state"
)

// TestReachRowsRenderAndSummon (PROTOTYPE): declared remote workspaces render
// as ↗ rows below the fleet with the host in the dim command slot, and ↵
// routes them to PointRemote — ssh in the viewport, nothing more.
func TestReachRowsRenderAndSummon(t *testing.T) {
	vp := &fakeViewport{}
	m := &railModel{
		vp: vp, collapsed: map[string]bool{},
		reach: []state.ReachTarget{{Name: "beastie", Host: "gd@beastie", Session: "work"}},
	}
	m.rebuildRows()

	if len(m.rows) != 1 {
		t.Fatalf("reach row not rendered: %+v", m.rows)
	}
	row := m.rows[0]
	if !row.reach || !row.flat || row.label != "beastie" || row.cmd != "gd@beastie" {
		t.Fatalf("reach row shape wrong: %+v", row)
	}
	if row.gutter() != "↗" {
		t.Fatalf("reach gutter = %q, want ↗", row.gutter())
	}

	m.activateRow(row)
	if len(vp.points) != 1 || vp.points[0] != "remote:beastie@gd@beastie:work" {
		t.Fatalf("↵ did not summon remotely: points=%v", vp.points)
	}
}

// TestReachRowsProveNothing: a reach row never joins the attention census,
// the Return Queue, or organization — the rail has no evidence about the
// remote side and refuses to pretend otherwise.
func TestReachRowsProveNothing(t *testing.T) {
	// Even with marks smuggled onto the row, membership tests refuse it.
	row := railRow{reach: true, flat: true, label: "r", sess: "r", bell: true, done: true, activityAt: 5}
	if attentionLeaf(row) {
		t.Fatal("reach row counted as an attention leaf")
	}
	if _, ok := organizationTargetOf(row); ok {
		t.Fatal("reach row accepted as an organization target")
	}

	vp := &fakeViewport{}
	m := &railModel{vp: vp, collapsed: map[string]bool{}, rows: []railRow{row}}
	if bells, done := m.attention(); bells != 0 || done != 0 {
		t.Fatalf("reach row fed the bar: ●%d ✓%d", bells, done)
	}
	m.returnOldest()
	if len(vp.points) != 0 || m.infoMsg != "queue empty" {
		t.Fatalf("reach row entered the queue: points=%v info=%q", vp.points, m.infoMsg)
	}

	// x is a CLI pointer for now, not a destruction.
	m.cursor = 0
	next, _ := m.Update(key("x"))
	got := next.(railModel)
	if got.mode == modeKillConfirm || !strings.Contains(got.infoMsg, "reach rm r") {
		t.Fatalf("x on reach row armed a kill: mode=%v info=%q", got.mode, got.infoMsg)
	}
}
