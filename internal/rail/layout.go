package rail

// Pure layout math for the rail tree (Task 7): label truncation and the
// scroll-window row selection. Kept dependency-free (no lipgloss, no tmux) so
// they're trivially unit-testable.

const railWidth = 30 // cols 1-30 (matches `split-window -hbf -l 30`)

// Width is the rail's fixed column count, exported for hosting frames: classic
// enforces it on a tmux pane, solo allocates it in its own layout.
const Width = railWidth

// truncateLabel truncates s to at most width runes, replacing the tail with
// "…" (U+2026) when it doesn't fit. width<=0 yields "". A width of 1 yields
// "…" itself (no room for content).
func truncateLabel(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}

// scrollWindow picks the visible slice of n rows in a viewport of height
// rows, keeping cursor in view. It returns [start,end) into the row slice,
// plus how many rows are hidden above/below (0 when nothing is hidden). When
// rows are hidden, the caller replaces the top/bottom visible row with a
// "N more…" indicator (accounted for by reserving a row at that edge here).
func scrollWindow(n, height, cursor int) (start, end, moreUp, moreDown int) {
	if height <= 0 || n <= 0 {
		return 0, 0, 0, 0
	}
	if n <= height {
		return 0, n, 0, 0
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= n {
		cursor = n - 1
	}
	// Start with a naive window keeping cursor centered-ish, then clamp.
	start = cursor - height/2
	if start < 0 {
		start = 0
	}
	end = start + height
	if end > n {
		end = n
		start = end - height
		if start < 0 {
			start = 0
		}
	}
	moreUp = start
	moreDown = n - end
	// Reserve one row at each edge that needs an indicator, so the visible
	// slice plus indicator rows never exceeds height.
	if moreUp > 0 {
		start++
		if start > cursor {
			start = cursor
		}
	}
	if moreDown > 0 {
		end--
		if end <= cursor {
			end = cursor + 1
		}
	}
	if start < 0 {
		start = 0
	}
	if end > n {
		end = n
	}
	if start > end {
		start = end
	}
	moreUp = start
	moreDown = n - end
	return start, end, moreUp, moreDown
}
