package rail

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/1broseidon/ghostmux/internal/state"
)

func sessionRow(name string) railRow { return railRow{depth: 0, label: name, sess: name} }
func winRow(sess, idx string) railRow {
	return railRow{depth: 1, isWin: true, sess: sess, window: idx, label: idx + ":x"}
}

// TestNoGroupsIsExactlyTheOldRail: grouping must be invisible until used, or
// every existing user pays for a feature they haven't asked for.
func TestNoGroupsIsExactlyTheOldRail(t *testing.T) {
	in := []railRow{sessionRow("a"), winRow("a", "1"), sessionRow("b")}
	out := applyGroups(in, nil, nil)
	if len(out) != len(in) {
		t.Fatalf("applyGroups with no groups changed the tree: %d rows, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i].label != in[i].label || out[i].depth != in[i].depth {
			t.Errorf("row %d changed: %+v vs %+v", i, out[i], in[i])
		}
	}
}

// TestApplyGroupsNestsMembersAndKeepsStrays: grouped sessions drop one level
// under their folder; ungrouped ones keep their shape and follow.
func TestApplyGroupsNestsMembersAndKeepsStrays(t *testing.T) {
	in := []railRow{
		sessionRow("api"), winRow("api", "1"),
		sessionRow("stray"),
	}
	out := applyGroups(in, []Group{{Name: "work", Members: []string{"tmux:api"}}}, nil)

	if !out[0].isGroup || out[0].label != "work" {
		t.Fatalf("first row should be the group folder: %+v", out[0])
	}
	if out[1].sess != "api" || out[1].depth != 1 || out[1].group != "work" {
		t.Errorf("grouped session not nested: %+v", out[1])
	}
	if out[2].depth != 2 || !out[2].isWin {
		t.Errorf("grouped window not nested: %+v", out[2])
	}
	last := out[len(out)-1]
	if last.sess != "stray" || last.depth != 0 || last.group != "" {
		t.Errorf("ungrouped session should follow at depth 0: %+v", last)
	}
}

// TestGroupAggregatesMarksForFolding is the whole point of a folder in an
// attention rail: folded, it must still say something wants you, or folding
// would hide exactly what the rail exists to surface.
func TestGroupAggregatesMarksForFolding(t *testing.T) {
	quiet := sessionRow("web")
	loud := sessionRow("api")
	loud.bell = true
	out := applyGroups([]railRow{loud, quiet},
		[]Group{{Name: "work", Members: []string{"tmux:api", "tmux:web"}}}, nil)

	if !out[0].bell {
		t.Errorf("group folder did not inherit its member's bell: %+v", out[0])
	}
	if out[0].count != 2 {
		t.Errorf("group count = %d, want 2", out[0].count)
	}
}

// TestFoldedGroupHidesEverythingInside
func TestFoldedGroupHidesEverythingInside(t *testing.T) {
	rows := applyGroups(
		[]railRow{sessionRow("api"), winRow("api", "1"), sessionRow("stray")},
		[]Group{{Name: "work", Members: []string{"tmux:api"}}}, nil)

	vis := visibleRows(rows, map[string]bool{groupKey("work"): true})
	for _, r := range vis {
		if r.sess == "api" && !r.isGroup {
			t.Errorf("folded group still shows member %+v", r)
		}
	}
	if len(vis) != 2 { // the folder itself + the ungrouped stray
		t.Errorf("folded group left %d rows, want 2: %+v", len(vis), vis)
	}
}

// TestGroupAndSessionOfSameNameFoldIndependently: the collapse map is shared,
// so group keys must be namespaced or naming a group after a session would
// fold both at once.
func TestGroupAndSessionOfSameNameFoldIndependently(t *testing.T) {
	if groupKey("api") == "api" {
		t.Fatalf("group collapse key collides with the session key")
	}
	rows := applyGroups(
		[]railRow{sessionRow("api"), winRow("api", "1")},
		[]Group{{Name: "api", Members: nil}}, nil)
	vis := visibleRows(rows, map[string]bool{groupKey("api"): true})
	found := false
	for _, r := range vis {
		if r.sess == "api" && !r.isGroup {
			found = true
		}
	}
	if !found {
		t.Errorf("folding the group also folded the same-named session away")
	}
}

