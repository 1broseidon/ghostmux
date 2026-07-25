package rail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
	// Dirs is where each grouped member was last observed running, memberKey →
	// path. It is captured from evidence (#{session_path}), never asked for, and
	// exists so a ghost can be summoned back into the directory it belonged to.
	// Omitted when empty so a fleet that has never had a ghost writes the same
	// file it always did.
	Dirs map[string]string `json:"dirs,omitempty"`
	// Settings is what the user changed through the settings pane. It rides in
	// this file rather than a config file of its own because it is the same
	// kind of thing as a group: a decision no multiplexer can report back. The
	// file is "the state file", not "the groups file".
	Settings *Settings `json:"settings,omitempty"`
}

// Settings is the user-editable half of the state file. Every field is
// optional: an absent field means "whatever the default is", so a file written
// before settings existed loads unchanged and an untouched setting is never
// written down.
type Settings struct {
	Toggle    []string `json:"toggle,omitempty"`     // bubbletea key names
	RailWidth int      `json:"rail_width,omitempty"` // 0 = default
	Agents    []string `json:"agents,omitempty"`     // extra agent cmds
}

// empty reports whether nothing has been set — the condition for leaving the
// key out of the file entirely.
func (s Settings) empty() bool {
	return len(s.Toggle) == 0 && s.RailWidth == 0 && len(s.Agents) == 0
}

// memberKey identifies a session across backends. tmux is spelled out rather
// than left empty so the file reads as data, not as a Go zero value.
func memberKey(backend, sess string) string {
	if backend == "" {
		backend = "tmux"
	}
	return backend + ":" + sess
}

// backendOf and sessOf invert memberKey. backendOf answers in railRow's
// spelling, where tmux is "" — the file says "tmux:api" so it reads as data,
// the row says "" so tmux stays the zero-value default.
func backendOf(key string) string {
	b, _, ok := strings.Cut(key, ":")
	if !ok || b == "tmux" {
		return ""
	}
	return b
}

