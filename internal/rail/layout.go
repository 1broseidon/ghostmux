package rail

// Pure layout math for the rail tree (Task 7): label truncation and the
// scroll-window row selection. Kept dependency-free (no lipgloss, no tmux) so
// they're trivially unit-testable.

// railWidth is the rail's column count. It is a var because the settings pane
// can change it, and everything that draws a row reads the same value.
var railWidth = defaultWidth

// Width bounds and default are shared with the frame when it reconciles an
// adopted settings snapshot.
const (
	defaultWidth = 30
	widthMin     = 20
	widthMax     = 60
)

// Width is the rail's current column count, for hosting frames: classic
// enforces it on a tmux pane, solo allocates it in its own layout.
func Width() int { return railWidth }

// DefaultWidth returns the width used when no explicit setting is stored.
func DefaultWidth() int { return defaultWidth }

// ClampWidth returns the supported value without changing live layout.
func ClampWidth(cols int) int {
	if cols < widthMin {
		return widthMin
	}
	if cols > widthMax {
		return widthMax
	}
	return cols
}

// SetWidth applies a clamped user-chosen rail width.
func SetWidth(cols int) int {
	railWidth = ClampWidth(cols)
	return railWidth
}

// GhostDir values for Settings.GhostDir: which tmux path evidence a ghost
// remembers when summoned. Empty means launch (the default).
const (
	GhostDirLaunch = "launch" // #{session_path}: where the session was created
	GhostDirLast   = "last"   // active pane #{pane_current_path}
)

var ghostDirMode = GhostDirLaunch

// SetGhostDir applies the ghost directory memory mode. Unknown values fall
// back to launch — the historical, SPEC-documented behaviour.
func SetGhostDir(mode string) {
	ghostDirMode = NormalizeGhostDir(mode)
}

// GhostDir returns the live memory mode (never empty: launch or last).
func GhostDir() string { return ghostDirMode }

// NormalizeGhostDir maps a stored setting to a live mode. Empty and unknown
// values are launch so old files and cleared settings keep prior behaviour.
func NormalizeGhostDir(mode string) string {
	if mode == GhostDirLast {
		return GhostDirLast
	}
	return GhostDirLaunch
}

// PersistGhostDir is what goes in Settings.GhostDir: empty for the default
// (launch), otherwise the explicit mode string.
func PersistGhostDir(mode string) string {
	if NormalizeGhostDir(mode) == GhostDirLast {
		return GhostDirLast
	}
	return ""
}

// CreateDir values for Settings.CreateDir: where `n` starts a new tmux
// session. Empty means home (the default).
const (
	CreateDirHome    = "home"    // the operator's home directory
	CreateDirCurrent = "current" // the viewport session's active pane cwd
)

var createDirMode = CreateDirHome

// SetCreateDir applies the new-session directory mode. Unknown values fall
// back to home — the historical behaviour.
func SetCreateDir(mode string) {
	createDirMode = NormalizeCreateDir(mode)
}

// CreateDir returns the live mode (never empty: home or current).
func CreateDir() string { return createDirMode }

// NormalizeCreateDir maps a stored setting to a live mode. Empty and unknown
// values are home so old files and cleared settings keep prior behaviour.
func NormalizeCreateDir(mode string) string {
	if mode == CreateDirCurrent {
		return CreateDirCurrent
	}
	return CreateDirHome
}

// PersistCreateDir is what goes in Settings.CreateDir: empty for the default
// (home), otherwise the explicit mode string.
func PersistCreateDir(mode string) string {
	if NormalizeCreateDir(mode) == CreateDirCurrent {
		return CreateDirCurrent
	}
	return ""
}

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

// truncateLeft keeps the TAIL of s and marks the cut with a leading "…"
// ("…ects/api"). It exists for paths: the head of a path is the same
// /home/george/Projects on every row, and the part that identifies the
// directory is the part truncateLabel would throw away.
func truncateLeft(s string, width int) string {
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
	return "…" + string(r[len(r)-(width-1):])
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
