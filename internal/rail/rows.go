package rail

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

// rowValidity records whether a backend row comes from the latest successful
// query, a retained snapshot after an outage, or a declaration whose backend
// has never been validated. The zero value is fresh so pure row builders and
// state-only group rows retain their existing behavior.
type rowValidity uint8

const (
	rowFresh rowValidity = iota
	rowStale
	rowUnvalidated
)

// railRow is one line of the rail: a session or one of its windows. The
// attention marks are booleans; the gutter is derived from them and validity.
type railRow struct {
	depth int // tree level: group 0, its sessions 1, their windows 2;
	// ungrouped sessions stay at 0 with windows at 1
	isGroup        bool   // a user-made folder, not anything the muxes reported
	isWin          bool   // a window row (vs a session or group row)
	group          string // name of the group holding this row, "" = ungrouped
	count          int    // group rows: how many live sessions are inside
	uncertainCount int    // group rows: members whose backend state is unknown

	// ghost marks a declared name that is authoritatively not running.
	ghost      bool
	ghostCount int    // group rows: how many authoritative ghosts are inside
	dir        string // ghost rows: where a summon would start it, "" if unknown
	validity   rowValidity

	label      string
	sess       string
	window     string // window index (depth-1 rows and flat session rows)
	active     bool   // tmux's current window of its session
	attached   bool   // session attached by an outside client (session rows)
	bell       bool   // ● window_bell_flag
	done       bool   // ✓ @ghostmux_done (D5)
	act        bool   // ~ window_activity_flag
	activityAt int64  // #{window_activity}: when this window last saw output
	inView     bool   // ▸ locked in the viewport
	collapsed  bool   // session rows: collapsed in the rail (view-only, set by visibleRows)
	flat       bool   // single-window session rendered as one row, no children
	cmd        string // the window's foreground command (flat + window rows); shown dim on flat rows
	instanceID string // stable tmux session ID captured with this row
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

// builtinAgentCmds records which names shipped with ghostmux, so the settings
// pane can show them as un-removable: the user's list is additive, and a
// built-in the user cannot delete must look different from one they added.
var builtinAgentCmds = func() map[string]bool {
	out := make(map[string]bool, len(agentCmds))
	for k := range agentCmds {
		out[k] = true
	}
	return out
}()

// isAgentCmd reports whether a foreground command is a recognized agent.
func isAgentCmd(cmd string) bool { return agentCmds[cmd] }

// agentQuietAge renders how long a window has been quiet, from tmux's own
// #{window_activity} timestamp. Empty under a minute (recent output is not
// news) and empty without evidence. Coarse units on purpose: this is a
// glance, not a stopwatch.
func agentQuietAge(now time.Time, activityAt int64) string {
	if activityAt <= 0 {
		return ""
	}
	d := now.Unix() - activityAt
	switch {
	case d < 60:
		return ""
	case d < 60*60:
		return fmt.Sprintf("%dm", d/60)
	case d < 24*60*60:
		return fmt.Sprintf("%dh", d/(60*60))
	default:
		return fmt.Sprintf("%dd", d/(24*60*60))
	}
}

// AddAgentCmds merges user-declared agent commands into the detection set.
// Lowercased and deduped, because pane_current_command is what it is compared
// against and that is what tmux reports.
func AddAgentCmds(cmds []string) {
	for _, c := range cmds {
		if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
			agentCmds[c] = true
		}
	}
}

// SetExtraAgentCmds replaces the user-added detection names exactly.
func SetExtraAgentCmds(cmds []string) {
	for cmd := range agentCmds {
		if !builtinAgentCmds[cmd] {
			delete(agentCmds, cmd)
		}
	}
	AddAgentCmds(cmds)
}

// BuiltinAgentCmds and ExtraAgentCmds split the detection set for display:
// what ghostmux knows on its own, and what this user added. Both sorted, so
// the pane does not reshuffle between renders.
func BuiltinAgentCmds() []string { return sortedKeys(builtinAgentCmds) }

// ExtraAgentCmds is everything AddAgentCmds contributed.
func ExtraAgentCmds() []string {
	out := map[string]bool{}
	for k := range agentCmds {
		if !builtinAgentCmds[k] {
			out[k] = true
		}
	}
	return sortedKeys(out)
}

