package rail

import (
	"fmt"
	"strings"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

// railRow is one line of the rail: a session or one of its windows. The
// attention marks are booleans; the 2-char gutter is derived from them by
// priority (bell > done > activity > in-viewport).
type railRow struct {
	depth     int // 0 session, 1 window
	label     string
	sess      string
	window    string // window index (depth-1 rows and flat session rows)
	active    bool   // tmux's current window of its session
	attached  bool   // session attached by an outside client (session rows)
	bell      bool   // ● window_bell_flag
	done      bool   // ✓ @ghostmux_done (D5)
	act       bool   // ~ window_activity_flag
	inView    bool   // ▸ locked in the viewport
	collapsed bool   // session rows: collapsed in the rail (view-only, set by visibleRows)
	flat      bool   // single-window session rendered as one row, no children
	cmd       string // flat rows: the window's foreground command, shown dim
	backend   string // "" = tmux; e.g. "zellij" for other multiplexers
}

// shellCmds are the foreground commands that count as "back at a prompt" — a
// non-shell → shell transition is what marks a window done (D5).
var shellCmds = map[string]bool{"zsh": true, "bash": true, "fish": true, "sh": true, "dash": true}

// agentCmds are foreground commands recognized as AI agents. Agent-ness is
// ambient: observed from what actually runs in the slot, never declared by a
// naming convention or a separate session type.
var agentCmds = map[string]bool{
	"claude": true, "codex": true, "aider": true, "gemini": true,
	"goose": true, "amp": true, "oa": true, "sol": true, "pi": true,
}

// isAgentCmd reports whether a foreground command is a recognized agent.
func isAgentCmd(cmd string) bool { return agentCmds[cmd] }

// viewState is what the viewport is currently showing, used to compute inView
// marks and to suppress the done mark on a session the user is watching.
type viewState struct {
	lockSess    string
	lockWin     string // "" = whole session (its active window)
	lockBackend string // "" = tmux
}

// gutter returns up to two attention glyphs, highest priority first:
// ● bell > ✓ done > ~ activity > ▸ in-viewport.
func (r railRow) gutter() string {
	var g []rune
	if r.bell {
		g = append(g, '●')
	}
	if r.done {
		g = append(g, '✓')
	}
	if r.act {
		g = append(g, '~')
	}
	if r.inView {
		g = append(g, '▸')
	}
	if len(g) > 2 {
		g = g[:2]
	}
	return string(g)
}

func (r railRow) plain() string {
	mark := " "
	if r.active {
		mark = "*"
	}
	return fmt.Sprintf("%s%s%-2s %s", strings.Repeat("  ", r.depth), mark, r.gutter(), r.label)
}

// plainFiltered is plain()'s counterpart for `rail once --filter q`: rows
// that don't match the query get a leading "·" (dim marker); matching rows
// get a leading space. Row order/positions are unchanged from plain().
func (r railRow) plainFiltered(query string) string {
	if matchesFilter(r, query) {
		return " " + r.plain()
	}
	return "·" + r.plain()
}

// marks is rail once --marks's machine format for one row:
// "SESS|WIN|bell,done,act,view" — WIN is empty for session rows, and the
// flags list only the marks that are set (empty string if none).
func (r railRow) marks() string {
	sess := r.sess
	if r.backend != "" {
		sess = r.backend + ":" + r.sess // backend-qualified row identity
	}
	win := ""
	if r.depth == 1 || r.flat {
		win = r.window
	}
	var flags []string
	if r.bell {
		flags = append(flags, "bell")
	}
	if r.done {
		flags = append(flags, "done")
	}
	if r.act {
		flags = append(flags, "act")
	}
	if r.inView {
		flags = append(flags, "view")
	}
	return fmt.Sprintf("%s|%s|%s", sess, win, strings.Join(flags, ","))
}

// railRows is the live-tmux entry point: fetch the fleet and build the tree.
func railRows(hub string, v viewState) []railRow {
	return buildRows(hub, v, tmux.Sessions(), tmux.Windows())
}

// buildRows renders sessions and their windows into rail rows, excluding the
// hub session the rail itself lives in (rendering the hub inside its own
// viewport would be an infinite mirror). Session rows aggregate the single
// highest-priority mark across their windows (mockup screen 2).
func buildRows(hub string, v viewState, sessions []tmux.Session, windows []tmux.Window) []railRow {
	var rows []railRow
	for _, s := range sessions {
		if s.Name == hub || strings.HasPrefix(s.Name, gmViewPrefix) {
			continue // gm-view-* shadows are viewport plumbing, not fleet
		}
		// "Attached" means attached ELSEWHERE: the viewport's own nested
		// client doesn't count, or every viewed session would show ●.
		attached := s.Clients > 0
		if s.Name == v.lockSess {
			attached = s.Clients > 1
		}
		sessRow := railRow{depth: 0, label: s.Name, sess: s.Name, attached: attached}
		var winRows []railRow
		var aggBell, aggDone, aggAct, aggView bool
		var sessWins []tmux.Window
		for _, w := range windows {
			if w.Session == s.Name {
				sessWins = append(sessWins, w)
			}
		}
		// A single-window session renders flat: one row, no children, the
		// window's marks inherited directly and its foreground command shown
		// dim — the rail reads as a fleet dashboard, not a file tree.
		if len(sessWins) == 1 {
			w := sessWins[0]
			cmd := ""
			if len(w.PaneCmds) > 0 {
				cmd = w.PaneCmds[0]
			}
			row := railRow{
				depth: 0, flat: true, label: s.Name, sess: s.Name,
				window: w.Index, attached: attached, active: w.Active,
				bell: w.Bell, done: w.Done, act: w.Activity,
				inView: isViewed(v, w.Session, w.Index, w.Active), cmd: cmd,
			}
			suppressViewedMarks(&row)
			rows = append(rows, row)
			continue
		}
		for _, w := range sessWins {
			inView := isViewed(v, w.Session, w.Index, w.Active)
			row := railRow{
				depth: 1, label: w.Index + ":" + w.Name,
				sess: s.Name, window: w.Index, active: w.Active,
				bell: w.Bell, done: w.Done, act: w.Activity, inView: inView,
			}
			suppressViewedMarks(&row)
			winRows = append(winRows, row)
			aggBell = aggBell || row.bell
			aggDone = aggDone || row.done
			aggAct = aggAct || row.act
			aggView = aggView || inView
		}
		switch { // single highest-priority aggregate mark
		case aggBell:
			sessRow.bell = true
		case aggDone:
			sessRow.done = true
		case aggAct:
			sessRow.act = true
		case aggView:
			sessRow.inView = true
		}
		rows = append(rows, sessRow)
		rows = append(rows, winRows...)
	}
	return rows
}

// visibleRows applies collapse state to a flat row tree: collapsed session
// rows are stamped `collapsed: true` and their window rows are dropped
// entirely. Pure function over rows + collapse state (Task 7).
func visibleRows(rows []railRow, collapsed map[string]bool) []railRow {
	if len(rows) == 0 {
		return rows
	}
	out := make([]railRow, 0, len(rows))
	hideSess := ""
	for _, r := range rows {
		if r.depth == 0 {
			hideSess = ""
			if !r.flat { // flat rows have no children and no disclosure state
				r.collapsed = collapsed[r.sess]
				if r.collapsed {
					hideSess = r.sess
				}
			}
			out = append(out, r)
			continue
		}
		if r.sess == hideSess {
			continue
		}
		out = append(out, r)
	}
	return out
}

// matchesFilter reports whether a row matches a filter query: case-
// insensitive substring against the session name, or — for window rows —
// against "index:name" too. A window row matches when its OWN label matches
// or its SESSION matches (so every row of a matching session stays lit; only
// non-matching sessions and their windows dim — DESIGN.md screen 5). An
// empty query matches everything.
func matchesFilter(r railRow, query string) bool {
	if query == "" {
		return true
	}
	q := strings.ToLower(query)
	if strings.Contains(strings.ToLower(r.sess), q) {
		return true
	}
	if r.depth == 1 && strings.Contains(strings.ToLower(r.label), q) {
		return true
	}
	return false
}

// suppressViewedMarks drops attention marks on the row the viewport is
// showing — you're looking at it, so nothing there needs attention. This also
// covers grouped (gm-view-*) attaches, where tmux's native alert flags don't
// clear on the origin session because no client displays its own winlink.
func suppressViewedMarks(r *railRow) {
	if r.inView {
		r.bell, r.done, r.act = false, false, false
	}
}

// isViewed reports whether the viewport is showing this window: either it is
// the explicitly locked window, or the whole session is locked and this is its
// active window.
func isViewed(v viewState, sess, window string, active bool) bool {
	if v.lockBackend != "" || v.lockSess != sess {
		return false // aux-backend locks never mark tmux rows
	}
	return v.lockWin == window || (v.lockWin == "" && active)
}

// paneKey identifies a single pane for command-transition tracking.
type paneKey struct {
	sess, window string
	idx          int
}

// doneTracker watches #{pane_current_command} per pane across refreshes and
// sets @ghostmux_done when a foreground command exits to a shell in a session
// the user is neither viewing nor attached to (D5).
type doneTracker struct {
	last map[paneKey]string
}

func newDoneTracker() *doneTracker {
	return &doneTracker{last: map[paneKey]string{}}
}

// observe records the current per-pane commands and, for each non-shell→shell
// transition, marks the window done unless suppress(sess, window) is true
// (session viewed in the viewport or attached elsewhere). The hub session is
// never tracked. Panes that vanished are forgotten so a later reuse of the same
// slot does not read as a transition against a stale command.
func (dt *doneTracker) observe(windows []tmux.Window, hub string, suppress func(sess, window string) bool) {
	seen := map[paneKey]bool{}
	for _, w := range windows {
		// Skip the hub and gm-view-* shadows — grouped sessions list the
		// same shared panes again and would double-fire transitions.
		if w.Session == hub || strings.HasPrefix(w.Session, gmViewPrefix) {
			continue
		}
		for i, cmd := range w.PaneCmds {
			key := paneKey{w.Session, w.Index, i}
			seen[key] = true
			prev, had := dt.last[key]
			dt.last[key] = cmd
			if had && !shellCmds[prev] && shellCmds[cmd] && !suppress(w.Session, w.Index) {
				tmux.SetDone(w.Session, w.Index, true)
			}
		}
	}
	for k := range dt.last {
		if !seen[k] {
			delete(dt.last, k)
		}
	}
}
