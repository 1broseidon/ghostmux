package rail

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/1broseidon/ghostmux/internal/state"
	"github.com/1broseidon/ghostmux/internal/tmux"
)

func navigationStore(t *testing.T) *state.Store {
	t.Helper()
	store, err := state.Open(t.TempDir() + "/groups.json")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSemanticLeftBranches(t *testing.T) {
	t.Run("expanded group collapses through persisted fold path", func(t *testing.T) {
		m := railModel{
			rows: []railRow{
				{isGroup: true, label: "work", sess: "work"},
				{depth: 1, flat: true, group: "work", label: "api", sess: "api"},
			},
			collapsed: map[string]bool{}, store: navigationStore(t),
		}
		m.semanticLeft()
		if !m.collapsed[groupKey("work")] || m.infoMsg != "collapsed · work · u undo" {
			t.Fatalf("group collapse = %v info=%q", m.collapsed, m.infoMsg)
		}
		if got := m.store.Snapshot().Collapsed; !reflect.DeepEqual(got, []string{groupKey("work")}) {
			t.Fatalf("persisted collapse = %v", got)
		}
	})

	t.Run("expanded non-flat session collapses", func(t *testing.T) {
		m := railModel{
			rows: []railRow{
				{label: "api", sess: "api"},
				{depth: 1, isWin: true, label: "1:zsh", sess: "api", window: "1"},
			},
			collapsed: map[string]bool{}, store: navigationStore(t),
		}
		m.semanticLeft()
		if !m.collapsed["api"] || m.infoMsg != "collapsed · api · u undo" {
			t.Fatalf("session collapse = %v info=%q", m.collapsed, m.infoMsg)
		}
	})

	t.Run("window selects session parent", func(t *testing.T) {
		m := railModel{rows: []railRow{
			{label: "api", sess: "api"},
			{depth: 1, isWin: true, label: "1:zsh", sess: "api", window: "1"},
		}, cursor: 1, collapsed: map[string]bool{}}
		m.semanticLeft()
		if m.cursor != 0 {
			t.Fatalf("window parent cursor = %d", m.cursor)
		}
	})

	t.Run("grouped session and ghost select group parent", func(t *testing.T) {
		for _, row := range []railRow{
			{depth: 1, flat: true, group: "work", label: "api", sess: "api"},
			{depth: 1, flat: true, ghost: true, group: "work", label: "gone", sess: "gone"},
		} {
			m := railModel{rows: []railRow{{isGroup: true, label: "work", sess: "work"}, row}, cursor: 1, collapsed: map[string]bool{}}
			m.semanticLeft()
			if m.cursor != 0 {
				t.Errorf("row %+v parent cursor = %d", row, m.cursor)
			}
		}
	})

	t.Run("collapsed grouped session selects group", func(t *testing.T) {
		m := railModel{rows: []railRow{
			{isGroup: true, label: "work", sess: "work"},
			{depth: 1, group: "work", label: "api", sess: "api"},
			{depth: 2, isWin: true, group: "work", label: "1:zsh", sess: "api", window: "1"},
		}, cursor: 1, collapsed: map[string]bool{"api": true}}
		m.semanticLeft()
		if m.cursor != 0 || !m.collapsed["api"] {
			t.Fatalf("collapsed child parent = cursor %d folds %v", m.cursor, m.collapsed)
		}
	})

	t.Run("top-level collapsed and leaf rows are inert", func(t *testing.T) {
		rows := []railRow{{label: "api", sess: "api"}, {flat: true, label: "solo", sess: "solo", window: "1"}}
		m := railModel{rows: rows, collapsed: map[string]bool{"api": true}}
		m.semanticLeft()
		if m.cursor != 0 || !m.collapsed["api"] {
			t.Fatalf("top-level collapsed row changed: cursor=%d folds=%v", m.cursor, m.collapsed)
		}
		m.cursor = 1
		m.semanticLeft()
		if m.cursor != 1 {
			t.Fatalf("top-level leaf moved to %d", m.cursor)
		}
	})

	t.Run("uncertain structural row still collapses and window still selects parent", func(t *testing.T) {
		for _, validity := range []rowValidity{rowStale, rowUnvalidated} {
			m := railModel{rows: []railRow{
				{label: "api", sess: "api", validity: validity},
				{depth: 1, isWin: true, label: "1:zsh", sess: "api", window: "1", validity: validity},
			}, collapsed: map[string]bool{}, store: navigationStore(t)}
			m.semanticLeft()
			if m.errMsg != "" || !m.collapsed["api"] {
				t.Errorf("validity %d structural h: err=%q folds=%v", validity, m.errMsg, m.collapsed)
			}
			m.collapsed = map[string]bool{}
			m.cursor = 1
			m.semanticLeft()
			if m.errMsg != "" || m.cursor != 0 {
				t.Errorf("validity %d window h: err=%q cursor=%d", validity, m.errMsg, m.cursor)
			}
		}
	})
}

func TestSemanticRightBranches(t *testing.T) {
	t.Run("collapsed group and fresh or uncertain session expand", func(t *testing.T) {
		for _, row := range []railRow{
			{isGroup: true, label: "work", sess: "work"},
			{label: "api", sess: "api"},
			{label: "stale", sess: "stale", validity: rowStale},
			{label: "unknown", sess: "unknown", validity: rowUnvalidated},
		} {
			key := foldKey(row)
			m := railModel{rows: []railRow{row}, collapsed: map[string]bool{key: true}, store: navigationStore(t)}
			m.semanticRight()
			if m.collapsed[key] || m.infoMsg != "expanded · "+row.label+" · u undo" {
				t.Errorf("expand %+v = folds %v info %q", row, m.collapsed, m.infoMsg)
			}
		}
	})

	t.Run("expanded structural row is inert", func(t *testing.T) {
		vp := &fakeViewport{}
		m := railModel{rows: []railRow{{label: "api", sess: "api"}}, collapsed: map[string]bool{}, store: navigationStore(t), vp: vp}
		m.semanticRight()
		if len(vp.points) != 0 || vp.focused || m.infoMsg != "" {
			t.Fatalf("expanded session acted: points=%v focused=%v info=%q", vp.points, vp.focused, m.infoMsg)
		}
	})

	t.Run("fresh live leaf previews exact target", func(t *testing.T) {
		vp := &fakeViewport{lock: ViewState{Sess: "other", Win: "9"}}
		row := railRow{depth: 1, isWin: true, label: "2:logs", sess: "api", window: "2"}
		m := railModel{rows: []railRow{row}, collapsed: map[string]bool{}, vp: vp}
		m.semanticRight()
		if lock := vp.Lock(); lock.Sess != "api" || lock.Win != "2" {
			t.Fatalf("preview lock = %+v", lock)
		}
		if vp.focused || m.infoMsg != "viewing api / 2:logs" {
			t.Fatalf("preview focused=%v info=%q", vp.focused, m.infoMsg)
		}
	})

	t.Run("already viewed exact leaf focuses viewport", func(t *testing.T) {
		vp := &fakeViewport{lock: ViewState{Sess: "api"}}
		row := railRow{flat: true, label: "api", sess: "api"}
		m := railModel{rows: []railRow{row}, collapsed: map[string]bool{}, vp: vp}
		m.semanticRight()
		if !vp.focused || len(vp.points) != 0 {
			t.Fatalf("already-viewed l: focused=%v points=%v", vp.focused, vp.points)
		}
	})

	t.Run("ghost never starts and uncertain row reports disabled", func(t *testing.T) {
		vp := &fakeViewport{}
		ghost := railModel{rows: []railRow{{flat: true, ghost: true, label: "gone", sess: "gone"}}, collapsed: map[string]bool{}, vp: vp}
		ghost.semanticRight()
		if len(vp.points) != 0 || ghost.errMsg != "" {
			t.Fatalf("ghost l: points=%v err=%q", vp.points, ghost.errMsg)
		}
		uncertain := railModel{rows: []railRow{{flat: true, label: "maybe", sess: "maybe", validity: rowUnvalidated}}, collapsed: map[string]bool{}, vp: vp}
		uncertain.semanticRight()
		if uncertain.errMsg != errBackendActionDisabled.Error() || len(vp.points) != 0 {
			t.Fatalf("uncertain l: points=%v err=%q", vp.points, uncertain.errMsg)
		}
	})
}

func TestRightIsUnconditionalFocusAndTabLeavesNoFoldState(t *testing.T) {
	vp := &fakeViewport{}
	m := railModel{
		rows:      []railRow{{flat: true, ghost: true, label: "gone", sess: "gone"}},
		collapsed: map[string]bool{}, store: navigationStore(t), vp: vp,
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	got := next.(railModel)
	if !vp.focused {
		t.Fatal("right did not focus viewport")
	}

	for _, row := range []railRow{
		{flat: true, label: "flat", sess: "flat", window: "1"},
		{depth: 1, isWin: true, label: "1:zsh", sess: "tree", window: "1"},
	} {
		got.rows = []railRow{row}
		got.cursor = 0
		next, _ = got.Update(tea.KeyMsg{Type: tea.KeyTab})
		got = next.(railModel)
	}
	if len(got.collapsed) != 0 || len(got.store.Snapshot().Collapsed) != 0 {
		t.Fatalf("Tab created leaf fold state: local=%v disk=%v", got.collapsed, got.store.Snapshot().Collapsed)
	}
}

func TestPhysicalPagingAndFilterBoundaries(t *testing.T) {
	rows := make([]railRow, 10)
	for i := range rows {
		rows[i] = railRow{flat: true, label: "dim", sess: "dim"}
	}
	if got := physicalMoveIndex(rows, 2, 3, ""); got != 5 {
		t.Errorf("physical +3 = %d, want 5", got)
	}
	if got := physicalMoveIndex(rows, 8, 20, ""); got != 9 {
		t.Errorf("down clamp = %d, want 9", got)
	}
	if got := physicalMoveIndex(rows, 1, -20, ""); got != 0 {
		t.Errorf("up clamp = %d, want 0", got)
	}

	for _, i := range []int{0, 3, 7} {
		rows[i].label, rows[i].sess = "keep", "keep"
	}
	if got := physicalMoveIndex(rows, 0, 2, "keep"); got != 3 {
		t.Errorf("filtered forward seek = %d, want 3", got)
	}
	if got := physicalMoveIndex(rows, 3, 20, "keep"); got != 7 {
		t.Errorf("filtered bottom clamp = %d, want 7", got)
	}
	if got := physicalMoveIndex(rows, 7, -3, "keep"); got != 3 {
		t.Errorf("filtered backward seek = %d, want 3", got)
	}
	if got := physicalMoveIndex(rows, 7, 2, "nothing"); got != 7 {
		t.Errorf("no eligible destination moved to %d", got)
	}
}

func TestPageDistances(t *testing.T) {
	rows := make([]railRow, 30)
	for i := range rows {
		rows[i] = railRow{flat: true, label: "row", sess: "row"}
	}
	for _, tc := range []struct {
		start, delta, want int
	}{
		{0, 4, 4},
		{10, -4, 6},
		{10, 8, 18},
		{10, -8, 2},
	} {
		m := railModel{rows: rows, cursor: tc.start, height: 10, collapsed: map[string]bool{}, vp: &fakeViewport{}}
		m.movePage(tc.delta)
		if m.cursor != tc.want {
			t.Errorf("movePage(%d) from %d = %d, want %d", tc.delta, tc.start, m.cursor, tc.want)
		}
	}
}

func TestAltNavigationSkipsWindowsDimmedRowsAndClamps(t *testing.T) {
	rows := []railRow{
		{isGroup: true, label: "keep-group", sess: "keep-group"},
		{depth: 1, label: "dim-session", sess: "dim-session"},
		{depth: 2, isWin: true, label: "1:keep-window", sess: "dim-session"},
		{depth: 1, flat: true, ghost: true, label: "keep-ghost", sess: "keep-ghost"},
		{flat: true, label: "keep-flat", sess: "keep-flat", validity: rowUnvalidated},
	}
	if got := nonWindowMoveIndex(rows, 0, 1, "keep"); got != 3 {
		t.Errorf("alt+j = %d, want ghost at 3", got)
	}
	if got := nonWindowMoveIndex(rows, 3, 1, "keep"); got != 4 {
		t.Errorf("alt+j uncertain target = %d, want 4", got)
	}
	if got := nonWindowMoveIndex(rows, 4, 1, "keep"); got != 4 {
		t.Errorf("alt+j wrapped/clamped to %d", got)
	}
	if got := nonWindowMoveIndex(rows, 4, -1, "keep"); got != 3 {
		t.Errorf("alt+k = %d, want 3", got)
	}
	if got := nonWindowMoveIndex(rows, 0, -1, "keep"); got != 0 {
		t.Errorf("alt+k wrapped/clamped to %d", got)
	}
}

func TestPreviousSessionToggleAndWindowHistory(t *testing.T) {
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) { return "", nil }
	t.Cleanup(func() { tmux.Runner = orig })

	vp := &fakeViewport{lock: ViewState{Sess: "one", Win: "1"}}
	m := railModel{
		vp: vp, collapsed: map[string]bool{},
		rows: []railRow{
			{flat: true, label: "one", sess: "one", window: "1"},
			{flat: true, label: "two", sess: "two", window: "1"},
		},
	}
	m.followViewport()
	vp.Point("two", "1", false)
	m.followViewport()
	if m.currentView.Sess != "two" || m.previousView.Sess != "one" {
		t.Fatalf("history = current %+v previous %+v", m.currentView, m.previousView)
	}
	if m.cursor != 1 {
		t.Fatalf("follow selected row %d, want row 1", m.cursor)
	}

	vp.points = nil
	m.viewPrevious()
	if lock := vp.Lock(); lock.Sess != "one" || lock.Win != "1" {
		t.Fatalf("first backtick lock = %+v", lock)
	}
	if !reflect.DeepEqual(vp.points, []string{"one:1"}) || m.infoMsg != "previous · one" {
		t.Fatalf("first backtick points=%v info=%q", vp.points, m.infoMsg)
	}
	m.viewPrevious()
	if lock := vp.Lock(); lock.Sess != "two" {
		t.Fatalf("second backtick did not toggle naturally: %+v", lock)
	}
	if got := vp.points[len(vp.points)-1]; got != "two:1" {
		t.Fatalf("second backtick point = %q", got)
	}

	// An inner tmux window change updates the exact current ref without turning
	// the old window into the previous session.
	vp.lock = ViewState{Sess: "tree", Win: "1"}
	m.rows = []railRow{
		{label: "tree", sess: "tree"},
		{depth: 1, isWin: true, label: "1:one", sess: "tree", window: "1"},
		{depth: 1, isWin: true, label: "2:two", sess: "tree", window: "2"},
		{flat: true, label: "other", sess: "other", window: "1"},
	}
	m.currentView, m.previousView, m.lastViewed = viewRef{}, viewRef{}, viewRef{}
	m.followViewport()
	vp.lock.Win = "2"
	m.followViewport()
	if m.currentView.Win != "2" || m.previousView.Sess != "" {
		t.Fatalf("window change polluted history: current=%+v previous=%+v", m.currentView, m.previousView)
	}
	vp.lock = ViewState{Sess: "other", Win: "1"}
	m.followViewport()
	if m.previousView != (viewRef{Sess: "tree", Win: "2"}) {
		t.Fatalf("previous exact window = %+v", m.previousView)
	}
	m.viewPrevious()
	if lock := vp.Lock(); lock.Sess != "tree" || lock.Win != "2" {
		t.Fatalf("previous exact window not restored: %+v", lock)
	}
}

func TestPreviousFailuresPreserveHistoryAndDoNotPoint(t *testing.T) {
	cases := []struct {
		name string
		row  *railRow
		want string
	}{
		{"missing", nil, "previous view missing"},
		{"ghost", &railRow{flat: true, ghost: true, label: "old", sess: "old"}, "previous session not live"},
		{"stale", &railRow{flat: true, label: "old", sess: "old", validity: rowStale}, "previous session unavailable"},
		{"unvalidated", &railRow{flat: true, label: "old", sess: "old", validity: rowUnvalidated}, "previous session unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vp := &fakeViewport{lock: ViewState{Sess: "now"}}
			m := railModel{vp: vp, currentView: viewRef{Sess: "now"}, previousView: viewRef{Sess: "old"}}
			if tc.row != nil {
				m.rows = []railRow{*tc.row}
			}
			beforeCurrent, beforePrevious := m.currentView, m.previousView
			m.viewPrevious()
			if len(vp.points) != 0 || m.currentView != beforeCurrent || m.previousView != beforePrevious || m.errMsg != tc.want {
				t.Fatalf("failure points=%v current=%+v previous=%+v err=%q", vp.points, m.currentView, m.previousView, m.errMsg)
			}
		})
	}

	t.Run("viewport refuses verified live point", func(t *testing.T) {
		vp := &fakeViewport{lock: ViewState{Sess: "now"}, pointBlocked: true}
		m := railModel{
			vp: vp, rows: []railRow{{flat: true, label: "old", sess: "old"}},
			currentView: viewRef{Sess: "now"}, previousView: viewRef{Sess: "old"},
		}
		beforeCurrent, beforePrevious := m.currentView, m.previousView
		m.viewPrevious()
		if m.errMsg != "previous view unavailable" || m.currentView != beforeCurrent || m.previousView != beforePrevious {
			t.Fatalf("failed point destroyed history: current=%+v previous=%+v err=%q", m.currentView, m.previousView, m.errMsg)
		}
	})

	t.Run("idle and detach retain history", func(t *testing.T) {
		vp := &fakeViewport{lock: ViewState{Sess: "now"}}
		m := railModel{vp: vp, currentView: viewRef{Sess: "now"}, previousView: viewRef{Sess: "old"}}
		vp.Detach()
		m.followViewport()
		if m.currentView.Sess != "now" || m.previousView.Sess != "old" {
			t.Fatalf("detach erased history: current=%+v previous=%+v", m.currentView, m.previousView)
		}
	})
}

