package rail

// viewRef is an exact viewport target. Win is empty for a whole-session view.
type viewRef struct {
	Sess string
	Win  string
}

func viewRefOf(lock ViewState) viewRef {
	return viewRef{Sess: lock.Sess, Win: lock.Win}
}

func (r viewRef) sameSession(other viewRef) bool {
	return r.Sess != "" && r.Sess == other.Sess
}

// structuralRow reports whether a row owns disclosure state. Flat sessions,
// windows, and declarations are leaves and must never create fold state.
func structuralRow(r railRow) bool {
	return r.isGroup || (!r.isWin && !r.flat)
}

// visibleParentIndex returns the nearest parent represented in the visible
// tree. A window belongs to its session; a grouped session or declaration
// belongs to its group. Top-level rows have no parent.
func visibleParentIndex(rows []railRow, cursor int) int {
	if cursor < 0 || cursor >= len(rows) {
		return -1
	}
	r := rows[cursor]
	if r.isWin {
		for i := cursor - 1; i >= 0; i-- {
			candidate := rows[i]
			if candidate.isGroup {
				break
			}
			if !candidate.isWin && candidate.sess == r.sess {
				return i
			}
		}
		return -1
	}
	if !r.isGroup && r.group != "" {
		for i := cursor - 1; i >= 0; i-- {
			if rows[i].isGroup && rows[i].label == r.group {
				return i
			}
		}
	}
	return -1
}

func rowEligible(r railRow, query string) bool {
	return query == "" || matchesFilter(r, query)
}

// physicalMoveIndex moves around a physical-row target while preserving the
// filter rule that the cursor never lands on a dimmed row. It first seeks in
// the requested direction, then back toward the old cursor at an edge.
func physicalMoveIndex(rows []railRow, cursor, delta int, query string) int {
	if len(rows) == 0 {
		return 0
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(rows) {
		cursor = len(rows) - 1
	}
	if delta == 0 {
		return cursor
	}

	dir := 1
	if delta < 0 {
		dir = -1
	}
	target := cursor + delta
	if target < 0 {
		target = 0
	}
	if target >= len(rows) {
		target = len(rows) - 1
	}
	if target == cursor {
		return cursor
	}
	if rowEligible(rows[target], query) {
		return target
	}
	for i := target + dir; i >= 0 && i < len(rows); i += dir {
		if rowEligible(rows[i], query) {
			return i
		}
	}
	for i := target - dir; i != cursor; i -= dir {
		if i < 0 || i >= len(rows) {
			break
		}
		if rowEligible(rows[i], query) {
			return i
		}
	}
	return cursor
}

// nonWindowMoveIndex finds the next group/session/declaration in one
// direction, skipping windows and filter-dimmed rows. It clamps at the ends.
func nonWindowMoveIndex(rows []railRow, cursor, dir int, query string) int {
	if len(rows) == 0 {
		return 0
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(rows) {
		cursor = len(rows) - 1
	}
	if dir == 0 {
		return cursor
	}
	if dir < 0 {
		dir = -1
	} else {
		dir = 1
	}
	for i := cursor + dir; i >= 0 && i < len(rows); i += dir {
		if !rows[i].isWin && rowEligible(rows[i], query) {
			return i
		}
	}
	return cursor
}

// attentionLeaf is one countable source of attention for the bar: a fresh live
// window or flat session. Activity (~) stays gutter-only. Aggregates are not
// counted — the bar reports leaves, and j/k finds the mark.
func attentionLeaf(r railRow) bool {
	if r.isGroup || r.ghost || r.validity != rowFresh {
		return false
	}
	return r.isWin || r.flat
}

type viewResolution uint8

const (
	viewResolved viewResolution = iota
	viewMissing
	viewGhost
	viewUnavailable
)

// resolveViewRef searches raw rows so folded targets remain addressable. It
// never falls back from an exact window to a same-session aggregate.
func resolveViewRef(rows []railRow, ref viewRef) (railRow, viewResolution) {
	if ref.Sess == "" {
		return railRow{}, viewMissing
	}
	var candidates []railRow
	for _, r := range rows {
		if r.isGroup || r.sess != ref.Sess {
			continue
		}
		candidates = append(candidates, r)
		exact := false
		if ref.Win == "" {
			exact = !r.isWin
		} else {
			exact = (r.isWin || r.flat) && r.window == ref.Win
		}
		if !exact {
			continue
		}
		switch {
		case r.validity != rowFresh:
			return railRow{}, viewUnavailable
		case r.ghost:
			return railRow{}, viewGhost
		default:
			return r, viewResolved
		}
	}
	// A former live session may now be represented by a flat ghost or an
	// uncertain declaration with no matching window field. Report provenance,
	// not a misleading missing-window fallback.
	for _, r := range candidates {
		if r.validity != rowFresh {
			return railRow{}, viewUnavailable
		}
		if r.ghost {
			return railRow{}, viewGhost
		}
	}
	return railRow{}, viewMissing
}

func rowExactView(r railRow, lock ViewState) bool {
	return r.sess == lock.Sess && r.window == lock.Win
}

func viewTargetName(r railRow) string {
	if r.isWin {
		return r.sess + " / " + r.label
	}
	return r.label
}
