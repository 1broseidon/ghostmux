package rail

import "strings"

// keyHelp is one line of the keymap: a key (as shown to the user) and its
// description. keyHelpRows is the single source of truth for the rail's
// keymap — the frame's help overlay and its bottom bar both derive from it
// (enforced by TestKeyHelpCoversBoundKeys).
type keyHelp struct{ key, desc string }

// The toggle is the only key whose name the rail does not choose: the frame
// intercepts it in process (GHOSTMUX_TOGGLE or the state file), and a desktop
// environment may have grabbed the default out from under it. The frame
// reports what it actually reserved so `?` can never lie about the keymap — a
// help page that names a dead key is worse than no help page.
var (
	toggleLabel = `alt+ctrl+\` // shown in the keymap table (first key)
	toggleAll   = `alt+ctrl+\` // shown in the footer (every accepted key)
)

// SetToggleKeys records the key(s) the hosting frame reserved for the
// rail ⇄ viewport toggle. Called once at startup, before any help render.
func SetToggleKeys(keys ...string) {
	if len(keys) == 0 || keys[0] == "" {
		return
	}
	toggleLabel, toggleAll = keys[0], strings.Join(keys, " or ")
}

// keyHelpRows is the operator keymap — short on purpose. Page jumps, attention
// cycling, and manual refresh are not taught here: j/k + the gutter cover
// hunting, and the 1s tick already refreshes.
//
// `?` and `,` are the frame's own keys, not the rail's: the table documents
// them anyway, because what a user needs from a keymap is every key that does
// something here, not a map of which package handles it.
func keyHelpRows() []keyHelp {
	return []keyHelp{
		{"j/k, ↓/↑", "move"},
		{"↵", "view / start ghost"},
		{"h/l", "fold / preview"},
		{"→", "focus viewport"},
		{"`", "previous session"},
		{"]", "oldest unseen ●/✓"},
		{"[", "peek unseen output"},
		{"v", "group wall"},
		{toggleLabel, "rail ⇄ viewport"},
		{"n", "new session"},
		{"a", "new group"},
		{"m, J/K", "organize"},
		{"u", "undo"},
		{"S", "start group's ghosts"},
		{"x", "kill / ungroup / forget"},
		{"/", "filter"},
		{"d", "detach"},
		{"?", "help"},
		{",", "settings"},
		{"q", "quit"},
	}
}

// HelpEntry is one keymap line, exported for the hosting frame's help overlay.
type HelpEntry struct{ Key, Desc string }

// HelpEntries is keyHelpRows for the frame. The rail no longer draws help
// itself: at 30 columns nine of these rows truncated, including the toggle
// row a user with a compositor-grabbed key needs intact. The table stays here
// because the keys are the rail's; only the drawing moved.
func HelpEntries() []HelpEntry {
	rows := keyHelpRows()
	out := make([]HelpEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, HelpEntry{Key: r.key, Desc: r.desc})
	}
	return out
}

// ToggleFooter names every accepted toggle key, or "" when there is only one.
// Two keys is a fact about this run (a grabbed default is not an error the
// terminal can report), so the overlay says it rather than implying the first
// key is the only one.
func ToggleFooter() string {
	if toggleAll == toggleLabel {
		return ""
	}
	return "toggle: " + toggleAll
}
