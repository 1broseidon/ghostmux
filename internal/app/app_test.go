package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/1broseidon/ghostmux/internal/rail"
	"github.com/1broseidon/ghostmux/internal/term"
	"github.com/1broseidon/ghostmux/internal/tmux"
)

// newTestViewport builds a viewport whose widget never starts a child.
func newTestViewport(t *testing.T) *ptyViewport {
	t.Helper()
	v := newPtyViewport(80, 24, nil)
	t.Cleanup(v.w.Close)
	return v
}

type fakePTYChild struct {
	running  bool
	startErr error
	starts   [][]string
	stops    int
}

func bindFakeChild(v *ptyViewport) *fakePTYChild {
	child := &fakePTYChild{}
	v.startChild = func(argv, _ []string) error {
		child.starts = append(child.starts, append([]string(nil), argv...))
		if child.startErr != nil {
			return child.startErr
		}
		child.running = true
		return nil
	}
	v.stopChild = func() {
		child.stops++
		child.running = false
	}
	v.childRunning = func() bool { return child.running }
	return child
}

type fakeViewTmux struct {
	calls           []string
	nextID          int
	targetAlive     bool
	backendErr      error
	configFailures  int
	cleanupFailures map[string]int
	alive           map[string]bool
	owned           map[string]bool
}

func (f *fakeViewTmux) run(args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "new-session":
		f.nextID++
		id := fmt.Sprintf("$%d", f.nextID)
		f.alive[id] = true
		return id + "\n", nil
	case "set-option":
		if len(args) > 3 && args[3] == "@ghostmux_view_owner" {
			f.owned[args[2]] = true
		}
		if len(args) > 3 && args[3] == "status-left" && f.configFailures > 0 {
			f.configFailures--
			return "", errors.New("configure failed")
		}
	case "if-shell":
		id := args[3]
		if f.cleanupFailures[id] > 0 {
			f.cleanupFailures[id]--
			return "", errors.New("cleanup unavailable")
		}
		if f.owned[id] {
			f.alive[id] = false
		}
	case "has-session":
		if f.backendErr != nil {
			return "", f.backendErr
		}
		if !f.targetAlive {
			return "", errors.New("no such session")
		}
	case "list-sessions":
		if f.backendErr != nil {
			return "", f.backendErr
		}
		if f.targetAlive {
			return "alpha\t$999\t0\t/tmp\t\n", nil
		}
	case "list-windows":
		if f.targetAlive {
			return "alpha\t$999\t@999\t1\tzsh\t1\t0\t0\t100\t\t\n", nil
		}
	case "list-panes":
		if f.targetAlive {
			return "alpha\t1\tzsh\n", nil
		}
	}
	return "", nil
}

func useFakeViewTmux(t *testing.T) *fakeViewTmux {
	t.Helper()
	fake := &fakeViewTmux{
		targetAlive:     true,
		cleanupFailures: make(map[string]int),
		alive:           make(map[string]bool),
		owned:           make(map[string]bool),
	}
	orig := tmux.Runner
	tmux.Runner = fake.run
	t.Cleanup(func() { tmux.Runner = orig })
	return fake
}

func cleanupForID(calls []string, id string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, "if-shell -F -t "+id+" ") {
			return true
		}
	}
	return false
}

// TestBlockIsExactlyWCols is the layout contract the whole frame rests on: if
// a single line is off by one cell, the divider zig-zags and the viewport's
// grid shears. Padding must be ANSI-aware — styled rail rows are mostly
// escape bytes.
func TestBlockIsExactlyWCols(t *testing.T) {
	styled := "\x1b[38;2;235;219;178mghostmux\x1b[0m"
	for _, in := range []string{"", "short", styled, strings.Repeat("x", 99), "日本語wide"} {
		lines := block(in+"\nsecond", 30, 4)
		if len(lines) != 4 {
			t.Fatalf("block(%q) gave %d lines, want 4", in, len(lines))
		}
		for i, ln := range lines {
			if w := ansi.StringWidth(ln); w != 30 {
				t.Errorf("block(%q) line %d width = %d, want 30 (%q)", in, i, w, ln)
			}
		}
	}
}

// TestBlockResetsStyleAtLineEnd: without the trailing reset a rail row's
// background would bleed across the divider into the viewport.
func TestBlockResetsStyleAtLineEnd(t *testing.T) {
	lines := block("\x1b[48;2;80;73;69mcursor row", 30, 1)
	if !strings.HasSuffix(lines[0], "\x1b[0m") {
		t.Errorf("block line does not end in a reset: %q", lines[0])
	}
}

// lineWidths returns the display width of each line in s.
func lineWidths(s string) []int {
	lines := strings.Split(s, "\n")
	out := make([]int, len(lines))
	for i, ln := range lines {
		out[i] = ansi.StringWidth(ln)
	}
	return out
}