// RemoveAgentCmd drops a user-added command. A built-in is never removed:
// ghostmux would then be claiming it cannot see something it plainly can.
func RemoveAgentCmd(cmd string) {
	cmd = strings.ToLower(strings.TrimSpace(cmd))
	if cmd != "" && !builtinAgentCmds[cmd] {
		delete(agentCmds, cmd)
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// gutter returns up to two attention glyphs, highest priority first:
// ● bell > ✓ done > ~ activity > ▸ in-viewport.
func (r railRow) gutter() string {
	// Unknown backend state suppresses every cached mark and every absence
	// assertion. The question mark is provenance, not another attention mark.
	if !r.isGroup && r.validity != rowFresh {
		return "?"
	}
	// A ghost's whole story is "declared, not running". ○ says it, and nothing
	// else may be said: every other glyph reports something a live session is
	// doing, and there is no process here to be doing it.
	if r.ghost {
		return "○"
	}
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
	win := ""
	if r.depth == 1 || r.flat {
		win = r.window
	}
	var flags []string
	if r.ghost {
		flags = append(flags, "ghost")
	}
	if r.validity != rowFresh {
		flags = append(flags, "uncertain")
	}
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

// railRows is the compatibility live-tmux entry point used by focused row
// tests. Interactive refresh and rail once surface tmux.Query errors.
func railRows(hub string, v ViewState) []railRow {
	snapshot, err := tmux.Query()
	if err != nil {
		return nil
	}
	return buildRows(hub, v, snapshot.Sessions, snapshot.Windows)
}

func stampValidity(rows []railRow, validity rowValidity) []railRow {
	for i := range rows {
		rows[i].validity = validity
	}
	return rows
}

// buildRows renders sessions and their windows into rail rows, excluding the
// hub session the rail itself lives in (rendering the hub inside its own
// viewport would be an infinite mirror). Session rows aggregate the single
// highest-priority mark across their windows (mockup screen 2).
func buildRows(hub string, v ViewState, sessions []tmux.Session, windows []tmux.Window) []railRow {
	var rows []railRow
	for _, s := range sessions {
		if s.Name == hub || tmux.IsOwnedView(s) {
			continue // exact owned shadows are viewport plumbing, not fleet
		}
		// "Attached" means attached ELSEWHERE: the viewport's own nested
		// client doesn't count, or every viewed session would show ●.
		attached := s.Clients > 0
		if s.Name == v.Sess {
			attached = s.Clients > 1
		}
		sessRow := railRow{
			depth: 0, label: s.Name, sess: s.Name, attached: attached,
			instanceID: s.SessionID,
		}
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
				instanceID: s.SessionID,
				bell:       w.Bell, done: w.Done, act: w.Activity,
				activityAt: w.ActivityAt,
				inView:     isViewed(v, w.Session, w.Index, w.Active), cmd: cmd,
			}
			suppressViewedMarks(&row)
			rows = append(rows, row)
			continue
		}
		for _, w := range sessWins {
			inView := isViewed(v, w.Session, w.Index, w.Active)
			cmd := ""
			if len(w.PaneCmds) > 0 {
				cmd = w.PaneCmds[0]
			}
			row := railRow{
				depth: 1, isWin: true, label: w.Index + ":" + w.Name,
				sess: s.Name, window: w.Index, active: w.Active,
				instanceID: s.SessionID, cmd: cmd,
				bell: w.Bell, done: w.Done, act: w.Activity, inView: inView,
				activityAt: w.ActivityAt,
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
	hideSess, hideGroup := "", ""
	for _, r := range rows {
		if r.isGroup {
			hideSess, hideGroup = "", ""
			r.collapsed = collapsed[groupKey(r.label)]
			if r.collapsed {
				hideGroup = r.label
			}
			out = append(out, r)
			continue
		}
		if r.group != "" && r.group == hideGroup {
			continue // folded group: sessions and windows alike
		}
		if !r.isWin {
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
// covers grouped attaches, where tmux's native alert flags don't clear on the
// origin session because no client displays its own winlink.
func suppressViewedMarks(r *railRow) {
	if r.inView {
		r.bell, r.done, r.act = false, false, false
	}
}

// isViewed reports whether the viewport is showing this window: either it is
// the explicitly locked window, or the whole session is locked and this is its
// active window.
func isViewed(v ViewState, sess, window string, active bool) bool {
	if v.Sess != sess {
		return false
	}
	return v.Win == window || (v.Win == "" && active)
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

// reset breaks command-transition continuity across a backend outage. The
// first recovered snapshot seeds a new baseline instead of inventing a direct
// transition across an interval ghostmux could not observe.
func (dt *doneTracker) reset() {
	if dt != nil {
		dt.last = map[paneKey]string{}
	}
}

// observe records the current per-pane commands and, for each non-shell→shell
// transition, marks the window done unless suppress(sess, window) is true
// (session viewed in the viewport or attached elsewhere). The hub session is
// never tracked. Panes that vanished are forgotten so a later reuse of the same
// slot does not read as a transition against a stale command.
func (dt *doneTracker) observe(windows []tmux.Window, sessions []tmux.Session, hub string, suppress func(sess, window string) bool) {
	owned := map[string]bool{}
	for _, session := range sessions {
		if tmux.IsOwnedView(session) {
			owned[session.Name] = true
		}
	}
	seen := map[paneKey]bool{}
	for _, w := range windows {
		// Exact owned shadows list their group's shared panes again and would
		// double-fire transitions. Prefix-only legacy sessions remain real.
		if w.Session == hub || owned[w.Session] {
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
