package rail

import (
	"errors"
	"sort"
	"strings"

	"github.com/1broseidon/ghostmux/internal/state"
)

// Group and Settings retain the rail package names used by the TUI while the
// state package owns their on-disk representation.
type Group = state.Group
type Settings = state.Settings

// memberKey namespaces persisted member identity as "tmux:<name>". The prefix
// stays on disk so state files survive across versions unchanged; keys written
// by the retired multi-backend prototype simply carry a different prefix.
func memberKey(sess string) string {
	return "tmux:" + sess
}

// foreignKey reports a persisted key from a backend this build no longer
// lists. Such members stay in the state file untouched but never render.
func foreignKey(key string) bool {
	b, _, ok := strings.Cut(key, ":")
	return ok && b != "tmux"
}

func sessOf(key string) string {
	_, sess, ok := strings.Cut(key, ":")
	if !ok {
		return key
	}
	return sess
}

func railState(doc state.Document) ([]Group, map[string]bool, map[string]string) {
	groups := make([]Group, len(doc.Groups))
	for i, group := range doc.Groups {
		groups[i] = group
		groups[i].Members = append([]string(nil), group.Members...)
	}
	collapsed := make(map[string]bool, len(doc.Collapsed))
	for _, key := range doc.Collapsed {
		collapsed[key] = true
	}
	dirs := make(map[string]string, len(doc.Dirs))
	for key, dir := range doc.Dirs {
		dirs[key] = dir
	}
	return groups, collapsed, dirs
}

func setRailDocument(doc *state.Document, groups []Group, collapsed map[string]bool, dirs map[string]string) {
	doc.Groups = cloneGroups(groups)
	doc.Collapsed = doc.Collapsed[:0]
	for key, folded := range collapsed {
		if folded {
			doc.Collapsed = append(doc.Collapsed, key)
		}
	}
	sort.Strings(doc.Collapsed)
	if len(dirs) == 0 {
		doc.Dirs = nil
	} else {
		doc.Dirs = cloneDirs(dirs)
	}
}

func cloneGroups(groups []Group) []Group {
	out := make([]Group, len(groups))
	for i, group := range groups {
		out[i] = group
		out[i].Members = append([]string(nil), group.Members...)
	}
	return out
}

func cloneCollapsed(collapsed map[string]bool) map[string]bool {
	out := make(map[string]bool, len(collapsed))
	for key, folded := range collapsed {
		if folded {
			out[key] = true
		}
	}
	return out
}

func cloneDirs(dirs map[string]string) map[string]string {
	out := make(map[string]string, len(dirs))
	for key, dir := range dirs {
		out[key] = dir
	}
	return out
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
func applyGroups(rows []railRow, groups []Group, dirs map[string]string, validity ...rowValidity) []railRow {
	if len(groups) == 0 {
		return rows
	}
	tmuxValidity := rowFresh
	if len(validity) > 0 {
		tmuxValidity = validity[0]
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
		blocks = append(blocks, block{key: memberKey(r.sess), rows: []railRow{r}})
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
				if foreignKey(key) {
					continue // retired backend's member: preserved on disk, never rendered
				}
				// A declaration becomes a real ghost only after tmux's latest
				// query authoritatively proved the name absent. During an
				// outage (or before first validation) it remains visible as `?`.
				name := sessOf(key)
				members = append(members, railRow{
					depth: 1, ghost: tmuxValidity == rowFresh, flat: true,
					label: name, sess: name,
					group: g.Name, dir: dirs[key], validity: tmuxValidity,
				})
				continue
			}
			for _, r := range b.rows {
				r.depth++
				r.group = g.Name
				members = append(members, r)
				// Ghosts and uncertain cache rows never feed the header's marks.
				if !r.isWin && !r.ghost && r.validity == rowFresh {
					gr.bell = gr.bell || r.bell
					gr.done = gr.done || r.done
					gr.act = gr.act || r.act
					gr.inView = gr.inView || r.inView
				}
			}
		}
		gr.count, gr.ghostCount, gr.uncertainCount = countMembers(members)
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

// countMembers counts session rows (not windows) as fresh live, authoritative
// ghosts, or uncertain backend state. Folded groups report all three apart.
func countMembers(rows []railRow) (live, ghosts, uncertain int) {
	for _, row := range rows {
		if row.isWin {
			continue
		}
		if row.validity != rowFresh {
			uncertain++
			continue
		}
		if row.ghost {
			ghosts++
			continue
		}
		live++
	}
	return live, ghosts, uncertain
}

// --- model actions ---

// organizationSnapshot captures the decision state and useful cursor identity
// immediately before an organization mutation. Directory evidence is
// intentionally not part of the snapshot.
func (m railModel) organizationSnapshot() organizationSnapshot {
	return snapshotOrganization(m.groups, m.collapsed, cursorIdentityAt(m.visible(), m.cursor))
}

func (m *railModel) registerOrganizationUndo(snapshot organizationSnapshot, action string) {
	m.organizationUndo = &organizationUndo{snapshot: snapshot, action: action}
}

func (m *railModel) invalidateOrganizationUndo() { m.organizationUndo = nil }

func (m *railModel) restoreCursor(id cursorIdentity) {
	if cursor, ok := selectionIndex(m.rows, m.collapsed, id); ok {
		m.cursor = cursor
	} else {
		m.clamp()
	}
}

// createGroup validates and persists a candidate before changing the model.
func (m *railModel) createGroup(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errBlankName
	}
	for _, group := range m.groups {
		if group.Name == name {
			return errDuplicateGroup
		}
	}
	snapshot := m.organizationSnapshot()
	groups := cloneGroups(m.groups)
	groups = append(groups, Group{Name: name})
	if err := m.persistRail(groups, m.collapsed, m.dirs); err != nil {
		return err
	}
	m.registerOrganizationUndo(snapshot, "create "+name)
	m.refreshWithoutCapture()
	m.flashInfo("created group · " + name + " · u undo")
	return nil
}

