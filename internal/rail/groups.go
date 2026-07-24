package rail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Groups are the first thing ghostmux owns that it cannot rediscover.
//
// Everything else in the rail is evidence: sessions, windows, marks, all read
// back from the multiplexers each tick, which is why the panel can be killed
// and relaunched anywhere. A grouping is user intent — nothing in tmux or
// zellij can report "these two belong together" — so it has to be written
// down. That is a real cost, and it buys the one thing a fleet view needs
// once the fleet is big enough to stop fitting on a screen.
//
// It lives in one small state file rather than in tmux user-options, because
// zellij has no equivalent: storing membership in the mux would mean zellij
// sessions could never be grouped, and multi-backend parity is the point.
// Group order and empty groups also need somewhere to live that a session's
// lifetime doesn't bound.

// Group is an ordered, named set of sessions, possibly spanning backends.
type Group struct {
	Name    string   `json:"name"`
	Members []string `json:"members"` // memberKey values, in display order
}

// groupState is the on-disk document. Fold state rides along with membership
// because it is the same kind of thing: a decision the user made that no
// multiplexer can report back. A folder that springs open on every relaunch
// is not a folder.
type groupState struct {
	Groups    []Group  `json:"groups"`
	Collapsed []string `json:"collapsed,omitempty"` // fold keys (see foldKey)
}

// memberKey identifies a session across backends. tmux is spelled out rather
// than left empty so the file reads as data, not as a Go zero value.
func memberKey(backend, sess string) string {
	if backend == "" {
		backend = "tmux"
	}
	return backend + ":" + sess
}

// groupsPath is the state file location: XDG_STATE_HOME, else ~/.local/state.
// State, not config — the user creates it through the UI, never by editing.
func groupsPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "ghostmux", "groups.json")
}

// loadState reads the state file. A missing or unreadable file is not an
// error: no groups is the normal state, and a corrupt file must never stop
// the panel from opening — the rail degrades to a flat fleet.
func loadState() ([]Group, map[string]bool) {
	path := groupsPath()
	if path == "" {
		return nil, map[string]bool{}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, map[string]bool{}
	}
	var st groupState
	if json.Unmarshal(b, &st) != nil {
		return nil, map[string]bool{}
	}
	collapsed := make(map[string]bool, len(st.Collapsed))
	for _, k := range st.Collapsed {
		collapsed[k] = true
	}
	return st.Groups, collapsed
}