// TestStatusLineFillsExactWidth: each footer chrome line is the frame's width,
// or the divider zig-zags.
func TestStatusLineFillsExactWidth(t *testing.T) {
	m := newChromeSolo(t)
	m.w, m.h = 120, 40
	for _, w := range []int{40, 80, 120, 200} {
		got := m.statusLine(w)
		for i, lw := range lineWidths(got) {
			if lw != w {
				t.Errorf("statusLine(%d) line %d width = %d, want %d", w, i, lw, w)
			}
		}
		plain := ansi.Strip(got)
		if !strings.Contains(plain, "gmx") || !strings.Contains(plain, "─") {
			t.Errorf("statusLine(%d) missing caption chrome: %q", w, plain)
		}
		if w >= 80 {
			for _, want := range []string{"move", "view"} {
				if !strings.Contains(plain, want) {
					t.Errorf("statusLine(%d) missing %q: %q", w, want, plain)
				}
			}
		}
		if lines := strings.Split(got, "\n"); len(lines) != 2 {
			t.Errorf("statusLine(%d) has %d lines, want 2 (rule + caption)", w, len(lines))
		}
	}
}

// TestStatusLineDropsKeysRatherThanTruncating: a half-rendered key hint is
// worse than an absent one, so narrow terminals shed whole pairs.
func TestStatusLineNarrowDoesNotOverflow(t *testing.T) {
	m := newSolo(newTestViewport(t))
	m.w, m.h = 120, 40
	for _, w := range []int{1, 5, 8, 12, 24, 39} {
		for i, lw := range lineWidths(m.statusLine(w)) {
			if lw > w {
				t.Errorf("statusLine(%d) line %d overflowed to %d cols", w, i, lw)
			}
		}
	}
}

func TestWideStatusLineIsCuratedCaption(t *testing.T) {
	m := newSolo(newTestViewport(t))
	m.w, m.h = 240, 40
	plain := ansi.Strip(m.statusLine(240))
	for _, want := range []string{"▎gmx", "h/l fold", "` prev", "─", "?", "keys"} {
		if !strings.Contains(plain, want) {
			t.Errorf("wide status line missing %q: %q", want, plain)
		}
	}
	for _, refuse := range []string{"organize", "undo", "kill", "]·[", "]/[ attention"} {
		if strings.Contains(plain, refuse) {
			t.Errorf("wide status line still advertises demoted key %q: %q", refuse, plain)
		}
	}
}

func TestFloatingFooterHasInsetRuleAboveCaption(t *testing.T) {
	m := newSolo(newTestViewport(t))
	m.w, m.h = 120, 40
	lines := strings.Split(ansi.Strip(m.statusLine(120)), "\n")
	if len(lines) != 2 {
		t.Fatalf("footer lines = %d, want rule + caption", len(lines))
	}
	rule, caption := lines[0], lines[1]
	if !strings.HasPrefix(rule, " ") || !strings.HasSuffix(rule, " ") {
		t.Fatalf("rule is not side-inset: %q", rule)
	}
	inner := strings.TrimSpace(rule)
	if inner == "" || strings.Trim(inner, "─") != "" {
		t.Fatalf("rule inner is not dotted: %q", rule)
	}
	if !strings.HasPrefix(caption, " ") || !strings.Contains(caption, "▎gmx") {
		t.Fatalf("caption is not side-inset wordmark: %q", caption)
	}
}

func TestAttentionClusterIsTelemetryOnly(t *testing.T) {
	frag, rw := attentionCluster(2, 1)
	plain := ansi.Strip(frag)
	if strings.Contains(plain, "]·[") || strings.Contains(plain, "]/[") {
		t.Fatalf("attention cluster still advertises a cycle chord: %q", plain)
	}
	if !strings.Contains(plain, "●2") || !strings.Contains(plain, "✓1") {
		t.Fatalf("attention cluster = %q", plain)
	}
	if got := ansi.StringWidth(frag); got != rw {
		t.Fatalf("cluster width bookkeeping = %d, display %d", rw, got)
	}
}

func TestViewportStatusLineKeepsCaptionRhythm(t *testing.T) {
	m := newSolo(newTestViewport(t))
	m.w, m.h = 160, 40
	vpBar := ansi.Strip(m.setFocus(focusViewport).statusLine(160))
	if !strings.Contains(vpBar, "▎gmx") || !strings.Contains(vpBar, "─") || !strings.Contains(vpBar, "back to rail") {
		t.Fatalf("viewport bar lost caption rhythm: %q", vpBar)
	}
	if strings.Contains(vpBar, "move") {
		t.Fatalf("viewport bar still advertises rail keys: %q", vpBar)
	}
}

