package rail

import (
	"strings"
	"testing"
)

func TestTruncateLabel(t *testing.T) {
	cases := []struct {
		name  string
		s     string
		width int
		want  string
	}{
		{"fits exactly", "agent-web", 9, "agent-web"},
		{"shorter than width", "vim", 10, "vim"},
		{"truncated with ellipsis", "payments-refactor-sprint", 10, "payments-…"},
		{"width zero", "anything", 0, ""},
		{"negative width", "anything", -3, ""},
		{"width one", "anything", 1, "…"},
		{"empty string", "", 5, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateLabel(tc.s, tc.width)
			if got != tc.want {
				t.Errorf("truncateLabel(%q, %d) = %q, want %q", tc.s, tc.width, got, tc.want)
			}
			if tc.width > 0 {
				if n := len([]rune(got)); n > tc.width {
					t.Errorf("truncateLabel(%q, %d) = %q (%d runes) exceeds width", tc.s, tc.width, got, n)
				}
			}
		})
	}
}

func TestScrollWindowNoOverflow(t *testing.T) {
	start, end, up, down := scrollWindow(5, 10, 2)
	if start != 0 || end != 5 || up != 0 || down != 0 {
		t.Errorf("scrollWindow(5,10,2) = %d,%d,%d,%d, want 0,5,0,0", start, end, up, down)
	}
}

func TestScrollWindowCursorStaysInView(t *testing.T) {
	// 20 rows, viewport height 5: every cursor position must land inside
	// [start,end), and the reserved indicator rows must keep the total
	// printed rows (indicators + slice) within height.
	n, height := 20, 5
	for cursor := 0; cursor < n; cursor++ {
		start, end, up, down := scrollWindow(n, height, cursor)
		if cursor < start || cursor >= end {
			t.Fatalf("cursor=%d out of window [%d,%d)", cursor, start, end)
		}
		rows := end - start
		if up > 0 {
			rows++
		}
		if down > 0 {
			rows++
		}
		if rows > height {
			t.Fatalf("cursor=%d: printed rows %d exceeds height %d (start=%d end=%d up=%d down=%d)",
				cursor, rows, height, start, end, up, down)
		}
		if up+(end-start)+down != n {
			t.Fatalf("cursor=%d: up(%d)+visible(%d)+down(%d) != n(%d)", cursor, up, end-start, down, n)
		}
	}
}

func TestScrollWindowEmpty(t *testing.T) {
	start, end, up, down := scrollWindow(0, 10, 0)
	if start != 0 || end != 0 || up != 0 || down != 0 {
		t.Errorf("scrollWindow(0,...) = %d,%d,%d,%d, want all zero", start, end, up, down)
	}
}

func TestVisibleRowsCollapse(t *testing.T) {
	rows := []railRow{
		{depth: 0, sess: "a", label: "a"},
		{depth: 1, isWin: true, sess: "a", label: "1:x"},
		{depth: 1, isWin: true, sess: "a", label: "2:y"},
		{depth: 0, sess: "b", label: "b"},
		{depth: 1, isWin: true, sess: "b", label: "1:z"},
	}
	got := visibleRows(rows, map[string]bool{"a": true})
	if len(got) != 3 {
		t.Fatalf("visibleRows collapsed 'a' = %d rows, want 3: %#v", len(got), got)
	}
	if !got[0].collapsed {
		t.Errorf("session row 'a' should be marked collapsed")
	}
	if got[1].sess != "b" || got[2].sess != "b" {
		t.Errorf("expected b's rows after collapsed a, got %#v", got)
	}
}

func TestMatchesFilter(t *testing.T) {
	sessRow := railRow{depth: 0, sess: "gm-agent-01", label: "gm-agent-01"}
	winRow := railRow{depth: 1, isWin: true, sess: "gm-agent-01", label: "1:claude"}
	other := railRow{depth: 0, sess: "dotfiles", label: "dotfiles"}

	if !matchesFilter(sessRow, "agent") {
		t.Errorf("expected session row to match 'agent'")
	}
	if !matchesFilter(sessRow, "AGENT") {
		t.Errorf("filter should be case-insensitive")
	}
	if matchesFilter(other, "agent") {
		t.Errorf("dotfiles should not match 'agent'")
	}
	if !matchesFilter(winRow, "claude") {
		t.Errorf("expected window row to match its label")
	}
	if !matchesFilter(other, "") {
		t.Errorf("empty query should match everything")
	}
}

// TestRowAtIsTheInverseOfWhatViewDraws is the regression for the off-by-two
// click: View() and rowAt() are the same layout expressed twice — once to
// draw, once to hit-test. When the title row was deleted from View but rowAt
// still skipped two lines for it, every click selected the row two above the
// one under the pointer. Rather than assert a magic offset, this walks the
// rendered frame and demands that the row rowAt() reports for a line is the
// row actually printed on that line.
func TestRowAtIsTheInverseOfWhatViewDraws(t *testing.T) {
	var rows []railRow
	for _, name := range []string{"alpha", "beta", "gamma", "delta"} {
		rows = append(rows,
			railRow{depth: 0, label: name, sess: name},
			railRow{depth: 1, isWin: true, label: "1:zsh", sess: name, window: "1"})
	}
	m := railModel{rows: rows, height: 24, collapsed: map[string]bool{}, vp: &fakeViewport{}}

	frame := strings.Split(m.View(), "\n")
	vis := m.visible()
	if len(vis) == 0 {
		t.Fatal("no visible rows to test")
	}
	checked := 0
	for y, line := range frame {
		idx, ok := m.rowAt(y)
		if !ok {
			continue
		}
		if idx < 0 || idx >= len(vis) {
			t.Fatalf("rowAt(%d) = %d, out of range for %d rows", y, idx, len(vis))
		}
		// The label of the row rowAt claims must appear on that very line.
		if want := vis[idx].label; !strings.Contains(line, want) {
			t.Errorf("rowAt(%d) = %d (%q), but line %d reads %q",
				y, idx, want, y, strings.TrimRight(line, " "))
		}
		checked++
	}
	if checked < len(vis) {
		t.Errorf("only %d of %d visible rows were hit-testable", checked, len(vis))
	}
}

// TestRowAtRejectsChromeLines: clicks on the blank separator and the hint line
// must select nothing rather than the nearest row.
func TestRowAtRejectsChromeLines(t *testing.T) {
	m := railModel{
		rows:      []railRow{{depth: 0, label: "solo", sess: "solo"}},
		height:    10,
		collapsed: map[string]bool{},
		vp:        &fakeViewport{},
	}
	if _, ok := m.rowAt(0); !ok {
		t.Errorf("first tree line should hit the first row")
	}
	for _, y := range []int{m.treeHeight(), m.treeHeight() + 1} {
		if idx, ok := m.rowAt(y); ok {
			t.Errorf("chrome line %d hit row %d, want no hit", y, idx)
		}
	}
	if _, ok := m.rowAt(-1); ok {
		t.Errorf("negative line reported a hit")
	}
}