// saveState writes the state file, creating its directory. Errors are
// returned so the caller can flash them; the in-memory state still stands for
// this run. Only folded keys are written — an unfolded row is the default and
// does not need recording.
func saveState(groups []Group, collapsed map[string]bool) error {
	path := groupsPath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	folded := make([]string, 0, len(collapsed))
	for k, v := range collapsed {
		if v {
			folded = append(folded, k)
		}
	}
	sort.Strings(folded) // stable file: no spurious diffs between runs
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(groupState{Groups: groups, Collapsed: folded}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// groupKey namespaces a group in the collapse map, so a group and a session
// of the same name never share disclosure state.
func groupKey(name string) string { return "grp:" + name }

// groupOf returns the name of the group holding key, or "".
func groupOf(groups []Group, key string) string {
	for _, g := range groups {
		for _, m := range g.Members {
			if m == key {
				return g.Name
			}
		}
	}
	return ""
}

// removeMember drops key from every group it appears in.
func removeMember(groups []Group, key string) []Group {
	for i := range groups {
		out := groups[i].Members[:0]
		for _, m := range groups[i].Members {
			if m != key {
				out = append(out, m)
			}
		}
		groups[i].Members = out
	}
	return groups
}

// applyGroups reorders a flat session/window tree into group folders. Sessions
// with no group keep their current shape and follow the groups, so a user who
// has made no groups sees exactly the rail they saw before — grouping is
// invisible until used.
func applyGroups(rows []railRow, groups []Group) []railRow {
	if len(groups) == 0 {
		return rows
	}
	// Index each session's rows (the session row plus its windows) by key.
	type block struct {
		key  string
		rows []railRow
	}
	var blocks []block
	for _, r := range rows {
		if r.isWin && len(blocks) > 0 {
			blocks[len(blocks)-1].rows = append(blocks[len(blocks)-1].rows, r)
			continue
		}
		blocks = append(blocks, block{key: memberKey(r.backend, r.sess), rows: []railRow{r}})
	}
	byKey := make(map[string]*block, len(blocks))
	for i := range blocks {
		byKey[blocks[i].key] = &blocks[i]
	}

	out := make([]railRow, 0, len(rows)+len(groups))
	used := map[string]bool{}
	for _, g := range groups {
		gr := railRow{depth: 0, isGroup: true, label: g.Name, sess: g.Name}
		// A folded group still has to report what is inside it, or folding
		// would hide exactly the thing the rail exists to surface.
		var members []railRow
		for _, key := range g.Members {
			b, ok := byKey[key]
			if !ok || used[key] {
				continue // session is gone, or listed in two groups
			}
			used[key] = true
			for _, r := range b.rows {
				r.depth++
				r.group = g.Name
				members = append(members, r)
				if !r.isWin {
					gr.bell = gr.bell || r.bell
					gr.done = gr.done || r.done
					gr.act = gr.act || r.act
					gr.inView = gr.inView || r.inView
				}
			}
		}
		gr.count = countSessions(members)
		out = append(out, gr)
		out = append(out, members...)
	}
	for i := range blocks {
		if !used[blocks[i].key] {
			out = append(out, blocks[i].rows...)
		}
	}
	return out
}

// countSessions counts session rows (not windows) in a member list.
func countSessions(rows []railRow) int {
	n := 0
	for _, r := range rows {
		if !r.isWin {
			n++
		}
	}
	return n
}

// --- model actions ---

// createGroup adds an empty group, or returns an error for a blank/duplicate
// name. Empty groups are legal: you name the shelf before you fill it.
func (m *railModel) createGroup(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errBlankName
	}
	for _, g := range m.groups {
		if g.Name == name {
			return errDuplicateGroup
		}
	}
	m.groups = append(m.groups, Group{Name: name})
	m.refresh()
	return m.saveState()
}

// deleteGroup removes a group and un-groups its members. It never touches a
// session: a shelf is not the things on it.
func (m *railModel) deleteGroup(name string) error {
	out := m.groups[:0]
	for _, g := range m.groups {
		if g.Name != name {
			out = append(out, g)
		}
	}
	m.groups = out
	m.refresh()
	return m.saveState()
}

// moveRow moves the selected session up (dir<0) or down (dir>0) in the rail:
// within its group, then across the boundary into the adjacent group, then
// out of grouping entirely. Ungrouped sessions sit below every group, so the
// only move available to them is up, into the last group.
func (m *railModel) moveRow(dir int) error {
	vis := m.visible()
	if m.cursor >= len(vis) {
		return nil
	}
	r := vis[m.cursor]
	if r.isGroup {
		return m.moveGroup(r.label, dir)
	}
	key := memberKey(r.backend, r.sess)
	gi, mi := m.findMember(key)

	if gi < 0 { // ungrouped
		if dir < 0 && len(m.groups) > 0 {
			last := len(m.groups) - 1
			m.groups[last].Members = append(m.groups[last].Members, key)
			return m.commitMove(key)
		}
		return nil
	}

	members := m.groups[gi].Members
	switch {
	case dir < 0 && mi > 0:
		members[mi], members[mi-1] = members[mi-1], members[mi]
	case dir > 0 && mi < len(members)-1:
		members[mi], members[mi+1] = members[mi+1], members[mi]
	case dir < 0 && gi > 0:
		m.groups = removeMember(m.groups, key)
		m.groups[gi-1].Members = append(m.groups[gi-1].Members, key)
	case dir > 0 && gi < len(m.groups)-1:
		m.groups = removeMember(m.groups, key)
		m.groups[gi+1].Members = append([]string{key}, m.groups[gi+1].Members...)
	case dir > 0:
		m.groups = removeMember(m.groups, key) // off the bottom: ungrouped
	default:
		return nil // top of the first group: nowhere further up
	}
	return m.commitMove(key)
}

// moveGroup reorders a whole group among its peers.
func (m *railModel) moveGroup(name string, dir int) error {
	for i, g := range m.groups {
		if g.Name != name {
			continue
		}
		j := i + dir
		if j < 0 || j >= len(m.groups) {
			return nil
		}
		m.groups[i], m.groups[j] = m.groups[j], m.groups[i]
		m.refresh()
		m.cursorToGroup(name)
		return m.saveState()
	}
	return nil
}

// commitMove rebuilds the tree and keeps the cursor on the row that moved —
// without it, the cursor would stay at a screen position and the user would
// be dragging a different session on the next keypress.
func (m *railModel) commitMove(key string) error {
	m.refresh()
	m.cursorToMember(key)
	return m.saveState()
}

func (m *railModel) findMember(key string) (int, int) {
	for gi, g := range m.groups {
		for mi, mk := range g.Members {
			if mk == key {
				return gi, mi
			}
		}
	}
	return -1, -1
}

func (m *railModel) cursorToMember(key string) {
	for i, r := range m.visible() {
		if !r.isGroup && !r.isWin && memberKey(r.backend, r.sess) == key {
			m.cursor = i
			return
		}
	}
	m.clamp()
}

func (m *railModel) cursorToGroup(name string) {
	for i, r := range m.visible() {
		if r.isGroup && r.label == name {
			m.cursor = i
			return
		}
	}
	m.clamp()
}

// errBlankName and errDuplicateGroup are the two ways naming a group fails.
var (
	errBlankName      = errNamed("name required")
	errDuplicateGroup = errNamed("group already exists")
)

type errNamed string

func (e errNamed) Error() string { return string(e) }

// forgetMember drops a killed session from its group, so the state file does
// not accumulate entries for sessions that will never come back.
func (m *railModel) forgetMember(key string) {
	if groupOf(m.groups, key) == "" {
		return
	}
	m.groups = removeMember(m.groups, key)
	m.saveState()
}

// saveState persists everything the rail owns: group membership and fold
// state. Called after any change to either.
func (m *railModel) saveState() error { return saveState(m.groups, m.collapsed) }

// toggleFold folds or unfolds a row and records it, so a folded group is
// still folded next launch.
func (m *railModel) toggleFold(r railRow) {
	if m.collapsed == nil {
		m.collapsed = map[string]bool{}
	}
	key := foldKey(r)
	if m.collapsed[key] {
		delete(m.collapsed, key) // absent == open: keeps the file minimal
	} else {
		m.collapsed[key] = true
	}
	m.clamp()
	m.saveState()
}
