package rail

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/1broseidon/ghostmux/internal/state"
	"github.com/1broseidon/ghostmux/internal/tmux"
)

type countingRailStore struct {
	*state.Store
	writes   int
	failNext error
}

func (s *countingRailStore) Update(mutate func(*state.Document) error) error {
	s.writes++
	if s.failNext != nil {
		err := s.failNext
		s.failNext = nil
		return err
	}
	return s.Store.Update(mutate)
}

func organizationTestModel(t *testing.T, groups []Group, collapsed map[string]bool, dirs map[string]string) (*railModel, *countingRailStore) {
	t.Helper()
	origTmuxPresent := tmuxPresent
	tmuxPresent = func() bool { return true }
	t.Cleanup(func() { tmuxPresent = origTmuxPresent })
	withFakeRunner(t, map[string]string{
		"list-sessions": "api\t0\t/tmp/api\t\nweb\t0\t/tmp/web\t\ndots\t0\t/tmp/dots\t\nstray\t0\t/tmp/stray\t\n",
		"list-windows": "api\t1\tzsh\t1\t0\t0\t0\n" +
			"web\t1\tzsh\t1\t0\t0\t0\n" +
			"dots\t1\tzsh\t1\t0\t0\t0\n" +
			"stray\t1\tzsh\t1\t0\t0\t0\n",
	})
	store, err := state.Open(t.TempDir() + "/groups.json")
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingRailStore{Store: store}
	m := &railModel{
		vp: &fakeViewport{}, store: counting, done: newDoneTracker(),
		groups: cloneGroups(groups), collapsed: cloneCollapsed(collapsed), dirs: cloneDirs(dirs),
	}
	m.refreshWithoutCapture()
	return m, counting
}

func pressRail(t *testing.T, m railModel, msg tea.KeyMsg) railModel {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(railModel)
}

