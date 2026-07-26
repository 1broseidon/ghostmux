package app

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/1broseidon/ghostmux/internal/rail"
	"github.com/1broseidon/ghostmux/internal/tmux"
)

// newTestViewport builds a viewport whose widget never starts a child.
func newTestViewport() *ptyViewport { return newPtyViewport(80, 24, nil) }

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

// TestStatusLineFillsExactWidth: the bar is the frame's last row, so a cell
// off here shifts the whole layout.
func TestStatusLineFillsExactWidth(t *testing.T) {
	m := newSolo(newTestViewport())
	for _, w := range []int{40, 80, 120, 200} {
		got := m.statusLine(w)
		if lw := ansi.StringWidth(got); lw != w {
			t.Errorf("statusLine(%d) width = %d, want %d", w, lw, w)
		}
		plain := ansi.Strip(got)
		for _, want := range []string{"gmx", "move", "view"} {
			if !strings.Contains(plain, want) {
				t.Errorf("statusLine(%d) missing %q: %q", w, want, plain)
			}
		}
	}
}

// TestStatusLineDropsKeysRatherThanTruncating: a half-rendered key hint is
// worse than an absent one, so narrow terminals shed whole pairs.
func TestStatusLineNarrowDoesNotOverflow(t *testing.T) {
	m := newSolo(newTestViewport())
	for _, w := range []int{1, 5, 8, 12, 24, 39} {
		if lw := ansi.StringWidth(m.statusLine(w)); lw > w {
			t.Errorf("statusLine(%d) overflowed to %d cols", w, lw)
		}
	}
}

// TestStatusLineFollowsFocus: with the viewport focused, every key except the
// toggle belongs to the program in it — the bar must say so rather than keep
// advertising rail keys that no longer apply.
func TestStatusLineFollowsFocus(t *testing.T) {
	m := newSolo(newTestViewport())
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
	if bodyH != 39 {
		t.Errorf("viewport height = %d, want 39 (one row for status)", bodyH)
	}
	tiny := soloModel{w: 4, h: 1}
	if vw, bodyH := tiny.viewportSize(); vw < 1 || bodyH < 1 {
		t.Errorf("degenerate size produced %dx%d, want >=1x1", vw, bodyH)
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

	v := newTestViewport()
	v.lockSess, v.lockWin = "ghost", "1"

	if dead := v.Heal(); !dead {
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
	v := newTestViewport()
	v.detached = true
	v.lockSess = "alpha"
	if v.Heal() {
		t.Errorf("Heal() reported a death while detached")
	}
	v2 := newTestViewport()
	if v2.Heal() {
		t.Errorf("Heal() reported a death while idle with no lock")
	}
}

// TestOnKillDropsGroupedShadow: a gm-view-* shadow left behind keeps the
// killed session's windows alive inside its group.
func TestOnKillDropsGroupedShadow(t *testing.T) {
	var calls []string
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "", nil
	}
	t.Cleanup(func() { tmux.Runner = orig })

	v := newTestViewport()
	v.lockSess, v.grouped = "alpha", true
	v.OnKill("alpha", "")

	want := "kill-session -t =" + rail.GroupedName("alpha")
	found := false
	for _, c := range calls {
		if c == want {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %q, got %v", want, calls)
	}
	if v.Lock().Sess != "" {
		t.Errorf("viewport should have idled after its lock was killed")
	}
}

// TestFocusRequestIsConsumedOnce: the rail's l/→ key sets a flag the frame
// must act on exactly once, or focus would snap back to the viewport on every
// later keystroke.
func TestFocusRequestIsConsumedOnce(t *testing.T) {
	v := newTestViewport()
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

	m := newSolo(newTestViewport())
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
		m := newSolo(newTestViewport())
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

	m := newSolo(newTestViewport())
	m = m.setFocus(focusViewport)
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
		{Type: tea.KeyCtrlC},
		{Type: tea.KeyRunes, Runes: []rune{'/'}},
	} {
		if _, cmd := m.Update(k); cmd != nil {
			t.Errorf("key %q produced a frame-level command while the viewport had focus", k.String())
		}
	}
}

// TestWindowSizeGivesRailItsFixedWidth: the rail is laid out by the frame in
// solo, so it must be told 30 columns regardless of the terminal's size.
func TestWindowSizeGivesRailItsFixedWidth(t *testing.T) {
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) { return "", nil }
	t.Cleanup(func() { tmux.Runner = orig })

	m := newSolo(newTestViewport())
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
