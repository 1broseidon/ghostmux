package rail

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func treeIndentFixture() []railRow {
	return []railRow{
		{depth: 0, isGroup: true, label: "work", sess: "work"},
		{depth: 1, group: "work", label: "multi", sess: "multi"},
		{depth: 2, isWin: true, group: "work", label: "0:edit", sess: "multi", window: "0"},
		{depth: 2, isWin: true, group: "work", label: "1:test", sess: "multi", window: "1"},
		{depth: 1, flat: true, group: "work", label: "flat", sess: "flat"},
		{depth: 1, collapsed: true, group: "work", label: "closed", sess: "closed"},
		{depth: 1, group: "work", label: "final", sess: "final"},
		{depth: 2, isWin: true, group: "work", label: "0:shell", sess: "final", window: "0"},
		{depth: 2, isWin: true, group: "work", label: "1:logs", sess: "final", window: "1"},
		{depth: 0, label: "loose", sess: "loose"},
		{depth: 1, isWin: true, label: "0:one", sess: "loose", window: "0"},
		{depth: 1, isWin: true, label: "1:two", sess: "loose", window: "1"},
		{depth: 0, flat: true, label: "single", sess: "single"},
		{depth: 0, collapsed: true, label: "folded", sess: "folded"},
	}
}

func strippedTreeRows(rows []railRow, cursor int, filter string) []string {
	out := make([]string, len(rows))
	for i, row := range rows {
		out[i] = ansi.Strip(renderRow(row, i == cursor, 0, filter))
	}
	return out
}

func prefixBeforeLabel(t *testing.T, rendered string, row railRow) string {
	t.Helper()
	at := strings.Index(rendered, row.label)
	if at < 0 {
		t.Fatalf("rendered row %q does not contain label %q", rendered, row.label)
	}
	return rendered[:at]
}

func TestTreePrefixesPreserveEveryLabelColumn(t *testing.T) {
	rows := treeIndentFixture()
	want := []string{
		" ▾ ",
		"   ▾ ",
		"       ",
		"       ",
		"     ",
		"   ▸ ",
		"   ▾ ",
		"       ",
		"       ",
		" ▾ ",
		"     ",
		"     ",
		"   ",
		" ▸ ",
	}
	gotRows := strippedTreeRows(rows, -1, "")
	for i, row := range rows {
		got := prefixBeforeLabel(t, gotRows[i], row)
		if got != want[i] {
			t.Errorf("row %d (%s) prefix = %q, want %q", i, row.label, got, want[i])
		}
		wantWidth := 3
		if row.isWin || row.group != "" {
			wantWidth = 5
		}
		if row.isWin && row.group != "" {
			wantWidth = 7
		}
		if width := ansi.StringWidth(got); width != wantWidth {
			t.Errorf("row %d (%s) label starts at column %d, want %d", i, row.label, width, wantWidth)
		}
	}
}