// deleteGroup persists removal before changing the visible group list. Its
// namespaced fold key is organization too, so deletion removes it and undo can
// restore it.
func (m *railModel) deleteGroup(name string) error {
	snapshot := m.organizationSnapshot()
	groups := make([]Group, 0, len(m.groups))
	found := false
	for _, group := range m.groups {
		if group.Name == name {
			found = true
			continue
		}
		groups = append(groups, group)
	}
	if !found {
		return nil
	}
	collapsed := cloneCollapsed(m.collapsed)
	delete(collapsed, groupKey(name))
	if err := m.persistRail(groups, collapsed, m.dirs); err != nil {
		return err
	}
	m.registerOrganizationUndo(snapshot, "delete "+name)
	m.refreshWithoutCapture()
	m.flashInfo("deleted group · " + name + " · u undo")
	return nil
}

// moveRow is the immediate expert path. It shares the pure helper with modal
// preview, but commits its one candidate immediately.
func (m *railModel) moveRow(dir int) error {
	vis := m.visible()
	if m.cursor < 0 || m.cursor >= len(vis) {
		return nil
	}
	row := vis[m.cursor]
	target, ok := organizationTargetOf(row)
	if !ok {
		return nil
	}
	groups, changed := moveOrganization(m.groups, target, dir)
	if !changed {
		return nil
	}
	snapshot := m.organizationSnapshot()
	if err := m.persistRail(groups, m.collapsed, m.dirs); err != nil {
		return err
	}
	label := row.sess
	if row.isGroup {
		label = row.label
	}
	m.registerOrganizationUndo(snapshot, "move "+label)
	m.refreshWithoutCapture()
	m.restoreCursor(targetCursorIdentity(target))
	m.flashInfo("moved · " + label + " · u undo")
	return nil
}

func findMember(groups []Group, key string) (int, int) {
	for gi, group := range groups {
		for mi, member := range group.Members {
			if member == key {
				return gi, mi
			}
		}
	}
	return -1, -1
}

func (m *railModel) findMember(key string) (int, int) { return findMember(m.groups, key) }

func (m *railModel) cursorToMember(key string) {
	for i, row := range m.visible() {
		if !row.isGroup && !row.isWin && memberKey(row.sess) == key {
			m.cursor = i
			return
		}
	}
	m.clamp()
}