// TestStatusLineFollowsFocus: with the viewport focused, every key except the
// toggle belongs to the program in it — the bar must say so rather than keep
// advertising rail keys that no longer apply.
func TestStatusLineFollowsFocus(t *testing.T) {
	m := newSolo(newTestViewport(t))
	m.w, m.h = 120, 40
	railBar := ansi.Strip(m.statusLine(120))
	vpBar := ansi.Strip(m.setFocus(focusViewport).statusLine(120))
	if !strings.Contains(railBar, "move") {
		t.Errorf("rail-focused bar lacks rail keys: %q", railBar)
	}
	if strings.Contains(vpBar, "move") {
		t.Errorf("viewport-focused bar still advertises rail keys: %q", vpBar)
	}
	if !strings.Contains(vpBar, "back to rail") {
		t.Errorf("viewport-focused bar does not name the way back: %q", vpBar)
	}
}

// TestViewportSizeLeavesRoomForRailDividerStatus pins the layout arithmetic
// the widget's pty is sized from — a wrong number here means every child
// program wraps at the wrong column.
func TestViewportSizeLeavesRoomForRailDividerStatus(t *testing.T) {
	m := soloModel{w: 120, h: 40}
	vw, bodyH := m.viewportSize()
	if vw != 120-rail.Width()-1 {
		t.Errorf("viewport width = %d, want %d", vw, 120-rail.Width()-1)
	}
	if bodyH != 38 {
		t.Errorf("viewport height = %d, want 38 (two rows for floating footer)", bodyH)
	}
	tiny := soloModel{w: 4, h: 1}
	if vw, bodyH := tiny.viewportSize(); vw < 1 || bodyH < 1 {
		t.Errorf("degenerate size produced %dx%d, want >=1x1", vw, bodyH)
	}
	cramped := soloModel{w: 80, h: 3}
	if _, bodyH := cramped.viewportSize(); bodyH != 2 {
		t.Errorf("cramped footer should be 1 row, bodyH = %d, want 2", bodyH)
	}
}

// TestHealIdlesWhenLockedSessionIsGone is solo's named improvement over
// classic: classic respawns a dead pane unconditionally, so a session killed
// from outside would be re-attached to forever, once per tick.
func TestHealIdlesWhenLockedSessionIsGone(t *testing.T) {
	var calls []string
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "has-session" {
			return "", fmt.Errorf("no such session")
		}
		return "", nil
	}
	t.Cleanup(func() { tmux.Runner = orig })

	v := newTestViewport(t)
	v.lockSess, v.lockWin = "ghost", "1"

	dead, err := v.Heal()
	if err != nil {
		t.Fatalf("Heal(): %v", err)
	}
	if !dead {
		t.Errorf("Heal() = false, want true (child is not running)")
	}
	if v.Lock().Sess != "" {
		t.Errorf("viewport should have idled, lock = %q", v.Lock().Sess)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "attach-session") || strings.HasPrefix(c, "new-session") {
			t.Errorf("heal re-attached to a dead session: %q", c)
		}
	}
}

// TestHealStaysIdleWhenDetached: `d` means stay gone. A dead child is the
// expected state, not a death to report.
func TestHealStaysIdleWhenDetached(t *testing.T) {
	v := newTestViewport(t)
	v.detached = true
	v.lockSess = "alpha"
	if dead, err := v.Heal(); dead || err != nil {
		t.Errorf("Heal() detached = (%v, %v), want (false, nil)", dead, err)
	}
	v2 := newTestViewport(t)
	if dead, err := v2.Heal(); dead || err != nil {
		t.Errorf("Heal() idle = (%v, %v), want (false, nil)", dead, err)
	}
}

// TestOnKillDropsOwnedShadow: a grouped shadow left behind keeps the killed
// target's windows alive inside its group.
func TestOnKillDropsOwnedShadow(t *testing.T) {
	var calls []string
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "", nil
	}
	t.Cleanup(func() { tmux.Runner = orig })

	v := newTestViewport(t)
	v.lockSess, v.grouped = "alpha", true
	v.view = tmux.ViewRef{
		Name: "gm-view-panel-1", SessionID: "$42", Owner: "v1:gm-view-panel-1",
	}
	v.OnKill("alpha")

	found := false
	for _, c := range calls {
		if strings.HasPrefix(c, "if-shell -F -t $42 ") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected ownership-checked cleanup by $42, got %v", calls)
	}
	if v.Lock().Sess != "" {
		t.Errorf("viewport should have idled after its lock was killed")
	}
}