// TestMoveWithinAndAcrossGroups walks the full ladder: reorder inside a group,
// cross into the neighbour, then fall out of grouping entirely.
func TestMoveWithinAndAcrossGroups(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	withFakeRunner(t, map[string]string{
		"list-sessions": "api\t0\t\t\nweb\t0\t\t\ndots\t0\t\t\n",
		"list-windows": "api\t1\tzsh\t1\t0\t0\t0\n" +
			"web\t1\tzsh\t1\t0\t0\t0\n" +
			"dots\t1\tzsh\t1\t0\t0\t0\n",
	})
	m := &railModel{
		vp:        &fakeViewport{},
		collapsed: map[string]bool{},
		groups: []Group{
			{Name: "work", Members: []string{"tmux:api", "tmux:web"}},
			{Name: "personal", Members: []string{"tmux:dots"}},
		},
	}
	m.refresh()
	// rows: [work] api web [personal] dots  → cursor onto "web"
	m.cursor = 2
	if got := m.visible()[m.cursor].sess; got != "web" {
		t.Fatalf("cursor setup wrong, on %q", got)
	}
	if err := m.moveRow(-1); err != nil {
		t.Fatalf("moveRow: %v", err)
	}
	if got := m.groups[0].Members; got[0] != "tmux:web" {
		t.Errorf("move up within group failed: %v", got)
	}
	// now push it down twice: back to second, then across into personal
	m.moveRow(1)
	m.moveRow(1)
	if gi, _ := m.findMember("tmux:web"); gi != 1 {
		t.Errorf("web did not cross into the next group: %+v", m.groups)
	}
	// and once more off the bottom: ungrouped
	m.moveRow(1)
	m.moveRow(1)
	if gi, _ := m.findMember("tmux:web"); gi != -1 {
		t.Errorf("web should have left grouping entirely: %+v", m.groups)
	}
}

// TestCursorFollowsTheMovedRow: without this the cursor keeps a screen
// position and the next keypress drags a different session.
func TestCursorFollowsTheMovedRow(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	withFakeRunner(t, map[string]string{
		"list-sessions": "api\t0\t\t\nweb\t0\t\t\n",
		"list-windows": "api\t1\tzsh\t1\t0\t0\t0\n" +
			"web\t1\tzsh\t1\t0\t0\t0\n",
	})
	m := &railModel{
		vp: &fakeViewport{}, collapsed: map[string]bool{},
		groups: []Group{{Name: "work", Members: []string{"tmux:api", "tmux:web"}}},
	}
	m.refresh()
	m.cursor = 2 // "web"
	m.moveRow(-1)
	if got := m.visible()[m.cursor].sess; got != "web" {
		t.Errorf("cursor landed on %q after the move, want web", got)
	}
}

// TestGroupsRoundTripOnDisk — the state file is the only thing ghostmux owns
// that a relaunch cannot rediscover, so it has to survive one.
func TestGroupsRoundTripOnDisk(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	want := []Group{{Name: "work", Members: []string{"tmux:api", "zellij:myz"}}}
	if err := saveState(want, nil, nil); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	got, _, _ := loadState()
	if len(got) != 1 || got[0].Name != "work" || len(got[0].Members) != 2 ||
		got[0].Members[1] != "zellij:myz" {
		t.Errorf("round trip lost data: %+v", got)
	}
	// A key written by the retired multi-backend prototype survives the round
	// trip untouched: this build never renders it, but it never destroys it.
	if !strings.Contains(strings.Join(got[0].Members, ","), "zellij:") {
		t.Errorf("foreign-prefix membership not preserved")
	}
}