func (m *railModel) cursorToGroup(name string) {
	for i, row := range m.visible() {
		if row.isGroup && row.label == name {
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

// forgetMember persists removal before changing the model. A successful
// forget is destructive rather than organizational: it invalidates undo so a
// later u cannot recreate the declaration.
func (m *railModel) forgetMember(key string) error {
	if groupOf(m.groups, key) == "" {
		return nil
	}
	groups := removeMember(cloneGroups(m.groups), key)
	dirs := cloneDirs(m.dirs)
	delete(dirs, key)
	if err := m.persistRail(groups, m.collapsed, dirs); err != nil {
		return err
	}
	m.invalidateOrganizationUndo()
	return nil
}

// toggleFold persists a candidate before changing disclosure state. Leaves
// are inert: Tab on a flat session or window must not create hidden state.
func (m *railModel) toggleFold(row railRow) error {
	if !structuralRow(row) {
		return nil
	}
	return m.setFold(row, !m.collapsed[foldKey(row)])
}

// setFold is the shared safe fold path for Tab, Enter, and semantic h/l. The
// candidate is committed before the model adopts it and reports success once,
// regardless of which semantic key reached this path.
func (m *railModel) setFold(row railRow, folded bool) error {
	if !structuralRow(row) {
		return nil
	}
	key := foldKey(row)
	if m.collapsed[key] == folded {
		return nil
	}
	snapshot := m.organizationSnapshot()
	collapsed := cloneCollapsed(m.collapsed)
	verb := "expanded"
	action := "expand " + row.label
	if folded {
		collapsed[key] = true
		verb = "collapsed"
		action = "collapse " + row.label
	} else {
		delete(collapsed, key)
	}
	if err := m.persistRail(m.groups, collapsed, m.dirs); err != nil {
		return err
	}
	m.registerOrganizationUndo(snapshot, action)
	m.restoreCursor(cursorIdentityOf(row))
	m.flashInfo(verb + " · " + row.label + " · u undo")
	return nil
}

// undoOrganization restores the last successful organization snapshot while
// retaining current directory evidence. It consumes itself only after the
// persist-before-apply Store update succeeds; there is deliberately no redo.
func (m *railModel) undoOrganization() error {
	if m.organizationUndo == nil {
		m.flashInfo("nothing to undo")
		return nil
	}
	undo := m.organizationUndo
	if err := m.persistRail(undo.snapshot.groups, undo.snapshot.collapsed, m.dirs); err != nil {
		return err
	}
	m.organizationUndo = nil
	m.refreshWithoutCapture()
	m.restoreCursor(undo.snapshot.cursor)
	m.flashInfo("undid · " + undo.action)
	return nil
}

var errStateConflict = errNamed("state changed in another panel; change not saved")

// railStore is the narrow Store boundary the rail needs. New still accepts the
// concrete shared state.Store; the interface keeps organization write-count
// and failure tests honest without changing the on-disk schema.
type railStore interface {
	Update(func(*state.Document) error) error
	Snapshot() state.Document
	Info() state.Info
	LoadError() error
}

func (m *railModel) ensureStore() railStore {
	if m.store != nil {
		return m.store
	}
	store, err := state.OpenDefault()
	m.store = store
	if err != nil {
		m.storageErr = "state read-only: " + store.Info().Status
	}
	return store
}

// persistRail commits a candidate through the shared Store, then adopts it.
// A conflict adopts the Store's current disk snapshot and rebuilds rows
// without running another storage mutation.
func (m *railModel) persistRail(groups []Group, collapsed map[string]bool, dirs map[string]string) error {
	store := m.ensureStore()
	err := store.Update(func(doc *state.Document) error {
		setRailDocument(doc, groups, collapsed, dirs)
		return nil
	})
	if err != nil {
		if errors.Is(err, state.ErrConflict) {
			// The Store has already adopted the external valid primary. Any undo
			// or modal draft was based on the superseded revision and is invalid.
			m.invalidateOrganizationUndo()
			m.mode, m.move = modeNormal, nil
			m.adoptStoreSnapshot()
			m.refreshWithoutCapture()
			return errStateConflict
		}
		if store.LoadError() != nil {
			m.storageErr = "state read-only: " + store.Info().Status
		}
		return err
	}
	m.groups, m.collapsed, m.dirs = railState(store.Snapshot())
	return nil
}

func (m *railModel) adoptStoreSnapshot() {
	if m.ensureStore() == nil {
		return
	}
	m.groups, m.collapsed, m.dirs = railState(m.store.Snapshot())
	if m.store.LoadError() != nil {
		m.storageErr = "state read-only: " + m.store.Info().Status
	}
}

// SyncState adopts the shared Store snapshot and rebuilds rows. The frame uses
// it after a settings conflict, when Store has already adopted another
// process's valid primary.
func (m Model) SyncState() Model {
	// SyncState is called only after another process's state was adopted. Draft
	// organization and its one-level undo both refer to the old revision.
	m.invalidateOrganizationUndo()
	m.mode, m.move = modeNormal, nil
	m.adoptStoreSnapshot()
	m.refreshWithoutCapture()
	return m
}