func TestActivityIsNotAttentionLeaf(t *testing.T) {
	row := railRow{flat: true, label: "only-act", sess: "only-act", act: true}
	// Flat shape is eligible, but without ●/✓ the bar counts nothing.
	if !attentionLeaf(row) {
		t.Fatalf("flat session should be an attention-shaped leaf")
	}
	m := railModel{rows: []railRow{row}}
	if bells, done := m.AttentionSummary(); bells != 0 || done != 0 {
		t.Fatalf("activity-only summary = ●%d ✓%d, want ●0 ✓0", bells, done)
	}
}

func TestAttentionSummaryCountsLeavesNotAggregates(t *testing.T) {
	m := railModel{rows: []railRow{
		{label: "api", sess: "api", bell: true, done: true}, // aggregate: ignored
		{depth: 1, isWin: true, label: "1:one", sess: "api", window: "1", bell: true},
		{depth: 1, isWin: true, label: "2:two", sess: "api", window: "2", bell: true, done: true},
		{flat: true, label: "flat", sess: "flat", done: true},
		{flat: true, label: "act", sess: "act", act: true},
		{isGroup: true, label: "work", sess: "work", bell: true},
		{flat: true, ghost: true, label: "gone", sess: "gone", bell: true},
		{flat: true, label: "stale", sess: "stale", done: true, validity: rowStale},
	}}
	bells, done := m.AttentionSummary()
	if bells != 2 || done != 2 {
		t.Fatalf("AttentionSummary = ●%d ✓%d, want ●2 ✓2", bells, done)
	}
}