// TestCorruptStateFileIsVisibleAndWriteBlocked verifies that startup remains
// usable without treating corruption as empty writable state.
func TestCorruptStateFileIsVisibleAndWriteBlocked(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	path := dir + "/ghostmux/groups.json"
	if err := os.MkdirAll(dir+"/ghostmux", 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("{not json")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(path)
	if err == nil {
		t.Fatal("corrupt state loaded without error")
	}
	withFakeRunner(t, map[string]string{"list-sessions": "", "list-windows": ""})
	m := New(&fakeViewport{}, store)
	if !strings.Contains(m.View(), "state read-only: corrupt") {
		t.Fatalf("rail did not display startup corruption: %q", m.View())
	}
	if err := m.createGroup("work"); err == nil {
		t.Fatal("write through corrupt Store succeeded")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil || string(got) != string(original) {
		t.Fatalf("corrupt primary changed: %q err=%v", got, readErr)
	}
	if len(m.groups) != 0 {
		t.Fatalf("failed write changed local groups: %+v", m.groups)
	}
}

// TestDeleteGroupKeepsSessions: a shelf is not the things on it.
func TestDeleteGroupKeepsSessions(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	withFakeRunner(t, map[string]string{
		"list-sessions": "api\t0\t\t\n",
		"list-windows":  "api\t1\tzsh\t1\t0\t0\t0\n",
	})
	m := &railModel{vp: &fakeViewport{}, collapsed: map[string]bool{},
		groups: []Group{{Name: "work", Members: []string{"tmux:api"}}}}
	if err := m.deleteGroup("work"); err != nil {
		t.Fatalf("deleteGroup: %v", err)
	}
	if len(m.groups) != 0 {
		t.Errorf("group not deleted: %+v", m.groups)
	}
	found := false
	for _, r := range m.rows {
		if r.sess == "api" {
			found = true
		}
	}
	if !found {
		t.Errorf("deleting a group destroyed its session's row")
	}
}

// TestCreateGroupRejectsBlankAndDuplicate
func TestCreateGroupRejectsBlankAndDuplicate(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	withFakeRunner(t, map[string]string{"list-sessions": "", "list-windows": ""})
	m := &railModel{vp: &fakeViewport{}, collapsed: map[string]bool{}}
	if err := m.createGroup("   "); err == nil {
		t.Errorf("blank group name accepted")
	}
	if err := m.createGroup("work"); err != nil {
		t.Fatalf("createGroup: %v", err)
	}
	if err := m.createGroup("work"); err == nil {
		t.Errorf("duplicate group name accepted")
	}
}

// TestActivatingAGroupFoldsItFromKeyboardAndMouse is the regression for a
// click on a group row trying to attach to a session named after the group.
// ↵ and a second click are the same action and must stay that way; the bug
// existed because the keyboard path knew about groups and the mouse path did
// not.
func TestActivatingAGroupFoldsItFromKeyboardAndMouse(t *testing.T) {
	// toggleFold persists — without this the test writes the REAL state file
	// and silently replaces the developer's own groups.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	withFakeRunner(t, map[string]string{
		"list-sessions": "api\t0\t\t\n",
		"list-windows":  "api\t1\tzsh\t1\t0\t0\t0\n",
	})
	vp := &fakeViewport{}
	m := &railModel{vp: vp, collapsed: map[string]bool{},
		groups: []Group{{Name: "dev", Members: []string{"tmux:api"}}}}
	m.refresh()

	if !m.visible()[0].isGroup {
		t.Fatalf("expected a group row first, got %+v", m.visible()[0])
	}

	// keyboard: ↵ on the group
	m.cursor = 0
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(railModel)
	if !got.collapsed[groupKey("dev")] {
		t.Errorf("↵ on a group did not fold it")
	}
	if len(vp.points) != 0 {
		t.Errorf("↵ on a group attached the viewport to %v", vp.points)
	}

	// mouse: a second click on the already-selected group row
	m2 := &railModel{vp: vp, collapsed: map[string]bool{},
		groups: []Group{{Name: "dev", Members: []string{"tmux:api"}}}}
	m2.refresh()
	m2.cursor = 0
	next, _ = m2.Update(tea.MouseMsg{
		X: 2, Y: treeTop, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft,
	})
	got = next.(railModel)
	if !got.collapsed[groupKey("dev")] {
		t.Errorf("click on a group did not fold it")
	}
	if len(vp.points) != 0 {
		t.Errorf("click on a group attached the viewport to %v", vp.points)
	}
}