func TestGroupedPanelsOnSameTargetHaveIndependentViews(t *testing.T) {
	fake := useFakeViewTmux(t)
	v1, v2 := newTestViewport(t), newTestViewport(t)
	bindFakeChild(v1)
	bindFakeChild(v2)
	v1.panelNonce, v2.panelNonce = "panel-a", "panel-b"

	v1.Point("alpha", "1", true)
	v2.Point("alpha", "1", true)
	if v1.view.Name == "" || v2.view.Name == "" || v1.view.Name == v2.view.Name {
		t.Fatalf("same-target panels shared a view: %+v %+v", v1.view, v2.view)
	}
	if v1.view.SessionID == v2.view.SessionID {
		t.Fatalf("same-target panels shared a session ID: %+v %+v", v1.view, v2.view)
	}
	for _, call := range fake.calls {
		if strings.Contains(call, " -A ") {
			t.Fatalf("grouped attach used -A: %q", call)
		}
	}
	v1.Detach()
	v2.Detach()
}

func TestOwnedViewCleanupAcrossViewportLifecycle(t *testing.T) {
	t.Run("idle and detach", func(t *testing.T) {
		fake := useFakeViewTmux(t)
		v := newTestViewport(t)
		bindFakeChild(v)
		v.panelNonce = "panel"
		v.Point("alpha", "", true)
		first := v.view.SessionID
		fake.calls = nil
		v.Idle()
		if !cleanupForID(fake.calls, first) || v.Lock().Sess != "" {
			t.Fatalf("idle did not clean %s and unlock: calls=%v lock=%+v", first, fake.calls, v.Lock())
		}

		v.Point("alpha", "", true)
		second := v.view.SessionID
		fake.calls = nil
		v.Detach()
		if !cleanupForID(fake.calls, second) || !v.detached {
			t.Fatalf("detach did not clean %s: calls=%v detached=%v", second, fake.calls, v.detached)
		}
	})

	t.Run("repoint", func(t *testing.T) {
		fake := useFakeViewTmux(t)
		v := newTestViewport(t)
		bindFakeChild(v)
		v.panelNonce = "panel"
		v.Point("alpha", "", true)
		first := v.view
		fake.calls = nil
		v.Point("alpha", "", true)
		if !cleanupForID(fake.calls, first.SessionID) {
			t.Fatalf("repoint did not clean old view %s: %v", first.SessionID, fake.calls)
		}
		if v.view.Name == first.Name || v.view.SessionID == first.SessionID {
			t.Fatalf("repoint reused its shadow: old=%+v new=%+v", first, v.view)
		}
		v.Detach()
	})

	t.Run("target kill", func(t *testing.T) {
		fake := useFakeViewTmux(t)
		v := newTestViewport(t)
		bindFakeChild(v)
		v.Point("alpha", "", true)
		id := v.view.SessionID
		fake.calls = nil
		v.OnKill("alpha")
		if !cleanupForID(fake.calls, id) || v.Lock().Sess != "" {
			t.Fatalf("target kill cleanup = %v lock=%+v", fake.calls, v.Lock())
		}
	})

	t.Run("child exit retains lock and heal replaces", func(t *testing.T) {
		fake := useFakeViewTmux(t)
		v := newTestViewport(t)
		child := bindFakeChild(v)
		v.panelNonce = "panel"
		v.Point("alpha", "2", true)
		first := v.view
		child.running = false // the term.ExitMsg state
		fake.calls = nil
		v.ChildExited()
		if cleanupForID(fake.calls, first.SessionID) || v.view != first {
			t.Fatalf("child exit changed owned view before validation: ref=%+v calls=%v", v.view, fake.calls)
		}
		if lock := v.Lock(); lock.Sess != "alpha" || lock.Win != "2" {
			t.Fatalf("child exit dropped logical lock: %+v", lock)
		}
		dead, err := v.Heal()
		if err != nil || !dead || v.view.Name == "" || v.view.Name == first.Name ||
			!cleanupForID(fake.calls, first.SessionID) {
			t.Fatalf("heal did not clean and replace: dead=%v err=%v old=%+v new=%+v calls=%v", dead, err, first, v.view, fake.calls)
		}
		v.Detach()
	})

	t.Run("failed PTY start", func(t *testing.T) {
		fake := useFakeViewTmux(t)
		v := newTestViewport(t)
		child := bindFakeChild(v)
		child.startErr = errors.New("pty failed")
		v.Point("alpha", "", true)
		if fake.nextID != 1 || !cleanupForID(fake.calls, "$1") {
			t.Fatalf("failed start did not clean tagged candidate: %v", fake.calls)
		}
		if v.view != (tmux.ViewRef{}) || v.Lock().Sess != "" {
			t.Fatalf("failed start published a view/lock: ref=%+v lock=%+v", v.view, v.Lock())
		}
	})

	t.Run("panel close", func(t *testing.T) {
		fake := useFakeViewTmux(t)
		v := newTestViewport(t)
		bindFakeChild(v)
		v.Point("alpha", "", true)
		id := v.view.SessionID
		fake.calls = nil
		v.Close()
		if !cleanupForID(fake.calls, id) || v.Lock().Sess != "" {
			t.Fatalf("panel close cleanup = %v lock=%+v", fake.calls, v.Lock())
		}
		v.Close() // idempotent
	})
}