func TestRenderRowIndentPrefixes(t *testing.T) {
	cases := []struct {
		name string
		row  railRow
		want string
	}{
		{"grouped expanded session", railRow{depth: 1, group: "g", sess: "session", label: "session"}, "   ▾ "},
		{"grouped flat session", railRow{depth: 1, group: "g", flat: true, sess: "flat", label: "flat"}, "     "},
		{"grouped window", railRow{depth: 2, isWin: true, group: "g", sess: "session", label: "0:win"}, "       "},
		{"ungrouped window", railRow{depth: 1, isWin: true, sess: "session", label: "0:win"}, "     "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := prefixBeforeLabel(t, ansi.Strip(renderRow(tc.row, false, 0, "")), tc.row)
			if got != tc.want {
				t.Errorf("prefix = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSelectedEdgeReplacesMarginWithoutInsertion(t *testing.T) {
	oldWidth := Width()
	t.Cleanup(func() { SetWidth(oldWidth) })
	SetWidth(30)

	rows := treeIndentFixture()
	plain := strippedTreeRows(rows, -1, "")
	for cursor, row := range rows {
		selected := strippedTreeRows(rows, cursor, "")[cursor]
		plainPrefix := prefixBeforeLabel(t, plain[cursor], row)
		selectedPrefix := prefixBeforeLabel(t, selected, row)
		wantPrefix := "▎" + strings.TrimPrefix(plainPrefix, " ")
		if selectedPrefix != wantPrefix {
			t.Errorf("row %d selected prefix = %q, want margin replacement %q", cursor, selectedPrefix, wantPrefix)
		}
		if got, want := ansi.StringWidth(selectedPrefix), ansi.StringWidth(plainPrefix); got != want {
			t.Errorf("row %d selected prefix width = %d, want unchanged %d", cursor, got, want)
		}
		if got := ansi.StringWidth(selected); got != railWidth {
			t.Errorf("row %d selected width = %d, want %d", cursor, got, railWidth)
		}
	}

	glyph, fg := treeEdge(true)
	if glyph != "▎" || fg != hexTitleAccent || hexTitleAccent != "#fe8019" {
		t.Errorf("selected edge = (%q, %q), want one orange focus cell", glyph, fg)
	}
	glyph, fg = treeEdge(false)
	if glyph != " " || fg != hexCursorBg {
		t.Errorf("unselected edge = (%q, %q), want unchanged blank margin", glyph, fg)
	}

	body := ansi.Strip(treeBody(rows, 6, 0, "", len(rows)))
	if got := strings.Count(body, "▎"); got != 1 {
		t.Errorf("tree has %d focus edges, want exactly one: %q", got, body)
	}
}

func TestTreeRowsKeepExactDisplayWidthAndRightGutter(t *testing.T) {
	oldWidth := Width()
	t.Cleanup(func() { SetWidth(oldWidth) })

	group := "a-very-long-group-name-for-width-testing"
	rows := []railRow{
		{depth: 0, isGroup: true, collapsed: true, label: strings.Repeat("group", 20), sess: strings.Repeat("group", 20), count: 123, ghostCount: 45, uncertainCount: 6, bell: true, done: true},
		{depth: 0, isGroup: true, label: group, sess: group},
		{depth: 1, group: group, flat: true, ghost: true, label: "dead", sess: "dead", dir: "/home/someone/Projects/a/deeply/nested/ghost-directory"},
		{depth: 1, group: group, label: "needle-" + strings.Repeat("session", 15), sess: "needle-session", attached: true, bell: true, done: true},
		{depth: 2, isWin: true, group: group, label: strings.Repeat("0:window", 15), sess: "needle-session", window: "0", bell: true, done: true},
		{depth: 0, flat: true, label: "agent", sess: "agent", cmd: strings.Repeat("claude", 15), bell: true, done: true},
		{depth: 0, label: strings.Repeat("loose", 20), sess: "loose"},
		{depth: 1, isWin: true, label: strings.Repeat("1:window", 15), sess: "loose", window: "1", act: true, inView: true},
		{depth: 0, flat: true, label: strings.Repeat("uncertain", 15), sess: "uncertain", validity: rowUnvalidated},
	}

	for _, width := range []int{20, 30, 60} {
		SetWidth(width)
		for cursor := range rows {
			for i, row := range rows {
				rendered := renderRow(row, i == cursor, 0, "needle")
				if got := ansi.StringWidth(rendered); got != width {
					t.Errorf("width %d cursor %d row %d display width = %d, want %d: %q", width, cursor, i, got, width, ansi.Strip(rendered))
				}
			}
		}

		for _, index := range []int{0, 3, 4, 5} {
			plain := []rune(ansi.Strip(renderRow(rows[index], false, 0, "needle")))
			if got := string(plain[len(plain)-railMarksWidth:]); got != " ●✓" {
				t.Errorf("width %d row %d right gutter = %q, want %q", width, index, got, " ●✓")
			}
		}
		ghost := ansi.Strip(renderRow(rows[2], false, 0, "needle"))
		if !strings.Contains(ghost, "…") {
			t.Errorf("width %d ghost path was not left-truncated into its existing budget: %q", width, ghost)
		}
		agent := ansi.Strip(renderRow(rows[5], false, 0, "needle"))
		if !strings.Contains(agent, " · ") || !strings.Contains(agent, "…") {
			t.Errorf("width %d command was not truncated into its existing budget: %q", width, agent)
		}
	}
}

func TestTreeGlyphsAreSingleTerminalCells(t *testing.T) {
	for _, glyph := range []string{"▎", "▾", "▸"} {
		if got := ansi.StringWidth(glyph); got != 1 {
			t.Errorf("ansi width of %q = %d, want 1", glyph, got)
		}
		if got := lipgloss.Width(glyph); got != 1 {
			t.Errorf("lipgloss width of %q = %d, want 1", glyph, got)
		}
	}
}

func TestTreeIndentAddsNoTitleOrRowsAndKeepsHits(t *testing.T) {
	rows := treeIndentFixture()[:4]
	m := railModel{rows: rows, cursor: 0, height: 8, collapsed: map[string]bool{}, vp: &fakeViewport{}}
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	if len(lines) != m.height {
		t.Fatalf("View rendered %d lines, want unchanged height %d", len(lines), m.height)
	}
	if !strings.Contains(lines[treeTop], rows[0].label) {
		t.Fatalf("first tree row moved below a title: line %d = %q", treeTop, lines[treeTop])
	}
	for y := treeTop; y < treeTop+len(rows); y++ {
		index, ok := m.rowAt(y)
		if !ok || index != y-treeTop {
			t.Errorf("rowAt(%d) = (%d, %v), want unchanged row %d", y, index, ok, y-treeTop)
		}
	}
}

func TestTreeBodyHasNoBoxDrawingConnectors(t *testing.T) {
	body := ansi.Strip(treeBody(treeIndentFixture(), 1, 0, "", 14))
	for _, glyph := range []string{"├", "└", "│"} {
		if strings.Contains(body, glyph) {
			t.Errorf("tree body still contains connector %q: %q", glyph, body)
		}
	}
}
