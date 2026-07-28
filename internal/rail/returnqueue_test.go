package rail

import (
	"strings"
	"testing"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

// returnQueueFixture is a four-session fleet with distinct activity
// timestamps: two bells, one done, one quiet. Full 11-field window rows so
// each window carries its own #{window_activity} evidence. The runner is
// stateful the way tmux is: displaying a window clears its native bell flag,
// and clearViewedDone's set-option clears @ghostmux_done — so a viewed entry
// leaves the queue exactly as it does in production.
func returnQueueFixture(t *testing.T, vp *fakeViewport) {
	t.Helper()
	type winState struct {
		id, index, activity, cmd string
		bell, done               bool
	}
	wins := map[string]*winState{
		"old":   {id: "$1", index: "1", activity: "100", cmd: "zsh", bell: true},
		"mid":   {id: "$2", index: "1", activity: "200", cmd: "zsh", done: true},
		"new":   {id: "$3", index: "1", activity: "300", cmd: "claude", bell: true},
		"quiet": {id: "$4", index: "1", activity: "50", cmd: "zsh"},
	}
	order := []string{"old", "mid", "new", "quiet"}
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) {
		// tmux clears window_bell_flag once a client displays the window.
		for _, p := range vp.points {
			if sess, _, ok := strings.Cut(p, ":"); ok {
				if w, present := wins[sess]; present {
					w.bell = false
				}
			}
		}
		switch args[0] {
		case "set-option":
			if len(args) >= 6 && args[4] == "@ghostmux_done" && args[5] == "0" {
				if sess, _, ok := strings.Cut(args[3], ":"); ok {
					if w, present := wins[sess]; present {
						w.done = false
					}
				}
			}
			return "", nil
		case "list-sessions":
			var b strings.Builder
			for _, name := range order {
				b.WriteString(name + "\t" + wins[name].id + "\t0\t/tmp\t\n")
			}
			return b.String(), nil
		case "list-windows":
			var b strings.Builder
			for _, name := range order {
				w := wins[name]
				bell, done := "0", ""
				if w.bell {
					bell = "1"
				}
				if w.done {
					done = "1"
				}
				b.WriteString(name + "\t" + w.id + "\t@" + w.id[1:] + "\t" + w.index +
					"\tzsh\t1\t" + bell + "\t0\t" + w.activity + "\t" + done + "\t\n")
			}
			return b.String(), nil
		case "list-panes":
			var b strings.Builder
			for _, name := range order {
				b.WriteString(name + "\t" + wins[name].index + "\t" + wins[name].cmd + "\n")
			}
			return b.String(), nil
		}
		return "", nil
	}
	t.Cleanup(func() { tmux.Runner = orig })
}

func returnQueueModel(t *testing.T) (*railModel, *fakeViewport) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	origPresent := tmuxPresent
	tmuxPresent = func() bool { return true }
	t.Cleanup(func() { tmuxPresent = origPresent })
	vp := &fakeViewport{}
	returnQueueFixture(t, vp)
	m := &railModel{vp: vp, done: newDoneTracker(), collapsed: map[string]bool{}}
	return m, vp
}

// TestReturnOldestDrainsAgentsFirstThenByActivityTimestamp is the Return
// Queue loop: agents outrank plain jobs (the "new" window runs claude, so it
// drains first despite being newest), then oldest → newest within the plain
// tier. Viewing removes each entry, so the queue keeps no state of its own.
func TestReturnOldestDrainsAgentsFirstThenByActivityTimestamp(t *testing.T) {
	m, vp := returnQueueModel(t)
	m.refresh()

	if bells, done := m.AttentionSummary(); bells != 2 || done != 1 {
		t.Fatalf("fixture attention = ●%d ✓%d, want ●2 ✓1", bells, done)
	}

	for i, want := range []string{"new", "old", "mid"} {
		next, _ := m.Update(key("]"))
		*m = next.(railModel)
		if got := vp.points[len(vp.points)-1]; got != want+":1" {
			t.Fatalf("press %d pointed at %q, want %q (points %v)", i+1, got, want+":1", vp.points)
		}
		if m.infoMsg != "return · "+want {
			t.Fatalf("press %d info = %q", i+1, m.infoMsg)
		}
	}
	if len(vp.points) != 3 {
		t.Fatalf("drain took %d points, want 3: %v", len(vp.points), vp.points)
	}
}