func sessOf(key string) string {
	_, s, ok := strings.Cut(key, ":")
	if !ok {
		return key
	}
	return s
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

// readState reads the whole document. A missing, unreadable, or corrupt file
// is not an error: no state is the normal state, and a bad file must never
// stop the panel from opening. It is the single reader — every caller that
// wants one field still goes through here, so a save that rewrites the file
// can never drop a field it did not know about.
func readState() groupState {
	path := groupsPath()
	if path == "" {
		return groupState{}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return groupState{}
	}
	var st groupState
	if json.Unmarshal(b, &st) != nil {
		return groupState{}
	}
	return st
}

// loadState reads the rail's own three fields. A missing or unreadable file is
// not an error: no groups is the normal state, and a corrupt file must never
// stop the panel from opening — the rail degrades to a flat fleet.
func loadState() ([]Group, map[string]bool, map[string]string) {
	st := readState()
	collapsed := make(map[string]bool, len(st.Collapsed))
	for _, k := range st.Collapsed {
		collapsed[k] = true
	}
	// A file written before dirs existed unmarshals a nil map; hand back an
	// empty one so every caller can write to it without a nil check.
	dirs := st.Dirs
	if dirs == nil {
		dirs = map[string]string{}
	}
	return st.Groups, collapsed, dirs
}

// saveState writes the state file, creating its directory. Errors are
// returned so the caller can flash them; the in-memory state still stands for
// this run. Only folded keys are written — an unfolded row is the default and
// does not need recording.
func saveState(groups []Group, collapsed map[string]bool, dirs map[string]string) error {
	folded := make([]string, 0, len(collapsed))
	for k, v := range collapsed {
		if v {
			folded = append(folded, k)
		}
	}
	sort.Strings(folded) // stable file: no spurious diffs between runs
	if len(dirs) == 0 {
		dirs = nil // omitempty: no ghost dirs recorded, no key in the file
	}
	// Read-modify-write: settings belong to the frame, not to the rail, and a
	// rail save that wrote its own three fields alone would silently erase
	// them. Safe because the frame and the rail share one process and one
	// single-threaded bubbletea update loop.
	st := readState()
	st.Groups, st.Collapsed, st.Dirs = groups, folded, dirs
	return writeState(st)
}

// writeState serializes the whole document, creating its directory. Errors are
// returned so the caller can flash them; the in-memory state still stands for
// this run.
func writeState(st groupState) error {
	path := groupsPath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if st.Settings != nil && st.Settings.empty() {
		st.Settings = nil // nothing set: no key in the file
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// LoadSettings reads the user-editable half of the state file. An absent
// settings key is not an error — it is the normal state — so the zero value
// comes back and every field falls through to its default.
func LoadSettings() Settings {
	st := readState()
	if st.Settings == nil {
		return Settings{}
	}
	return *st.Settings
}

// SaveSettings writes settings back, leaving groups, folds, and dirs exactly
// as they are on disk. Same read-modify-write, same reason: the two halves of
// this file have two owners, and neither may erase the other's.
func SaveSettings(s Settings) error {
	st := readState()
	if s.empty() {
		st.Settings = nil
	} else {
		st.Settings = &s
	}
	return writeState(st)
}

// StateInfo is what can be PROVEN about the state file: where it is, whether
// it exists, what it holds, and when it last changed. Counts are read from the
// file, never from the live fleet — the settings pane reports the file, and a
// number taken from memory would be reporting something else.
type StateInfo struct {
	Path      string
	Exists    bool
	Groups    int
	Members   int
	Dirs      int
	Collapsed int
	ModTime   time.Time
}

// StateFile reports the state file's facts. A missing file is not an error: it
// is the normal state before the first group is made.
func StateFile() StateInfo {
	info := StateInfo{Path: groupsPath()}
	if info.Path == "" {
		return info
	}
	fi, err := os.Stat(info.Path)
	if err != nil {
		return info
	}
	info.Exists, info.ModTime = true, fi.ModTime()
	st := readState()
	info.Groups = len(st.Groups)
	for _, g := range st.Groups {
		info.Members += len(g.Members)
	}
	info.Dirs = len(st.Dirs)
	info.Collapsed = len(st.Collapsed)
	return info
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
func applyGroups(rows []railRow, groups []Group, dirs map[string]string) []railRow {
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
			if used[key] {
				continue // listed in two groups: the first one keeps it
			}
			used[key] = true
			b, ok := byKey[key]
			if !ok {
				// No session behind the name — but the name is still declared
				// here, and the user put it there. Dropping the row would lose
				// the declaration; showing it as a ghost keeps both facts and
				// makes the fleet survive a reboot.
				name := sessOf(key)
				members = append(members, railRow{
					depth: 1, ghost: true, flat: true,
					label: name, sess: name, backend: backendOf(key),
					group: g.Name, dir: dirs[key],
				})
				continue
			}
			for _, r := range b.rows {
				r.depth++
				r.group = g.Name
				members = append(members, r)
				// Ghosts never feed the header's marks: there is no process
				// there to have rung a bell or finished anything.
				if !r.isWin && !r.ghost {
					gr.bell = gr.bell || r.bell
					gr.done = gr.done || r.done
					gr.act = gr.act || r.act
					gr.inView = gr.inView || r.inView
				}
			}
		}
		gr.count, gr.ghostCount = countMembers(members)
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

// countMembers counts session rows (not windows) in a member list, live and
// ghost separately. A folded group reports them separately too — "2 ○1" is a
// different fleet from "3", and collapsing the two would be the inference this
// rail refuses to make.
func countMembers(rows []railRow) (live, ghosts int) {
	for _, r := range rows {
		if r.isWin {
			continue
		}
		if r.ghost {
			ghosts++
			continue
		}
		live++
	}
	return live, ghosts
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
// not accumulate entries for sessions that will never come back. Its recorded
// dir goes with it: a directory we are no longer declaring anything about is
// just stale evidence.
func (m *railModel) forgetMember(key string) {
	if groupOf(m.groups, key) == "" {
		return
	}
	m.groups = removeMember(m.groups, key)
	delete(m.dirs, key)
	m.saveState()
}

// saveState persists everything the rail owns: group membership, fold state,
// and the observed dirs of grouped members. Called after any change to any.
func (m *railModel) saveState() error { return saveState(m.groups, m.collapsed, m.dirs) }

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