func TestCleanupFailureIsRetainedSurfacedAndRetried(t *testing.T) {
	fake := useFakeViewTmux(t)
	v := newTestViewport(t)
	bindFakeChild(v)
	v.Point("alpha", "", true)
	ref := v.view
	fake.cleanupFailures[ref.SessionID] = 2

	v.Idle()
	if v.view != (tmux.ViewRef{}) || len(v.pendingRetirements) != 1 ||
		v.pendingRetirements[0].ref != ref || !fake.alive[ref.SessionID] {
		t.Fatalf("failed cleanup capability was discarded: current=%+v pending=%+v alive=%v", v.view, v.pendingRetirements, fake.alive)
	}
	dead, err := v.Heal()
	if dead || err == nil || !strings.Contains(err.Error(), "retire owned tmux view") || len(v.pendingRetirements) != 1 {
		t.Fatalf("persistent cleanup failure was not surfaced: dead=%v err=%v pending=%+v", dead, err, v.pendingRetirements)
	}
	dead, err = v.Heal()
	if dead || err != nil || len(v.pendingRetirements) != 0 || fake.alive[ref.SessionID] {
		t.Fatalf("later heal did not retire retained capability: dead=%v err=%v pending=%+v alive=%v", dead, err, v.pendingRetirements, fake.alive)
	}
}