// TestReturnOldestOnEmptyQueueSaysSo: no unseen ●/✓ means ] does nothing but
// say so — it must never move the viewport as a side effect of nothing.
func TestReturnOldestOnEmptyQueueSaysSo(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	origPresent := tmuxPresent
	tmuxPresent = func() bool { return true }
	t.Cleanup(func() { tmuxPresent = origPresent })
	withFakeRunner(t, map[string]string{
		"list-sessions": "quiet\t$1\t0\t/tmp\t\n",
		"list-windows":  "quiet\t$1\t@1\t1\tzsh\t1\t0\t0\t50\t\t\n",
	})
	vp := &fakeViewport{}
	m := &railModel{vp: vp, done: newDoneTracker(), collapsed: map[string]bool{}}
	m.refresh()

	next, _ := m.Update(key("]"))
	*m = next.(railModel)
	if len(vp.points) != 0 || m.infoMsg != "queue empty" {
		t.Fatalf("empty queue: points=%v info=%q", vp.points, m.infoMsg)
	}
}

// TestReturnOldestIgnoresNonQueueRows: activity (~) is gutter-only, and
// ghosts, stale rows, and aggregates can never be inbox items — the queue
// admits exactly what AttentionSummary counts.
func TestReturnOldestIgnoresNonQueueRows(t *testing.T) {
	vp := &fakeViewport{}
	m := &railModel{
		vp: vp, collapsed: map[string]bool{},
		rows: []railRow{
			{flat: true, label: "act", sess: "act", act: true, activityAt: 10},
			{flat: true, ghost: true, label: "gone", sess: "gone", bell: true, activityAt: 20},
			{flat: true, label: "stale", sess: "stale", done: true, validity: rowStale, activityAt: 30},
			{isGroup: true, label: "work", sess: "work", bell: true},
			{label: "tree", sess: "tree", bell: true, activityAt: 40}, // aggregate session row
		},
	}
	m.returnOldest()
	if len(vp.points) != 0 || m.infoMsg != "queue empty" {
		t.Fatalf("non-queue rows admitted: points=%v info=%q", vp.points, m.infoMsg)
	}
}

// TestReturnOldestReachesIntoCollapsedGroups: a fold hides rows from the eye,
// not from the queue. Attention inside a collapsed group is still one press
// away — which is the whole reason the queue beats scanning the rail.
func TestReturnOldestReachesIntoCollapsedGroups(t *testing.T) {
	m, vp := returnQueueModel(t)
	m.groups = []Group{{Name: "work", Members: []string{"tmux:new"}}}
	m.collapsed[groupKey("work")] = true
	m.refresh()

	for _, r := range m.visible() {
		if r.sess == "new" && !r.isGroup {
			t.Fatalf("fixture broken: new is visible despite the fold")
		}
	}
	next, _ := m.Update(key("]"))
	*m = next.(railModel)
	if len(vp.points) == 0 || vp.points[0] != "new:1" {
		t.Fatalf("folded member unreachable: points=%v err=%q", vp.points, m.errMsg)
	}
}

// TestReturnOldestReportsARefusedView: if the viewport cannot render the
// target, the press must say so instead of pretending the queue drained.
func TestReturnOldestReportsARefusedView(t *testing.T) {
	m, vp := returnQueueModel(t)
	m.refresh()
	vp.pointBlocked = true

	next, _ := m.Update(key("]"))
	*m = next.(railModel)
	if !strings.Contains(m.errMsg, "view unavailable") {
		t.Fatalf("refused view reported %q", m.errMsg)
	}
}