// TestPointRowRefusesGroups is the belt to activateRow's braces: no future
// path may attach a viewport to a group name.
func TestPointRowRefusesGroups(t *testing.T) {
	vp := &fakeViewport{}
	m := &railModel{vp: vp, collapsed: map[string]bool{}}
	m.pointRow(railRow{isGroup: true, label: "dev", sess: "dev"})
	if len(vp.points) != 0 {
		t.Errorf("pointRow attached to a group: %v", vp.points)
	}
}

// TestFoldStateSurvivesRestart: a folder that springs open on every relaunch
// is not a folder. Fold state is the same kind of thing as membership — a
// decision no multiplexer can report back — so it persists with it.
func TestFoldStateSurvivesRestart(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	withFakeRunner(t, map[string]string{
		"list-sessions": "api\t0\t\t\n",
		"list-windows":  "api\t1\tzsh\t1\t0\t0\t0\n",
	})
	groups := []Group{{Name: "dev", Members: []string{"tmux:api"}}}
	m := &railModel{vp: &fakeViewport{}, collapsed: map[string]bool{}, groups: groups}
	m.refresh()
	m.toggleFold(m.visible()[0]) // fold the group

	// a fresh model is what a relaunch builds
	again := New(&fakeViewport{})
	if !again.collapsed[groupKey("dev")] {
		t.Errorf("group did not stay folded across a restart: %+v", again.collapsed)
	}
	vis := again.visible()
	for _, r := range vis {
		if r.sess == "api" && !r.isGroup {
			t.Errorf("relaunched rail showed a member of a folded group: %+v", r)
		}
	}

	// and unfolding must persist too, not just folding
	again.toggleFold(vis[0])
	if _, collapsed, _ := loadState(); collapsed[groupKey("dev")] {
		t.Errorf("unfold was not persisted")
	}
}

// TestOnlyFoldedKeysAreWritten keeps the state file minimal and stable:
// absent means open, and the list is sorted so runs produce no spurious diffs.
func TestOnlyFoldedKeysAreWritten(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := saveState(nil, map[string]bool{"b": true, "a": true, "open": false}, nil); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	b, err := os.ReadFile(groupsPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "open") {
		t.Errorf("unfolded row was written to the state file: %s", b)
	}
	if i, j := strings.Index(string(b), `"a"`), strings.Index(string(b), `"b"`); i < 0 || j < 0 || i > j {
		t.Errorf("fold keys not written in sorted order: %s", b)
	}
}

// TestRailConflictAdoptsDiskAndReportsUncommittedChange verifies the TUI
// conflict path rather than only Store's error contract.
func TestRailConflictAdoptsDiskAndReportsUncommittedChange(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/ghostmux/groups.json"
	first, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	external, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	withFakeRunner(t, map[string]string{"list-sessions": "", "list-windows": ""})
	m := New(&fakeViewport{}, first)
	if err := external.Update(func(doc *state.Document) error {
		doc.Groups = []state.Group{{Name: "external"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	err = m.createGroup("local")
	if err == nil || err.Error() != "state changed in another panel; change not saved" {
		t.Fatalf("conflict error = %v", err)
	}
	if len(m.groups) != 1 || m.groups[0].Name != "external" {
		t.Fatalf("rail did not adopt external snapshot: %+v", m.groups)
	}
	if len(m.rows) == 0 || !m.rows[0].isGroup || m.rows[0].label != "external" {
		t.Fatalf("rows were not rebuilt from external snapshot: %+v", m.rows)
	}
}