func TestMultipleFailedRetirementsSurviveNewCurrentView(t *testing.T) {
	fake := useFakeViewTmux(t)
	v := newTestViewport(t)
	bindFakeChild(v)
	v.panelNonce = "panel"

	v.Point("alpha", "", true)
	first := v.view
	fake.cleanupFailures[first.SessionID] = 3
	v.Point("alpha", "", true)
	second := v.view
	fake.cleanupFailures[second.SessionID] = 2
	v.Point("alpha", "", true)
	third := v.view

	if len(v.pendingRetirements) != 2 || v.pendingRetirements[0].ref != first ||
		v.pendingRetirements[1].ref != second || third == (tmux.ViewRef{}) ||
		third == first || third == second {
		t.Fatalf("new current overwrote pending capabilities: first=%+v second=%+v third=%+v pending=%+v", first, second, third, v.pendingRetirements)
	}
	fake.cleanupFailures[first.SessionID] = 0
	fake.cleanupFailures[second.SessionID] = 0
	dead, err := v.Heal()
	if dead || err != nil || len(v.pendingRetirements) != 0 || v.view != third ||
		fake.alive[first.SessionID] || fake.alive[second.SessionID] || !fake.alive[third.SessionID] {
		t.Fatalf("heal did not retire all old capabilities while preserving current: dead=%v err=%v current=%+v pending=%+v alive=%v", dead, err, v.view, v.pendingRetirements, fake.alive)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTaggedConfigurationAndStartFailuresUseRetainedCleanup(t *testing.T) {
	t.Run("configuration failure", func(t *testing.T) {
		fake := useFakeViewTmux(t)
		v := newTestViewport(t)
		bindFakeChild(v)
		fake.configFailures = 1
		fake.cleanupFailures["$1"] = 1

		v.Point("alpha", "", true)
		if v.view != (tmux.ViewRef{}) || len(v.pendingRetirements) != 1 ||
			v.pendingRetirements[0].ref.SessionID != "$1" || !fake.owned["$1"] {
			t.Fatalf("tagged configuration failure was not retained: current=%+v pending=%+v owned=%v calls=%v", v.view, v.pendingRetirements, fake.owned, fake.calls)
		}
		if _, err := v.Heal(); err != nil || len(v.pendingRetirements) != 0 || fake.alive["$1"] {
			t.Fatalf("configuration-failure cleanup did not retry: err=%v pending=%+v alive=%v", err, v.pendingRetirements, fake.alive)
		}
	})

	t.Run("PTY start failure", func(t *testing.T) {
		fake := useFakeViewTmux(t)
		v := newTestViewport(t)
		child := bindFakeChild(v)
		child.startErr = errors.New("pty failed")
		fake.cleanupFailures["$1"] = 1

		v.Point("alpha", "", true)
		if v.view != (tmux.ViewRef{}) || len(v.pendingRetirements) != 1 ||
			v.pendingRetirements[0].ref.SessionID != "$1" || !fake.alive["$1"] {
			t.Fatalf("tagged PTY-start failure was not retained: current=%+v pending=%+v alive=%v", v.view, v.pendingRetirements, fake.alive)
		}
		if _, err := v.Heal(); err != nil || len(v.pendingRetirements) != 0 || fake.alive["$1"] {
			t.Fatalf("PTY-start cleanup did not retry: err=%v pending=%+v alive=%v", err, v.pendingRetirements, fake.alive)
		}
	})
}

func TestRetirementOwnershipMismatchClearsWithoutKill(t *testing.T) {
	fake := useFakeViewTmux(t)
	v := newTestViewport(t)
	bindFakeChild(v)
	v.Point("alpha", "", true)
	ref := v.view
	fake.cleanupFailures[ref.SessionID] = 1
	v.Idle()
	if len(v.pendingRetirements) != 1 {
		t.Fatalf("cleanup failure was not retained: %+v", v.pendingRetirements)
	}

	// Simulate the exact session-ID context being retagged before retry. The
	// conditional command succeeds via its false branch and must be forgotten,
	// while the now-unowned session remains alive.
	fake.owned[ref.SessionID] = false
	if _, err := v.Heal(); err != nil || len(v.pendingRetirements) != 0 || !fake.alive[ref.SessionID] {
		t.Fatalf("ownership mismatch was killed or retained: err=%v pending=%+v alive=%v", err, v.pendingRetirements, fake.alive)
	}
	for _, call := range fake.calls {
		if strings.HasPrefix(call, "kill-session ") {
			t.Fatalf("retirement issued an unconditional kill: %v", fake.calls)
		}
	}
}

func TestPanelCloseRetriesAllCapabilitiesBestEffort(t *testing.T) {
	fake := useFakeViewTmux(t)
	v := newTestViewport(t)
	bindFakeChild(v)
	v.Point("alpha", "", true)
	first := v.view
	fake.cleanupFailures[first.SessionID] = 3
	v.Point("alpha", "", true)
	second := v.view
	fake.cleanupFailures[second.SessionID] = 1
	fake.calls = nil

	err := v.Close()
	if err == nil || len(v.pendingRetirements) != 2 || v.view != (tmux.ViewRef{}) ||
		!cleanupForID(fake.calls, first.SessionID) || !cleanupForID(fake.calls, second.SessionID) {
		t.Fatalf("close did not retain/report and attempt every capability: err=%v current=%+v pending=%+v calls=%v", err, v.view, v.pendingRetirements, fake.calls)
	}
	fake.cleanupFailures[first.SessionID] = 0
	fake.calls = nil
	if err := v.Close(); err != nil || len(v.pendingRetirements) != 0 ||
		fake.alive[first.SessionID] || fake.alive[second.SessionID] {
		t.Fatalf("repeated close did not finish cleanup: err=%v pending=%+v alive=%v calls=%v", err, v.pendingRetirements, fake.alive, fake.calls)
	}
}

func TestHealValidatesOriginalTargetWhileGroupedChildRuns(t *testing.T) {
	fake := useFakeViewTmux(t)
	v := newTestViewport(t)
	child := bindFakeChild(v)
	v.Point("alpha", "", true)
	id := v.view.SessionID
	if !child.running {
		t.Fatal("fake grouped child is not running")
	}

	fake.targetAlive = false
	fake.calls = nil
	dead, err := v.Heal()
	if err != nil || !dead {
		t.Fatalf("Heal did not report externally killed original target: dead=%v err=%v", dead, err)
	}
	if child.running || v.Lock().Sess != "" || !cleanupForID(fake.calls, id) {
		t.Fatalf("running shadow kept dead target alive: running=%v lock=%+v calls=%v", child.running, v.Lock(), fake.calls)
	}
}

func TestHealRetainsOwnedShadowAndLockWhenBackendStateIsUnknown(t *testing.T) {
	fake := useFakeViewTmux(t)
	v := newTestViewport(t)
	child := bindFakeChild(v)
	v.Point("alpha", "2", true)
	ref := v.view
	starts := len(child.starts)

	fake.backendErr = errors.New("transport down")
	fake.calls = nil
	dead, err := v.Heal()
	if dead || err == nil || !strings.Contains(err.Error(), "tmux unavailable") {
		t.Fatalf("running unknown Heal = (%v, %v)", dead, err)
	}
	if !child.running || v.view != ref || v.Lock().Sess != "alpha" || cleanupForID(fake.calls, ref.SessionID) {
		t.Fatalf("running unknown changed viewport: running=%v ref=%+v lock=%+v calls=%v", child.running, v.view, v.Lock(), fake.calls)
	}

	child.running = false
	v.ChildExited()
	fake.calls = nil
	dead, err = v.Heal()
	if dead || err == nil || len(child.starts) != starts || v.view != ref ||
		v.Lock().Sess != "alpha" || cleanupForID(fake.calls, ref.SessionID) {
		t.Fatalf("dead unknown changed/restarted viewport: dead=%v err=%v starts=%d ref=%+v lock=%+v calls=%v", dead, err, len(child.starts), v.view, v.Lock(), fake.calls)
	}

	// The same retained state is cleaned only after a later probe
	// authoritatively proves the original target absent.
	fake.backendErr = nil
	fake.targetAlive = false
	fake.calls = nil
	dead, err = v.Heal()
	if err != nil || !dead || v.Lock().Sess != "" || v.view != (tmux.ViewRef{}) ||
		!cleanupForID(fake.calls, ref.SessionID) {
		t.Fatalf("authoritative absence did not idle/clean: dead=%v err=%v ref=%+v lock=%+v calls=%v", dead, err, v.view, v.Lock(), fake.calls)
	}
}

func TestTermExitMsgRetainsViewUntilTypedHeal(t *testing.T) {
	fake := useFakeViewTmux(t)
	v := newTestViewport(t)
	child := bindFakeChild(v)
	v.Point("alpha", "", true)
	id := v.view.SessionID
	child.running = false
	fake.calls = nil

	m := soloModel{vp: v}
	next, _ := m.Update(term.ExitMsg{})
	got := next.(soloModel)
	if cleanupForID(fake.calls, id) || got.vp.view.SessionID != id {
		t.Fatalf("term.ExitMsg changed owned view before typed validation: ref=%+v calls=%v", got.vp.view, fake.calls)
	}
	if got.vp.Lock().Sess != "alpha" {
		t.Fatalf("term.ExitMsg dropped logical target lock: %+v", got.vp.Lock())
	}
	got.vp.Detach()
}

// TestFocusRequestIsConsumedOnce: the rail's l/→ key sets a flag the frame
// must act on exactly once, or focus would snap back to the viewport on every
// later keystroke.
func TestFocusRequestIsConsumedOnce(t *testing.T) {
	v := newTestViewport(t)
	if v.takeFocusRequest() {
		t.Errorf("fresh viewport reported a focus request")
	}
	v.FocusViewport()
	if !v.takeFocusRequest() {
		t.Errorf("focus request not delivered")
	}
	if v.takeFocusRequest() {
		t.Errorf("focus request delivered twice")
	}
}

// TestToggleKeySwitchesFocusFromEitherSide: the toggle is the frame's entire
// reserved key surface — it must work in BOTH directions, in-process, so no
// key is ever stolen from the program running in the viewport.
func TestToggleKeySwitchesFocusFromEitherSide(t *testing.T) {
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) { return "", nil }
	t.Cleanup(func() { tmux.Runner = orig })

	m := newSolo(newTestViewport(t))
	if m.focus != focusRail {
		t.Fatalf("solo should start rail-focused")
	}
	toggle := tea.KeyMsg{Type: tea.KeyCtrlBackslash, Alt: true}
	if !m.toggles[toggle.String()] {
		t.Fatalf("toggle key name drift: %q not in %v", toggle.String(), m.toggles)
	}

	next, _ := m.Update(toggle)
	m = next.(soloModel)
	if m.focus != focusViewport {
		t.Errorf("toggle did not move focus to the viewport")
	}
	next, _ = m.Update(toggle)
	m = next.(soloModel)
	if m.focus != focusRail {
		t.Errorf("toggle did not move focus back to the rail")
	}
}

// TestEveryDefaultToggleWorksFromEitherSide pins the default chord's
// bubbletea spelling: the settings pane, the env var, and this list must all
// agree on "alt+ctrl+\" or a rebind round-trip would not match itself.
func TestEveryDefaultToggleWorksFromEitherSide(t *testing.T) {
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) { return "", nil }
	t.Cleanup(func() { tmux.Runner = orig })

	for _, key := range []tea.KeyMsg{{Type: tea.KeyCtrlBackslash, Alt: true}} {
		m := newSolo(newTestViewport(t))
		if !m.toggles[key.String()] {
			t.Fatalf("%q is not an accepted default toggle", key.String())
		}
		next, _ := m.Update(key)
		if next.(soloModel).focus != focusViewport {
			t.Errorf("%q did not move focus to the viewport", key.String())
		}
		next, _ = next.(soloModel).Update(key)
		if next.(soloModel).focus != focusRail {
			t.Errorf("%q did not move focus back to the rail", key.String())
		}
	}
}