func TestInfoExpiryErrorClearingAndHintPrecedence(t *testing.T) {
	m := railModel{rows: []railRow{{flat: true, ghost: true, label: "gone", sess: "gone"}}, collapsed: map[string]bool{}}
	m.flashInfo("viewing api")
	remaining := time.Until(m.infoUntil)
	if remaining < 1900*time.Millisecond || remaining > 2100*time.Millisecond || !m.infoActive() {
		t.Fatalf("info lifetime = %v active=%v", remaining, m.infoActive())
	}
	m.flashError(errNamed("failed safely"))
	if m.infoMsg != "" || m.infoActive() {
		t.Fatalf("error did not clear info: message=%q active=%v", m.infoMsg, m.infoActive())
	}

	active := time.Now().Add(time.Minute)
	base := railModel{
		rows: []railRow{{flat: true, ghost: true, label: "gone", sess: "gone"}}, collapsed: map[string]bool{},
		errMsg: "action error", errUntil: active,
		infoMsg: "success", infoUntil: active,
		storageErr: "storage error", viewportErr: "viewport outage", viewportDead: true,
	}
	modal := base
	modal.mode = modeKillConfirm
	modal.killKind, modal.killTarget = killLive, "api"
	if got := ansi.Strip(modal.hintLine()); !strings.Contains(got, "kill api?") {
		t.Fatalf("modal did not win precedence: %q", got)
	}
	if got := ansi.Strip(base.hintLine()); !strings.Contains(got, "action error") {
		t.Fatalf("active error did not win precedence: %q", got)
	}
	base.errUntil = time.Now().Add(-time.Second)
	if got := ansi.Strip(base.hintLine()); !strings.Contains(got, "storage error") {
		t.Fatalf("storage error did not win precedence: %q", got)
	}
	base.storageErr = ""
	if got := ansi.Strip(base.hintLine()); !strings.Contains(got, "viewport outage") {
		t.Fatalf("outage did not win precedence: %q", got)
	}
	base.viewportErr = ""
	if got := ansi.Strip(base.hintLine()); !strings.Contains(got, "re-point viewport") {
		t.Fatalf("dead viewport did not win precedence: %q", got)
	}
	base.viewportDead = false
	if got := ansi.Strip(base.hintLine()); !strings.Contains(got, "success") {
		t.Fatalf("info did not precede ghost hint: %q", got)
	}
	base.infoUntil = time.Now().Add(-time.Second)
	if got := ansi.Strip(base.hintLine()); !strings.Contains(got, "start") {
		t.Fatalf("expired info did not reveal ghost hint: %q", got)
	}
	base.rows = []railRow{{flat: true, label: "live", sess: "live"}}
	if got := base.hintLine(); got != "" {
		t.Fatalf("blank fallback = %q", got)
	}
}

func TestDetachFeedbackIsExact(t *testing.T) {
	origPresent := tmuxPresent
	tmuxPresent = func() bool { return false }
	t.Cleanup(func() { tmuxPresent = origPresent })
	vp := &fakeViewport{lock: ViewState{Sess: "api"}}
	m := railModel{vp: vp, collapsed: map[string]bool{}, done: newDoneTracker()}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	got := next.(railModel)
	if !vp.detached || got.infoMsg != "viewport detached" {
		t.Fatalf("detach = detached %v message %q", vp.detached, got.infoMsg)
	}
}