func runeKey(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

func cursorToSessionForTest(t *testing.T, m *railModel, sess string) {
	t.Helper()
	m.restoreCursor(cursorIdentity{sess: sess})
	vis := m.visible()
	if m.cursor >= len(vis) || vis[m.cursor].sess != sess || vis[m.cursor].isGroup {
		t.Fatalf("could not select session %s: cursor=%d rows=%+v", sess, m.cursor, vis)
	}
}

func cursorToGroupForTest(t *testing.T, m *railModel, name string) {
	t.Helper()
	m.restoreCursor(cursorIdentity{group: name})
	vis := m.visible()
	if m.cursor >= len(vis) || !vis[m.cursor].isGroup || vis[m.cursor].label != name {
		t.Fatalf("could not select group %s: cursor=%d rows=%+v", name, m.cursor, vis)
	}
}

func TestMoveOrganizationPureOneStepSemantics(t *testing.T) {
	base := []Group{
		{Name: "one", Members: []string{"tmux:api", "tmux:web"}},
		{Name: "two", Members: []string{"tmux:dots"}},
	}
	cases := []struct {
		name   string
		target organizationTarget
		dir    int
		want   []Group
	}{
		{"group swap", organizationTarget{group: true, id: "two"}, -1, []Group{{Name: "two", Members: []string{"tmux:dots"}}, {Name: "one", Members: []string{"tmux:api", "tmux:web"}}}},
		{"member reorder", organizationTarget{id: "tmux:web"}, -1, []Group{{Name: "one", Members: []string{"tmux:web", "tmux:api"}}, {Name: "two", Members: []string{"tmux:dots"}}}},
		{"next group head", organizationTarget{id: "tmux:web"}, 1, []Group{{Name: "one", Members: []string{"tmux:api"}}, {Name: "two", Members: []string{"tmux:web", "tmux:dots"}}}},
		{"previous group tail", organizationTarget{id: "tmux:dots"}, -1, []Group{{Name: "one", Members: []string{"tmux:api", "tmux:web", "tmux:dots"}}, {Name: "two"}}},
		{"last group exit", organizationTarget{id: "tmux:dots"}, 1, []Group{{Name: "one", Members: []string{"tmux:api", "tmux:web"}}, {Name: "two"}}},
		{"ungrouped upward entry", organizationTarget{id: "tmux:stray"}, -1, []Group{{Name: "one", Members: []string{"tmux:api", "tmux:web"}}, {Name: "two", Members: []string{"tmux:dots", "tmux:stray"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := cloneGroups(base)
			got, changed := moveOrganization(base, tc.target, tc.dir)
			if !changed || !groupsEqual(got, tc.want) {
				t.Fatalf("move = %+v changed=%v, want %+v", got, changed, tc.want)
			}
			if !reflect.DeepEqual(base, before) {
				t.Fatalf("pure helper mutated input: got %+v want %+v", base, before)
			}
		})
	}
}

func TestMovePreviewEscWritesNothingAndEnterWritesExactlyOnce(t *testing.T) {
	groups := []Group{
		{Name: "work", Members: []string{"tmux:api", "tmux:web"}},
		{Name: "personal", Members: []string{"tmux:dots"}},
	}
	m, store := organizationTestModel(t, groups, nil, nil)
	cursorToSessionForTest(t, m, "web")
	model := pressRail(t, *m, runeKey('m'))
	if model.mode != modeMove || model.move == nil || model.move.target.id != "tmux:web" {
		t.Fatalf("m did not start normalized move: mode=%v state=%+v", model.mode, model.move)
	}
	for _, key := range []tea.KeyMsg{runeKey('k'), runeKey('j'), runeKey('j'), runeKey('J'), runeKey('K'), {Type: tea.KeyDown}} {
		model = pressRail(t, model, key)
	}
	if store.writes != 0 {
		t.Fatalf("preview performed %d Store writes", store.writes)
	}
	if !reflect.DeepEqual(model.groups, groups) {
		t.Fatalf("preview assigned draft to persisted groups: %+v", model.groups)
	}
	model = pressRail(t, model, tea.KeyMsg{Type: tea.KeyEsc})
	if store.writes != 0 || model.mode != modeNormal || model.move != nil || !reflect.DeepEqual(model.groups, groups) {
		t.Fatalf("Esc wrote/adopted draft: writes=%d mode=%v move=%+v groups=%+v", store.writes, model.mode, model.move, model.groups)
	}

	cursorToSessionForTest(t, &model, "web")
	model = pressRail(t, model, runeKey('m'))
	model = pressRail(t, model, runeKey('k'))
	want := cloneGroups(model.move.draft)
	model = pressRail(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if store.writes != 1 {
		t.Fatalf("drop performed %d Store writes, want exactly 1", store.writes)
	}
	if model.mode != modeNormal || model.move != nil || !reflect.DeepEqual(model.groups, want) {
		t.Fatalf("drop did not adopt once: mode=%v move=%+v groups=%+v want=%+v", model.mode, model.move, model.groups, want)
	}
	if model.organizationUndo == nil || model.infoMsg != "moved · web · u undo" {
		t.Fatalf("drop undo/feedback = undo %+v info %q", model.organizationUndo, model.infoMsg)
	}
}

func TestMoveStartsFromEveryStateOnlyRowAndNormalizesWindows(t *testing.T) {
	rows := []railRow{
		{isGroup: true, label: "work", sess: "work"},
		{depth: 1, group: "work", label: "tree", sess: "tree", validity: rowStale},
		{depth: 2, isWin: true, group: "work", label: "1:zsh", sess: "tree", window: "1", validity: rowStale},
		{depth: 1, flat: true, group: "work", label: "flat", sess: "flat"},
		{depth: 1, flat: true, ghost: true, group: "work", label: "gone", sess: "gone"},
		{depth: 1, flat: true, group: "work", label: "maybe", sess: "maybe", validity: rowUnvalidated},
	}
	for i, row := range rows {
		m := railModel{rows: rows, cursor: i, groups: []Group{{Name: "work"}}, collapsed: map[string]bool{}}
		m.startMove()
		if m.mode != modeMove || m.move == nil {
			t.Errorf("row %+v did not enter move mode: info=%q", row, m.infoMsg)
			continue
		}
		if row.isWin && (m.move.target.group || m.move.target.id != "tmux:tree") {
			t.Errorf("window target = %+v, want tmux:tree session", m.move.target)
		}
		if row.sess == "maybe" && m.move.target.id != "tmux:maybe" {
			t.Errorf("uncertain target = %+v", m.move.target)
		}
	}
	m := railModel{collapsed: map[string]bool{}}
	m.startMove()
	if m.mode != modeNormal || m.infoMsg != "nothing to move" {
		t.Fatalf("empty start = mode %v info %q", m.mode, m.infoMsg)
	}
}

func TestMovePreviewSelectsCollapsedDestinationHeader(t *testing.T) {
	groups := []Group{{Name: "one", Members: []string{"tmux:api"}}, {Name: "two", Members: []string{"tmux:web"}}}
	m, store := organizationTestModel(t, groups, map[string]bool{groupKey("two"): true}, nil)
	cursorToSessionForTest(t, m, "api")
	model := pressRail(t, *m, runeKey('m'))
	model = pressRail(t, model, runeKey('j'))
	if store.writes != 0 {
		t.Fatalf("collapsed preview wrote %d times", store.writes)
	}
	row := model.visible()[model.cursor]
	if !row.isGroup || row.label != "two" {
		t.Fatalf("hidden moved target cursor = %+v, want destination header two", row)
	}
}

func TestMoveTickPreservesDraftCursorAndSkipsDirectoryCapture(t *testing.T) {
	groups := []Group{{Name: "work", Members: []string{"tmux:api", "tmux:web"}}}
	m, store := organizationTestModel(t, groups, nil, nil)
	m.vp.(*fakeViewport).lock = ViewState{Sess: "dots", Win: "1"}
	cursorToSessionForTest(t, m, "web")
	model := pressRail(t, *m, runeKey('m'))
	model = pressRail(t, model, runeKey('k'))
	wantDraft := cloneGroups(model.move.draft)
	next, _ := model.Update(railTick(time.Now()))
	model = next.(railModel)
	next, _ = model.Update(refreshMsg{})
	model = next.(railModel)
	if model.mode != modeMove || model.move == nil || !reflect.DeepEqual(model.move.draft, wantDraft) {
		t.Fatalf("tick/event lost modal draft: mode=%v move=%+v", model.mode, model.move)
	}
	if store.writes != 0 || len(model.dirs) != 0 {
		t.Fatalf("tick/event captured dirs during move: writes=%d dirs=%v", store.writes, model.dirs)
	}
	if row := model.visible()[model.cursor]; row.sess != "web" || row.isGroup {
		t.Fatalf("tick/event auto-followed away from target: %+v", row)
	}
}

func TestModalAndImmediateMovesHavePureHelperParity(t *testing.T) {
	groups := []Group{{Name: "work", Members: []string{"tmux:api", "tmux:web"}}, {Name: "personal", Members: []string{"tmux:dots"}}}
	target := organizationTarget{id: "tmux:web"}
	want, changed := moveOrganization(groups, target, 1)
	if !changed {
		t.Fatal("pure helper unexpectedly inert")
	}

	modal, modalStore := organizationTestModel(t, groups, nil, nil)
	cursorToSessionForTest(t, modal, "web")
	modalModel := pressRail(t, *modal, runeKey('m'))
	modalModel = pressRail(t, modalModel, runeKey('j'))
	if !reflect.DeepEqual(modalModel.move.draft, want) || modalStore.writes != 0 {
		t.Fatalf("modal draft=%+v writes=%d want=%+v", modalModel.move.draft, modalStore.writes, want)
	}

	immediate, immediateStore := organizationTestModel(t, groups, nil, nil)
	cursorToSessionForTest(t, immediate, "web")
	immediateModel := pressRail(t, *immediate, runeKey('J'))
	if !reflect.DeepEqual(immediateModel.groups, want) || immediateStore.writes != 1 {
		t.Fatalf("immediate groups=%+v writes=%d want=%+v", immediateModel.groups, immediateStore.writes, want)
	}
}

func TestMoveDropConflictAdoptsExternalStateAndExits(t *testing.T) {
	origTmuxPresent := tmuxPresent
	tmuxPresent = func() bool { return true }
	t.Cleanup(func() { tmuxPresent = origTmuxPresent })
	withFakeRunner(t, map[string]string{
		"list-sessions": "api\t0\t\t\nweb\t0\t\t\n",
		"list-windows":  "api\t1\tzsh\t1\t0\t0\t0\nweb\t1\tzsh\t1\t0\t0\t0\n",
	})
	path := t.TempDir() + "/groups.json"
	seed, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	initial := []Group{{Name: "work", Members: []string{"tmux:api", "tmux:web"}}}
	if err := seed.Update(func(doc *state.Document) error { doc.Groups = cloneGroups(initial); return nil }); err != nil {
		t.Fatal(err)
	}
	panelStore, _ := state.Open(path)
	external, _ := state.Open(path)
	counting := &countingRailStore{Store: panelStore}
	m := railModel{vp: &fakeViewport{}, store: counting, done: newDoneTracker(), groups: cloneGroups(initial), collapsed: map[string]bool{}}
	m.refreshWithoutCapture()
	cursorToSessionForTest(t, &m, "web")
	m = pressRail(t, m, runeKey('m'))
	m = pressRail(t, m, runeKey('k'))
	m.organizationUndo = &organizationUndo{action: "older"}
	if err := external.Update(func(doc *state.Document) error {
		doc.Groups = []state.Group{{Name: "external", Members: []string{"tmux:api"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m = pressRail(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if counting.writes != 1 || m.mode != modeNormal || m.move != nil || m.organizationUndo != nil {
		t.Fatalf("conflict lifecycle: writes=%d mode=%v move=%+v undo=%+v", counting.writes, m.mode, m.move, m.organizationUndo)
	}
	if len(m.groups) != 1 || m.groups[0].Name != "external" || m.errMsg != errStateConflict.Error() {
		t.Fatalf("conflict did not adopt/report external: groups=%+v err=%q", m.groups, m.errMsg)
	}
}

func TestMoveDropFailureRestoresPersistedRowsAndVisibleError(t *testing.T) {
	groups := []Group{{Name: "work", Members: []string{"tmux:api", "tmux:web"}}}
	m, store := organizationTestModel(t, groups, nil, nil)
	cursorToSessionForTest(t, m, "web")
	model := pressRail(t, *m, runeKey('m'))
	model = pressRail(t, model, runeKey('k'))
	store.failNext = errors.New("disk full")
	model = pressRail(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.mode != modeNormal || model.move != nil || !reflect.DeepEqual(model.groups, groups) {
		t.Fatalf("failed drop left draft visible: mode=%v move=%+v groups=%+v", model.mode, model.move, model.groups)
	}
	if model.errMsg != "disk full" || !model.errorActive() || !strings.Contains(model.hintLine(), "disk full") {
		t.Fatalf("failed drop error not visible: err=%q hint=%q", model.errMsg, model.hintLine())
	}
}

func TestEveryOrganizationActionIsUndoable(t *testing.T) {
	base := []Group{{Name: "work", Members: []string{"tmux:api", "tmux:web"}}, {Name: "personal", Members: []string{"tmux:dots"}}}

	t.Run("group create", func(t *testing.T) {
		m, _ := organizationTestModel(t, base, nil, nil)
		if err := m.createGroup("new"); err != nil {
			t.Fatal(err)
		}
		if m.infoMsg != "created group · new · u undo" {
			t.Fatalf("create feedback = %q", m.infoMsg)
		}
		if err := m.undoOrganization(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(m.groups, base) || m.infoMsg != "undid · create new" {
			t.Fatalf("create undo = groups %+v info %q", m.groups, m.infoMsg)
		}
	})

	t.Run("group delete restores fold key", func(t *testing.T) {
		folds := map[string]bool{groupKey("work"): true}
		m, _ := organizationTestModel(t, base, folds, nil)
		cursorToGroupForTest(t, m, "work")
		if err := m.deleteGroup("work"); err != nil {
			t.Fatal(err)
		}
		if m.collapsed[groupKey("work")] {
			t.Fatal("delete retained group fold key")
		}
		if m.infoMsg != "deleted group · work · u undo" {
			t.Fatalf("delete feedback = %q", m.infoMsg)
		}
		if err := m.undoOrganization(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(m.groups, base) || !m.collapsed[groupKey("work")] {
			t.Fatalf("delete undo = groups %+v folds %v", m.groups, m.collapsed)
		}
		row := m.visible()[m.cursor]
		if !row.isGroup || row.label != "work" {
			t.Fatalf("delete undo cursor = %+v", row)
		}
	})

	t.Run("session move", func(t *testing.T) {
		m, _ := organizationTestModel(t, base, nil, nil)
		cursorToSessionForTest(t, m, "web")
		if err := m.moveRow(1); err != nil {
			t.Fatal(err)
		}
		if m.infoMsg != "moved · web · u undo" {
			t.Fatalf("move feedback = %q", m.infoMsg)
		}
		if err := m.undoOrganization(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(m.groups, base) {
			t.Fatalf("move undo = %+v", m.groups)
		}
	})

	t.Run("group reorder", func(t *testing.T) {
		m, _ := organizationTestModel(t, base, nil, nil)
		cursorToGroupForTest(t, m, "personal")
		if err := m.moveRow(-1); err != nil {
			t.Fatal(err)
		}
		if m.infoMsg != "moved · personal · u undo" {
			t.Fatalf("reorder feedback = %q", m.infoMsg)
		}
		if err := m.undoOrganization(); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(m.groups, base) {
			t.Fatalf("reorder undo = %+v", m.groups)
		}
	})

	t.Run("fold both directions", func(t *testing.T) {
		m, _ := organizationTestModel(t, base, nil, nil)
		cursorToGroupForTest(t, m, "work")
		row := m.visible()[m.cursor]
		if err := m.setFold(row, true); err != nil {
			t.Fatal(err)
		}
		if m.infoMsg != "collapsed · work · u undo" {
			t.Fatalf("collapse feedback = %q", m.infoMsg)
		}
		if err := m.undoOrganization(); err != nil {
			t.Fatal(err)
		}
		if m.collapsed[groupKey("work")] {
			t.Fatal("collapse undo stayed folded")
		}
		if err := m.setFold(row, true); err != nil {
			t.Fatal(err)
		}
		row = m.visible()[m.cursor]
		if err := m.setFold(row, false); err != nil {
			t.Fatal(err)
		}
		if err := m.undoOrganization(); err != nil {
			t.Fatal(err)
		}
		if !m.collapsed[groupKey("work")] {
			t.Fatal("expand undo did not restore folded state")
		}
	})
}

func TestUndoKeepsCurrentDirsAndAutomaticOrSettingsSavesDoNotReplaceIt(t *testing.T) {
	groups := []Group{{Name: "work", Members: []string{"tmux:api"}}}
	m, store := organizationTestModel(t, groups, nil, map[string]string{"tmux:api": "/old"})
	cursorToGroupForTest(t, m, "work")
	if err := m.setFold(m.visible()[m.cursor], true); err != nil {
		t.Fatal(err)
	}
	undo := m.organizationUndo
	m.captureDirs([]tmux.Session{{Name: "api", Path: "/new"}})
	if m.organizationUndo != undo {
		t.Fatal("automatic directory capture replaced organization undo")
	}
	if err := store.Update(func(doc *state.Document) error {
		doc.Settings = &state.Settings{RailWidth: 44}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if m.organizationUndo != undo {
		t.Fatal("settings save replaced organization undo")
	}
	if err := m.undoOrganization(); err != nil {
		t.Fatal(err)
	}
	if got := m.dirs["tmux:api"]; got != "/new" {
		t.Fatalf("undo restored stale dirs %q", got)
	}
	doc := store.Snapshot()
	if doc.Dirs["tmux:api"] != "/new" || doc.Settings == nil || doc.Settings.RailWidth != 44 {
		t.Fatalf("undo discarded non-organization state: %+v", doc)
	}
}

func TestOrganizationUndoNoRedoAndFailureRetention(t *testing.T) {
	groups := []Group{{Name: "work", Members: []string{"tmux:api"}}}
	m, store := organizationTestModel(t, groups, nil, nil)
	if err := m.createGroup("new"); err != nil {
		t.Fatal(err)
	}
	if err := m.undoOrganization(); err != nil {
		t.Fatal(err)
	}
	writes := store.writes
	if err := m.undoOrganization(); err != nil {
		t.Fatal(err)
	}
	if store.writes != writes || m.infoMsg != "nothing to undo" {
		t.Fatalf("second u created redo/write: writes=%d want=%d info=%q", store.writes, writes, m.infoMsg)
	}

	m.organizationUndo = &organizationUndo{snapshot: snapshotOrganization(groups, nil, cursorIdentity{}), action: "move api"}
	retained := m.organizationUndo
	store.failNext = errors.New("save failed")
	if err := m.undoOrganization(); err == nil {
		t.Fatal("failed undo returned nil")
	}
	if m.organizationUndo != retained {
		t.Fatal("failed undo was consumed")
	}
}

func TestDestructionAndExternalSyncInvalidateOrganizationUndo(t *testing.T) {
	groups := []Group{{Name: "work", Members: []string{"tmux:api"}}}

	t.Run("forget", func(t *testing.T) {
		m, _ := organizationTestModel(t, groups, nil, nil)
		m.organizationUndo = &organizationUndo{action: "older"}
		if err := m.forgetMember("tmux:api"); err != nil {
			t.Fatal(err)
		}
		if m.organizationUndo != nil {
			t.Fatal("successful forget retained undo")
		}
	})

	t.Run("session kill", func(t *testing.T) {
		m, _ := organizationTestModel(t, nil, nil, nil)
		m.organizationUndo = &organizationUndo{action: "older"}
		if err := m.killSession("api"); err != nil {
			t.Fatal(err)
		}
		if m.organizationUndo != nil {
			t.Fatal("successful kill retained undo")
		}
	})

	t.Run("external sync", func(t *testing.T) {
		m, store := organizationTestModel(t, groups, nil, nil)
		cursorToSessionForTest(t, m, "api")
		m.startMove()
		m.organizationUndo = &organizationUndo{action: "older"}
		if err := store.Update(func(doc *state.Document) error {
			doc.Groups = []state.Group{{Name: "external"}}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		next := Model(*m).SyncState()
		if next.organizationUndo != nil || next.mode != modeNormal || next.move != nil || len(next.groups) != 1 || next.groups[0].Name != "external" {
			t.Fatalf("SyncState retained stale organization: undo=%+v mode=%v move=%+v groups=%+v", next.organizationUndo, next.mode, next.move, next.groups)
		}
	})
}