// TestToggleKeysHonorsEnvList: GHOSTMUX_TOGGLE replaces the defaults, so a
// user whose desktop eats both can name their own — with no config file.
func TestToggleKeysHonorsEnvList(t *testing.T) {
	t.Setenv("GHOSTMUX_TOGGLE", " ctrl+] , f9 ")
	got := toggleKeys()
	if len(got) != 2 || got[0] != "ctrl+]" || got[1] != "f9" {
		t.Errorf("toggleKeys() = %v, want [ctrl+] f9]", got)
	}
	t.Setenv("GHOSTMUX_TOGGLE", " , , ")
	if got := toggleKeys(); len(got) != len(defaultToggles) {
		t.Errorf("empty env should fall back to defaults, got %v", got)
	}
}

// TestViewportFocusedKeysDoNotReachRail is the anti-key-stealing guarantee:
// with the viewport focused, `q` must reach the child, not quit ghostmux.
func TestViewportFocusedKeysDoNotReachRail(t *testing.T) {
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) { return "", nil }
	t.Cleanup(func() { tmux.Runner = orig })

	m := newSolo(newTestViewport(t))
	m = m.setFocus(focusViewport)
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
		{Type: tea.KeyCtrlC},
		{Type: tea.KeyRunes, Runes: []rune{'/'}},
		{Type: tea.KeyRunes, Runes: []rune{'h'}},
		{Type: tea.KeyRunes, Runes: []rune{'l'}},
		{Type: tea.KeyRunes, Runes: []rune{'`'}},
		{Type: tea.KeyRunes, Runes: []rune{']'}},
		{Type: tea.KeyRunes, Runes: []rune{'['}},
		{Type: tea.KeyCtrlD},
		{Type: tea.KeyCtrlU},
		{Type: tea.KeyPgDown},
		{Type: tea.KeyPgUp},
		{Type: tea.KeyRunes, Runes: []rune{'j'}, Alt: true},
		{Type: tea.KeyRunes, Runes: []rune{'k'}, Alt: true},
	} {
		next, cmd := m.Update(k)
		if cmd != nil {
			t.Errorf("key %q produced a frame-level command while the viewport had focus", k.String())
		}
		if next.(soloModel).focus != focusViewport {
			t.Errorf("key %q escaped viewport focus", k.String())
		}
	}
}

