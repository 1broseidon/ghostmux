package rail

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

// TestUnreadPeekCapturesLazily: the count is ledger arithmetic; the text is
// fetched only when asked, from the window's active pane.
func TestUnreadPeekCapturesLazily(t *testing.T) {
	var captured []string
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) {
		captured = append(captured, strings.Join(args, " "))
		if args[0] == "capture-pane" {
			return "one\ntwo\nthree\n", nil
		}
		return "", nil
	}
	t.Cleanup(func() { tmux.Runner = orig })

	m := railModel{
		rows: []railRow{{flat: true, label: "api", sess: "api", window: "1", windowID: "@1", unread: 3}},
		unread: map[string]unreadInfo{
			"@1": {count: 3, pane: "%7", alt: false},
		},
	}
	title, lines, ok := m.UnreadPeek()
	if !ok || !strings.Contains(title, "api · +3 unseen") {
		t.Fatalf("peek = %q ok=%v", title, ok)
	}
	if len(lines) != 3 || lines[0] != "one" {
		t.Fatalf("peek lines = %v", lines)
	}
	if len(captured) != 1 || !strings.Contains(captured[0], "capture-pane -p -t %7 -S -3") {
		t.Fatalf("capture argv = %v", captured)
	}

	// A row with nothing banked peeks nothing — and captures nothing.
	captured = nil
	m.rows[0].unread = 0
	delete(m.unread, "@1")
	if _, _, ok := m.UnreadPeek(); ok || len(captured) != 0 {
		t.Fatalf("empty peek fetched anyway: ok=%v captured=%v", ok, captured)
	}
}

// TestUnreadCountRenders: banked lines render as +N beside the marks, and the
// viewed row's count is suppressed with the rest of its marks.
func TestUnreadCountRenders(t *testing.T) {
	row := railRow{flat: true, label: "api", sess: "api", window: "1", unread: 38, done: true}
	plain := ansi.Strip(renderRow(row, false, 0, ""))
	if !strings.Contains(plain, "+38") {
		t.Fatalf("unread count missing: %q", plain)
	}
	over := railRow{flat: true, label: "api", sess: "api", window: "1", unread: 5000}
	if got := ansi.Strip(renderRow(over, false, 0, "")); !strings.Contains(got, "+999") {
		t.Fatalf("cap missing: %q", got)
	}
	viewed := railRow{flat: true, label: "api", sess: "api", window: "1", unread: 12, inView: true}
	suppressViewedMarks(&viewed)
	if viewed.unread != 0 {
		t.Fatalf("viewed row kept a bank: %+v", viewed)
	}
}

// TestAgentRowShowsSparkOrAge: motion while alive, age while quiet, never both.
func TestAgentRowShowsSparkOrAge(t *testing.T) {
	alive := railRow{flat: true, label: "w", sess: "w", cmd: "claude",
		pulse: []uint8{0, 0, 1, 3, 8, 2, 1, 0}, activityAt: 1}
	plain := ansi.Strip(renderRow(alive, false, 0, ""))
	if !strings.Contains(plain, "█") {
		t.Fatalf("live agent has no spark: %q", plain)
	}
	quiet := railRow{flat: true, label: "w", sess: "w", cmd: "claude",
		pulse: []uint8{0, 0, 0, 0, 0, 0, 0, 0}, activityAt: 1}
	if got := ansi.Strip(renderRow(quiet, false, 0, "")); strings.ContainsAny(got, "▁▂▃▄▅▆▇█") {
		t.Fatalf("quiet agent shows a spark: %q", got)
	}
	plainCmd := railRow{flat: true, label: "w", sess: "w", cmd: "npm",
		pulse: []uint8{0, 0, 1, 3, 8, 2, 1, 0}}
	if got := ansi.Strip(renderRow(plainCmd, false, 0, "")); strings.ContainsAny(got, "▁▂▃▄▅▆▇█") {
		t.Fatalf("non-agent shows a spark: %q", got)
	}
}