// TestWindowSizeGivesRailItsFixedWidth: the rail is laid out by the frame in
// solo, so it must be told 30 columns regardless of the terminal's size.
func TestWindowSizeGivesRailItsFixedWidth(t *testing.T) {
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) { return "", nil }
	t.Cleanup(func() { tmux.Runner = orig })

	m := newSolo(newTestViewport(t))
	next, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	m = next.(soloModel)
	if m.w != 200 || m.h != 50 {
		t.Errorf("frame size not recorded: %dx%d", m.w, m.h)
	}
	view := m.View()
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) != 50 {
		t.Fatalf("View() produced %d lines, want 50", len(lines))
	}
	for i, ln := range lines {
		if w := ansi.StringWidth(ln); w != 200 {
			t.Errorf("View() line %d width = %d, want 200", i, w)
		}
	}
}

// TestIdleViewIsTheSharedPlaceholder: classic renders it via a `rail idle`
// subprocess, solo in-process — the content must be the same one source.
func TestIdleViewIsTheSharedPlaceholder(t *testing.T) {
	got := ansi.Strip(idleView(60, 20))
	for _, ln := range rail.IdleLines() {
		if !strings.Contains(got, ln.Text) {
			t.Errorf("idleView missing %q", ln.Text)
		}
	}
}

// TestPointRemoteRunsSSHAndHealIdlesOnExit (PROTOTYPE): a reach summon runs
// ssh as the viewport child with the lock recording the reach name; a
// finished ssh — detach or dropped link — idles instead of blindly
// reconnecting, and a later local Point clears remote state.
func TestPointRemoteRunsSSHAndHealIdlesOnExit(t *testing.T) {
	v := newTestViewport(t)
	child := bindFakeChild(v)

	v.PointRemote("beastie", "gd@beastie", "work")
	if len(child.starts) != 1 {
		t.Fatalf("PointRemote did not start a child: %+v", child.starts)
	}
	want := []string{"ssh", "-t", "gd@beastie", "--", "tmux", "new-session", "-A", "-s", "work"}
	got := child.starts[0]
	if len(got) != len(want) {
		t.Fatalf("ssh argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ssh argv = %v, want %v", got, want)
		}
	}
	if lock := v.Lock(); lock.Sess != "beastie" || lock.Win != "" {
		t.Fatalf("remote lock = %+v", lock)
	}

	// While the ssh child runs, heal leaves it alone.
	if dead, err := v.Heal(); dead || err != nil {
		t.Fatalf("running remote heal = (%v, %v)", dead, err)
	}
	// A finished ssh idles; no local probe, no reconnect loop.
	child.running = false
	dead, err := v.Heal()
	if err != nil || !dead {
		t.Fatalf("dead remote heal = (%v, %v), want (true, nil)", dead, err)
	}
	if v.Lock().Sess != "" || v.remote || len(child.starts) != 1 {
		t.Fatalf("dead remote did not idle cleanly: lock=%+v remote=%v starts=%d", v.Lock(), v.remote, len(child.starts))
	}
}
