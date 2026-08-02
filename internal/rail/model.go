package rail

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/1broseidon/ghostmux/internal/state"
	"github.com/1broseidon/ghostmux/internal/tmux"
)

type railTick time.Time
type blinkMsg time.Time

// mode selects which keymap Update() dispatches to and what the hint line
// shows (Tasks 8-9).
type mode int

const (
	modeNormal mode = iota
	modeFilter
	modeCreate
	modeKillConfirm
	modeGroup
	modeMove
)

// killKind is what `x` would actually destroy. The key is one key, but the
// four outcomes are not variations of each other — killing a running session,
// unmaking a shelf, dropping a declaration, and deleting a backend's
// serialized session take away four different things, and the confirm prompt
// has to name the one it is asking about.
type killKind int

const (
	killLive killKind = iota
	killUngroup
	killForget
)

// verb is the word the confirm prompt uses.
func (k killKind) verb() string {
	switch k {
	case killUngroup:
		return "ungroup"
	case killForget:
		return "forget"
	}
	return "kill"
}

// kindOf is a pure interpretation of row provenance. Fresh exact-name
// validation happens only after confirmation, immediately before mutation.
func kindOf(r railRow) killKind {
	switch {
	case r.isGroup:
		return killUngroup
	case r.ghost:
		return killForget
	default:
		return killLive
	}
}

// tmuxCache retains the last successful snapshot so a query outage degrades
// to stale rows instead of an empty rail.
type tmuxCache struct {
	snapshot    tmux.Snapshot
	hasSnapshot bool
	enabled     bool
	lastErr     error
}

// tmuxPresent reports whether tmux is installed; injectable for tests.
var tmuxPresent = func() bool { _, err := exec.LookPath("tmux"); return err == nil }

var (
	errBackendActionDisabled     = errNamed("backend unavailable; action disabled")
	errTmuxExecutableUnavailable = errNamed("tmux executable not found")
)

// foldKey is the collapse-map key for a row: groups are namespaced so a group
// and a session sharing a name never share disclosure state.
func foldKey(r railRow) string {
	if r.isGroup {
		return groupKey(r.label)
	}
	return r.sess
}

type railModel struct {
	rows         []railRow
	cursor       int // index into visible() rows, not raw rows
	height       int
	hub          string                // session the rail lives in — excluded from the tree
	vp           Viewport              // where selections render (pane / embedded pty)
	viewportDead bool                  // pane was dead this tick — swap the hint line
	viewportErr  string                // persistent typed-probe failure from Heal
	done         *doneTracker          // per-pane command-transition tracking (D5)
	activity     activityLedger        // stable window ID → panel-local unread state
	pulse        map[string]*pulseRing // stable window ID → output cadence ring
	unread       map[string]unreadInfo // stable window ID → banked unseen lines
	attached     map[string]bool       // session name → attached elsewhere
	blinking     bool                  // 400ms blink timer running (D7)
	blinkPhase   int                   // bell blink phase, mod 3 (glyph hidden on phase 2)

	collapsed map[string]bool // session name → collapsed in the rail (Task 7)

	mode         mode
	filterQuery  string          // active filter substring (Task 8)
	killTarget   string          // session or group pending `x` confirmation
	killInstance string          // tmux #{session_id} captured from the armed row
	killKind     killKind        // what `x` is about to destroy, captured at press
	input        textinput.Model // shared prompt editor for `/` and `n` modes

	errMsg    string    // last action error, shown on the hint line
	errUntil  time.Time // errMsg clears once time.Now() passes this
	infoMsg   string    // last successful/neutral action, shown for two seconds
	infoUntil time.Time

	tmuxCache tmuxCache

	// store is shared with the frame's settings model. storageErr remains visible
	// while a primary that failed to load keeps the Store read-only.
	store      railStore
	storageErr string

	// groups is persisted organization. A modal move renders move.draft without
	// assigning that draft here; organizationUndo is one level and has no redo.
	groups           []Group
	move             *moveState
	organizationUndo *organizationUndo

	// dirs is the observed working directory of each grouped member, memberKey
	// → path. Evidence, recorded while the session is alive, so that when it
	// dies the ghost can say where it will come back.
	dirs map[string]string

	lastViewed   viewRef // exact backend/session/window last followed by the cursor
	currentView  viewRef // latest non-idle viewport session, including its window
	previousView viewRef // prior backend-qualified session for the backtick toggle
}

// Model is the rail's public face for composition: the solo frame embeds it
// as a child bubbletea model, sharing the exact keymap/modes/refresh brain
// classic runs.
type Model = railModel

// New builds a rail model over a Viewport and Store. The optional form keeps
// internal callers compatible while opening one default Store for that model.
func New(vp Viewport, stores ...*state.Store) Model {
	var store *state.Store
	if len(stores) > 0 {
		store = stores[0]
	}
	var openErr error
	if store == nil {
		store, openErr = state.OpenDefault()
	}
	groups, collapsed, dirs := railState(store.Snapshot())
	m := Model{
		vp: vp, store: store, groups: groups, collapsed: collapsed, dirs: dirs,
		done: newDoneTracker(),
	}
	if openErr != nil || store.LoadError() != nil {
		m.storageErr = "state read-only: " + store.Info().Status
	}
	// Bootstrap both caches without done marks, directory writes, or viewport
	// synchronization. The first regular tick owns those side effects.
	m.refreshState(false)
	return m
}

// InHost tells the rail which tmux session it is itself running inside, so it
// never lists — or nests into — its own host. Live tmux data is rediscovered
// and saved organization comes from Store, so a frame can be relaunched
// anywhere. Excluding its host keeps that relaunch safe.
func (m Model) InHost(sess string) Model {
	if sess == "" {
		return m
	}
	m.hub = sess
	m.rebuildRows()
	return m
}

func railTicker() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return railTick(t) })
}

// blinkTicker drives the bell blink; it runs only while a bell exists (D7).
func blinkTicker() tea.Cmd {
	return tea.Tick(400*time.Millisecond, func(t time.Time) tea.Msg { return blinkMsg(t) })
}

func (m railModel) Init() tea.Cmd { return railTicker() }

func (m railModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.collapsed == nil {
		m.collapsed = map[string]bool{}
	}
	switch msg := msg.(type) {
	case railTick:
		m.refresh()
		m.healViewport()
		m.clamp()
		debugRefresh("tick")
		return m, tea.Batch(railTicker(), m.maybeBlink())
	case refreshMsg:
		m.refresh()
		m.healViewport()
		m.clamp()
		debugRefresh("event")
		return m, m.maybeBlink()
	case blinkMsg:
		if !anyBell(m.rows) {
			m.blinking = false // bells cleared: stop the timer
			return m, nil
		}
		m.blinkPhase = (m.blinkPhase + 1) % 3
		return m, blinkTicker()
	case tea.WindowSizeMsg:
		m.height = msg.Height
		return m, nil
	case tea.MouseMsg:
		if m.mode == modeMove {
			return m, nil
		}
		return m.updateMouse(msg)
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		switch m.mode {
		case modeFilter:
			return m.updateFilterKey(msg)
		case modeCreate:
			return m.updateCreateKey(msg)
		case modeKillConfirm:
			return m.updateKillConfirmKey(msg)
		case modeGroup:
			return m.updateGroupKey(msg)
		case modeMove:
			return m.updateMoveKey(msg)
		default:
			return m.updateNormalKey(msg)
		}
	}
	return m, nil
}

// visible returns the current tree with collapse applied — what the cursor
// walks and the View renders (Task 7).
func (m railModel) visible() []railRow {
	return visibleRows(m.rows, m.collapsed)
}

// updateNormalKey handles every key in the default mode.
func (m railModel) updateNormalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "j", "down":
		m.moveCursor(1)
	case "k", "up":
		m.moveCursor(-1)
	case "enter":
		if vis := m.visible(); m.cursor < len(vis) {
			m.activateRow(vis[m.cursor])
		}
	case "a":
		// Name the shelf before you fill it: empty groups are legal.
		m.mode = modeGroup
		m.input = newPromptInput()
		m.errMsg = ""
		return m, textinput.Blink
	case "m":
		m.startMove()
	case "u":
		if err := m.undoOrganization(); err != nil {
			m.flashError(err)
		}
	case "J", "K":
		dir := 1
		if msg.String() == "K" {
			dir = -1
		}
		if err := m.moveRow(dir); err != nil {
			m.flashError(err)
		}
	case "h":
		m.semanticLeft()
	case "l":
		m.semanticRight()
	case "right":
		m.vp.FocusViewport()
	case "`":
		m.viewPrevious()
	case "]":
		m.returnOldest()
	case "d":
		m.vp.Detach()
		m.flashInfo("viewport detached")
		m.refresh()
	case "n":
		return m.startCreate()
	case "x":
		if vis := m.visible(); m.cursor < len(vis) {
			row := vis[m.cursor]
			if !row.isGroup && row.validity != rowFresh {
				m.flashError(errBackendActionDisabled)
				return m, nil
			}
			m.mode = modeKillConfirm
			m.killTarget = row.sess
			m.killInstance = row.instanceID
			m.killKind = kindOf(row)
			m.errMsg = ""
		}
	case "S":
		// The fleet verb: one press brings a whole declared workspace back.
		if vis := m.visible(); m.cursor < len(vis) {
			if err := m.summonGroup(vis[m.cursor]); err != nil {
				m.flashError(err)
			}
		}
	case "/":
		m.mode = modeFilter
		m.input = newPromptInput()
		m.input.SetValue(m.filterQuery)
		m.errMsg = ""
		return m, textinput.Blink
	case "esc":
		if m.filterQuery != "" {
			m.filterQuery = ""
			m.clamp()
		}
	}
	return m, nil
}

// InPrompt reports whether the rail is mid-prompt: typing a filter, a name, or
// answering a confirm. The frame asks before claiming `?` and `,`, because a
// key it steals while the user is typing is a character that silently never
// arrives — the one failure mode a reserved key must never have.
func (m railModel) InPrompt() bool { return m.mode != modeNormal }

// newPromptInput builds the shared hint-line editor for `/` and `n` modes —
// a real bubbles textinput: arrow keys, ctrl+a/e, word-wise editing, paste.
func newPromptInput() textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 48
	ti.Width = railWidth - 6
	ti.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(hexSessionName))
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(hexSessionName))
	ti.Focus()
	return ti
}

// updateFilterKey handles keys while typing a filter query (`/`, Task 8).
func (m railModel) updateFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.filterQuery = ""
		m.mode = modeNormal
		m.clamp()
		return m, nil
	case "enter":
		m.mode = modeNormal // keeps the filter active; second esc clears it
		m.clamp()
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.filterQuery = m.input.Value() // live: dimming follows every keystroke
	return m, cmd
}

// updateCreateKey handles keys while typing a new-session name (`n`, Task 9).
func (m railModel) updateCreateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeNormal
		return m, nil
	case "enter":
		name := m.input.Value()
		m.mode = modeNormal
		if err := m.createSession(name); err != nil {
			m.flashError(err)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// updateGroupKey handles keys while typing a new group name (`a`).
func (m railModel) updateGroupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeNormal
		return m, nil
	case "enter":
		name := m.input.Value()
		m.mode = modeNormal
		if err := m.createGroup(name); err != nil {
			m.flashError(err)
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// startMove normalizes any window to its backend-qualified session and starts
// a state-only draft. Liveness is deliberately irrelevant: declarations and
// stale rows remain organizable without a backend action.
func (m *railModel) startMove() {
	vis := m.visible()
	if m.cursor < 0 || m.cursor >= len(vis) {
		m.flashInfo("nothing to move")
		return
	}
	row := vis[m.cursor]
	target, ok := organizationTargetOf(row)
	if !ok {
		m.flashInfo("row cannot move")
		return
	}
	label := row.sess
	if row.isGroup {
		label = row.label
	}
	original := cloneGroups(m.groups)
	m.move = &moveState{
		target: target, original: original, draft: cloneGroups(original), label: label,
		cursor: cursorIdentityOf(row),
	}
	m.mode = modeMove
	m.restoreCursor(targetCursorIdentity(target))
}

// updateMoveKey previews any number of pure one-step moves. Enter performs at
// most one Store update; Esc discards the draft without writing.
func (m railModel) updateMoveKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.move == nil {
		m.mode = modeNormal
		m.rebuildRows()
		return m, nil
	}
	switch msg.String() {
	case "j", "down", "J":
		m.previewMove(1)
	case "k", "up", "K":
		m.previewMove(-1)
	case "esc":
		target := m.move.target
		m.mode, m.move = modeNormal, nil
		m.rebuildRows()
		m.restoreCursor(targetCursorIdentity(target))
	case "enter":
		move := m.move
		target, label := move.target, move.label
		if !move.dirty {
			m.mode, m.move = modeNormal, nil
			m.rebuildRows()
			m.restoreCursor(targetCursorIdentity(target))
			m.flashInfo("not moved · " + label)
			return m, nil
		}
		snapshot := snapshotOrganization(move.original, m.collapsed, move.cursor)
		candidate := cloneGroups(move.draft)
		if err := m.persistRail(candidate, m.collapsed, m.dirs); err != nil {
			// Conflict already exited and rebuilt from the external snapshot.
			// Other errors discard only the draft and restore persisted display.
			if m.mode == modeMove {
				m.mode, m.move = modeNormal, nil
				m.rebuildRows()
				m.restoreCursor(targetCursorIdentity(target))
			}
			m.flashError(err)
			return m, nil
		}
		m.registerOrganizationUndo(snapshot, "move "+label)
		m.mode, m.move = modeNormal, nil
		m.refreshWithoutCapture()
		m.restoreCursor(targetCursorIdentity(target))
		m.flashInfo("moved · " + label + " · u undo")
	}
	return m, nil
}

func (m *railModel) previewMove(dir int) {
	if m.move == nil {
		return
	}
	draft, changed := moveOrganization(m.move.draft, m.move.target, dir)
	if !changed {
		return
	}
	m.move.draft = draft
	m.move.dirty = !groupsEqual(m.move.original, draft)
	m.rebuildRows()
	m.restoreCursor(targetCursorIdentity(m.move.target))
}

// updateKillConfirmKey handles the y/n confirmation after `x` (Task 9).
func (m railModel) updateKillConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y":
		target, instance, kind := m.killTarget, m.killInstance, m.killKind
		m.mode = modeNormal
		m.killTarget, m.killInstance, m.killKind = "", "", killLive
		if kind != killUngroup {
			if err := validateDestructiveAction(kind, target, instance); err != nil {
				// Rebuild from a fresh query, but do not let a cancelled
				// destructive action trigger done/dir/window side effects.
				m.refreshWithoutCapture()
				m.flashError(err)
				return m, nil
			}
		}
		var err error
		switch kind {
		case killUngroup:
			err = m.deleteGroup(target)
		case killForget:
			err = m.forgetMember(memberKey(target))
			m.refresh()
		default:
			err = m.killSessionInstance(target, instance)
		}
		if err != nil {
			m.flashError(err)
		}
	case "n", "esc":
		m.mode = modeNormal
		m.killTarget, m.killInstance, m.killKind = "", "", killLive
	}
	return m, nil
}

// validateDestructiveAction performs the fresh exact-name check required after
// confirmation and immediately before any tmux command or state mutation.
func validateDestructiveAction(kind killKind, name, armedInstance string) error {
	present, currentInstance, err := tmux.ProbeSessionInstance(name)
	if err != nil {
		return fmt.Errorf("tmux unavailable: %w", err)
	}

	valid := false
	switch kind {
	case killLive:
		valid = present && armedInstance != "" && currentInstance == armedInstance
	case killForget:
		valid = !present
	}
	if !valid {
		return fmt.Errorf("session state changed; %s cancelled", kind.verb())
	}
	return nil
}

// updateMouse handles clicks and wheel scroll now that the rail owns mouse
// reporting for its pane. Click selects a row; a click on the already-
// selected row views it (same as ↵). Drags are consumed and ignored — that
// is the point: they no longer fall through to tmux copy-mode.
func (m railModel) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Button == tea.MouseButtonWheelUp:
		m.moveCursor(-1)
	case msg.Button == tea.MouseButtonWheelDown:
		m.moveCursor(1)
	case msg.Action == tea.MouseActionPress && msg.Button == tea.MouseButtonLeft:
		idx, ok := m.rowAt(msg.Y)
		if !ok {
			return m, nil
		}
		if idx == m.cursor {
			if vis := m.visible(); idx < len(vis) {
				m.activateRow(vis[idx])
			}
			return m, nil
		}
		m.cursor = idx
	}
	return m, nil
}

// rowAt maps a screen line to a visible-row index. It is the inverse of what
// View draws, so it reads treeTop/treeHeight from the same place View does —
// see the note on treeHeight.
func (m railModel) rowAt(y int) (int, bool) {
	vis := m.visible()
	start, end, moreUp, _ := scrollWindow(len(vis), m.treeHeight(), m.cursor)
	line := y - treeTop
	if moreUp > 0 {
		line-- // first tree line is the ↑ indicator
	}
	idx := start + line
	if line < 0 || idx < start || idx >= end {
		return 0, false
	}
	return idx, true
}

// moveCursor steps the cursor by step (±1) over the visible rows, skipping
// rows dimmed by an active filter (Task 8). It stays put if no matching row
// exists in that direction.
func (m *railModel) moveCursor(step int) {
	vis := m.visible()
	n := len(vis)
	if n == 0 {
		m.cursor = 0
		return
	}
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	c := m.cursor
	for {
		nc := c + step
		if nc < 0 || nc >= n {
			return // no matching row that direction: stay put
		}
		c = nc
		if m.filterQuery == "" || matchesFilter(vis[c], m.filterQuery) {
			m.cursor = c
			return
		}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (m *railModel) movePage(delta int) {
	vis := m.visible()
	m.cursor = physicalMoveIndex(vis, m.cursor, delta, m.filterQuery)
}

func (m *railModel) moveNonWindow(dir int) {
	vis := m.visible()
	m.cursor = nonWindowMoveIndex(vis, m.cursor, dir, m.filterQuery)
}

// semanticLeft implements tree-style h: collapse an open structural row,
// otherwise move to the nearest visible parent.
func (m *railModel) semanticLeft() {
	vis := m.visible()
	if m.cursor < 0 || m.cursor >= len(vis) {
		return
	}
	r := vis[m.cursor]
	// Collapse and parent selection are organization-only. They remain safe on
	// stale or never-validated rows because neither path touches a backend.
	if structuralRow(r) && !r.collapsed {
		if err := m.setFold(r, true); err != nil {
			m.flashError(err)
		}
		return
	}
	if parent := visibleParentIndex(vis, m.cursor); parent >= 0 {
		m.cursor = parent
	}
}

// semanticRight expands a folded structural row. On a leaf it previews the
// exact live target, or hands focus to an already viewed target.
func (m *railModel) semanticRight() {
	vis := m.visible()
	if m.cursor < 0 || m.cursor >= len(vis) {
		return
	}
	r := vis[m.cursor]
	// Structural expansion is state-only and does not depend on backend
	// freshness. Validity gates only the leaf preview/focus path below.
	if structuralRow(r) {
		if r.collapsed {
			if err := m.setFold(r, false); err != nil {
				m.flashError(err)
			}
		}
		return
	}
	if r.isGroup || r.ghost {
		return
	}
	if r.validity != rowFresh {
		m.flashError(errBackendActionDisabled)
		return
	}
	if rowExactView(r, m.vp.Lock()) {
		m.vp.FocusViewport()
		return
	}
	m.pointRow(r)
	if !rowExactView(r, m.vp.Lock()) {
		m.flashError(errNamed("view unavailable"))
		return
	}
	m.followViewport()
	m.flashInfo("viewing " + viewTargetName(r))
}

func (m *railModel) observeViewport(lock ViewState) {
	ref := viewRefOf(lock)
	if ref.Sess == "" {
		return // deliberate idle/detach retains the two-session history
	}
	if m.currentView.Sess == "" {
		m.currentView = ref
		return
	}
	if m.currentView.sameSession(ref) {
		m.currentView.Win = ref.Win
		return
	}
	m.previousView = m.currentView
	m.currentView = ref
}

func (m *railModel) viewPrevious() {
	if m.vp == nil {
		m.flashError(errNamed("previous view unavailable"))
		return
	}
	m.observeViewport(m.vp.Lock())
	if m.previousView.Sess == "" {
		m.flashError(errNamed("no previous session"))
		return
	}
	target, resolution := resolveViewRef(m.rows, m.previousView)
	switch resolution {
	case viewGhost:
		m.flashError(errNamed("previous session not live"))
		return
	case viewUnavailable:
		m.flashError(errNamed("previous session unavailable"))
		return
	case viewMissing:
		m.flashError(errNamed("previous view missing"))
		return
	}
	requested := m.previousView
	m.pointRow(target)
	lock := m.vp.Lock()
	if lock.Sess != requested.Sess {
		m.flashError(errNamed("previous view unavailable"))
		return
	}
	m.followViewport() // observes the verified change and swaps the two refs
	m.flashInfo("previous · " + viewTargetName(target))
}

// returnOldest is the Return Queue verb: one press views the oldest window
// that rang (●) or finished (✓) while unseen — the fleet as an inbox, drained
// oldest-first. Viewing clears the mark through the paths that already exist
// (native bell clear, clearViewedDone, the activity ledger), so the next
// press walks to the next-oldest with no queue state of its own.
func (m *railModel) returnOldest() {
	// m.rows, not visible(): a fold hides rows from the eye, not from the
	// queue — attention inside a collapsed group must still be reachable.
	// Agents outrank plain jobs: a finished agent is usually the next thing
	// to steer, a finished build is usually just news. Oldest-first within
	// each tier. Agent-ness is the same ambient foreground-command evidence
	// that drives the accent — never a naming convention.
	better := func(a, b railRow) bool {
		if aa, ba := isAgentCmd(a.cmd), isAgentCmd(b.cmd); aa != ba {
			return aa
		}
		return a.activityAt < b.activityAt
	}
	best := -1
	for i, r := range m.rows {
		if !attentionLeaf(r) || (!r.bell && !r.done) {
			continue
		}
		if best < 0 || better(r, m.rows[best]) {
			best = i
		}
	}
	if best < 0 {
		m.flashInfo("queue empty")
		return
	}
	target := m.rows[best]
	m.pointRow(target)
	if !rowExactView(target, m.vp.Lock()) {
		m.flashError(errNamed("view unavailable"))
		return
	}
	m.refresh() // re-observes marks post-view and follows the viewport
	m.flashInfo("return · " + viewTargetName(target))
}

// unreadPeekCap bounds one peek fetch. The COUNT is always exact (ledger
// arithmetic); only the text is capped — a 5,000-line dump is not a peek.
const unreadPeekCap = 200

// UnreadPeek returns the unseen tail of the selected row's window: the count
// from the ledger, the text captured lazily — only when the operator asks.
// ok is false when the selected row has nothing banked.
func (m Model) UnreadPeek() (title string, lines []string, ok bool) {
	vis := m.visible()
	if m.cursor < 0 || m.cursor >= len(vis) {
		return "", nil, false
	}
	r := vis[m.cursor]
	info, has := m.unread[r.windowID]
	if r.windowID == "" || !has || info.count == 0 || info.pane == "" {
		return "", nil, false
	}
	fetch := info.count
	if fetch > unreadPeekCap {
		fetch = unreadPeekCap
	}
	captured, err := tmux.CaptureTail(info.pane, fetch)
	if err != nil {
		return "", nil, false
	}
	title = viewTargetName(r) + " · +" + itoa(info.count) + " unseen"
	if info.alt {
		// A full-screen program's history says less; the title says so
		// instead of letting the peek overclaim.
		title += " · TUI"
	}
	if info.count > unreadPeekCap {
		title += " (last " + itoa(unreadPeekCap) + ")"
	}
	return title, captured, true
}

// flashError records an action error for the hint-line flash (3s, Task 9).
func (m *railModel) flashError(err error) {
	m.errMsg = err.Error()
	m.errUntil = time.Now().Add(3 * time.Second)
	m.infoMsg, m.infoUntil = "", time.Time{}
}

// errorActive reports whether the error flash is still within its window.
func (m railModel) errorActive() bool {
	return m.errMsg != "" && time.Now().Before(m.errUntil)
}

// flashInfo records successful or neutral action feedback for two seconds.
func (m *railModel) flashInfo(message string) {
	m.errMsg, m.errUntil = "", time.Time{}
	m.infoMsg = message
	m.infoUntil = time.Now().Add(2 * time.Second)
}

func (m railModel) infoActive() bool {
	return m.infoMsg != "" && time.Now().Before(m.infoUntil)
}

// startCreate opens the new-session prompt.
func (m railModel) startCreate() (tea.Model, tea.Cmd) {
	m.mode = modeCreate
	m.input = newPromptInput()
	m.errMsg = ""
	return m, textinput.Blink
}

// createSession creates a tmux session and points the viewport at it (Task 9).
func (m *railModel) createSession(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name required")
	}
	if err := tmux.Run("new-session", "-d", "-s", name, "-c", m.createDir()); err != nil {
		return err
	}
	m.refresh()
	m.vp.Point(name, "", false)
	m.refresh()
	return nil
}

// createDir resolves where a new tmux session starts: home by default, or the
// viewport session's proven active-pane cwd when the operator chose
// "current". An empty lock, an unproven path, or a vanished directory all
// fall back to home rather than failing the create.
func (m *railModel) createDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "~"
	}
	if CreateDir() != CreateDirCurrent || m.vp == nil {
		return home
	}
	lock := m.vp.Lock()
	if lock.Sess == "" {
		return home
	}
	for _, s := range m.tmuxCache.snapshot.Sessions {
		if s.Name != lock.Sess || s.CurrentPath == "" {
			continue
		}
		if _, err := os.Stat(s.CurrentPath); err == nil {
			return s.CurrentPath
		}
	}
	return home
}

// killSession is the compatibility name-targeted boundary used by direct
// callers. Confirmed UI kills use killSessionInstance with the armed stable ID.
func (m *railModel) killSession(name string) error {
	return m.killSessionInstance(name, "")
}

// killSessionInstance kills the validated tmux instance by stable ID. If the
// name is deleted and recreated after validation, the old ID is absent and the
// command fails safely instead of targeting the replacement by name.
func (m *railModel) killSessionInstance(name, instance string) error {
	if instance != "" {
		killed, err := tmux.KillSessionIfInstance(name, instance)
		if err != nil {
			return err
		}
		if !killed {
			return fmt.Errorf("session state changed; kill cancelled")
		}
	} else if err := tmux.Run("kill-session", "-t", "="+name); err != nil {
		return err
	}
	m.invalidateOrganizationUndo()
	m.vp.OnKill(name)
	if err := m.forgetMember(memberKey(name)); err != nil {
		m.refresh()
		return err
	}
	m.refresh()
	return nil
}

// summonGroup is `S` on a group header: start every dead member at once. No
// confirm, because creating a session takes nothing away — and a fleet that
// needs six confirmations is not the one keystroke this is supposed to be.
// The viewport is left alone: S is about the fleet, not about what you are
// looking at. On any other row it does nothing; ↵ already says it better.
func (m *railModel) summonGroup(r railRow) error {
	if !r.isGroup {
		return nil
	}
	var failures []string
	uncertain := 0
	// m.rows, not visible(): a folded group is still a fleet, and S must not
	// mean something different depending on a disclosure triangle.
	for _, row := range m.rows {
		if row.isGroup || row.isWin || row.group != r.label {
			continue
		}
		if row.validity != rowFresh {
			uncertain++
			continue
		}
		if !row.ghost {
			continue
		}
		dir, _ := summonDir(row.dir)
		if err := tmux.Run("new-session", "-d", "-s", row.sess, "-c", dir); err != nil {
			failures = append(failures, row.sess+": "+err.Error())
		}
	}
	m.refresh()
	if uncertain > 0 {
		failures = append(failures, fmt.Sprintf("skipped %d uncertain member(s)", uncertain))
	}
	if len(failures) > 0 {
		return fmt.Errorf("group start: %s", strings.Join(failures, "; "))
	}
	return nil
}

// refresh queries each installed backend independently, retains failed
// backends' last successful snapshots, and runs tmux side effects only from a
// candidate that succeeded on this refresh.
func (m *railModel) refresh() { m.refreshState(true) }

// refreshWithoutCapture is a side-effect-free fleet rebuild. It is used after
// state conflicts and cancelled destructive validations.
func (m *railModel) refreshWithoutCapture() { m.refreshState(false) }

func (m *railModel) refreshState(sideEffects bool) {
	tmuxFresh := m.refreshTmuxCache()
	moving := m.mode == modeMove && m.move != nil

	if tmuxFresh {
		snapshot := m.tmuxCache.snapshot
		m.attached = attachedMap(snapshot.Sessions)
		if sideEffects {
			if m.done != nil {
				m.done.observe(snapshot.Windows, snapshot.Sessions, m.hub, m.suppressDone)
			}
			// Moving is a state-only transaction. Ticks may update backend
			// evidence, but must not introduce a Store write into its preview.
			if !moving {
				m.captureDirs(snapshot.Sessions)
			}
			if m.vp != nil {
				m.vp.SyncActiveWindow(snapshot.Windows)
			}
		}
	}
	m.rebuildRows()
	if moving {
		m.restoreCursor(targetCursorIdentity(m.move.target))
		return // never auto-follow the viewport away from the moved target
	}
	m.followViewport()
}

func (m *railModel) refreshTmuxCache() bool {
	installed := tmuxPresent()
	if !installed {
		m.done.reset()
		if !m.tmuxCache.hasSnapshot {
			// Initial not-installed is a disabled backend, not an outage.
			m.tmuxCache.enabled = false
			m.tmuxCache.lastErr = nil
			return false
		}
		// A backend that previously succeeded remains enabled as a stale cache.
		// This distinguishes executable disappearance from initial absence and
		// lets the next successful lookup recover normally.
		m.tmuxCache.enabled = true
		m.tmuxCache.lastErr = errTmuxExecutableUnavailable
		return false
	}
	m.tmuxCache.enabled = true
	snapshot, err := tmux.Query()
	if err != nil {
		m.tmuxCache.lastErr = err
		m.done.reset()
		return false
	}
	m.observeActivity(snapshot.Windows)
	m.tmuxCache.snapshot = snapshot
	m.tmuxCache.hasSnapshot = true
	m.tmuxCache.lastErr = nil
	return true
}

func (m railModel) tmuxValidity() rowValidity {
	if !m.tmuxCache.enabled || !m.tmuxCache.hasSnapshot {
		return rowUnvalidated
	}
	if m.tmuxCache.lastErr != nil {
		return rowStale
	}
	return rowFresh
}

func (m *railModel) rebuildRows() {
	lock := ViewState{}
	if m.vp != nil {
		lock = m.vp.Lock()
	}
	groups := m.groups
	if m.mode == modeMove && m.move != nil {
		groups = m.move.draft
	}
	var rows []railRow
	if m.tmuxCache.enabled && m.tmuxCache.hasSnapshot {
		snapshot := m.tmuxCache.snapshot
		rows = stampValidity(buildRows(m.hub, lock, snapshot.Sessions, snapshot.Windows), m.tmuxValidity())
	}
	m.rows = applyGroups(rows, groups, m.dirs, m.tmuxValidity())
}

// backendStatus is persistent while an enabled query or viewport probe is
// failing. It clears on the next successful refresh.
func (m railModel) backendStatus() string {
	var status []string
	if m.tmuxCache.enabled && m.tmuxCache.lastErr != nil {
		text := "tmux unavailable"
		if m.tmuxCache.hasSnapshot {
			text += "; showing last snapshot"
		}
		status = append(status, text)
	}
	if m.viewportErr != "" {
		duplicate := false
		for _, existing := range status {
			if strings.HasPrefix(existing, strings.TrimSuffix(m.viewportErr, ":")) ||
				strings.HasPrefix(m.viewportErr, strings.Fields(existing)[0]+" unavailable") {
				duplicate = true
				break
			}
		}
		if !duplicate {
			status = append(status, m.viewportErr)
		}
	}
	return strings.Join(status, " · ")
}

func (m *railModel) healViewport() {
	if m.vp == nil {
		m.viewportDead, m.viewportErr = false, ""
		return
	}
	dead, err := m.vp.Heal()
	m.viewportDead = dead
	if err != nil {
		m.viewportErr = err.Error()
		return
	}
	m.viewportErr = ""
}

// captureDirs records where each grouped tmux session is actually running. It
// is the whole of the ghost's memory, and it is taken from evidence while the
// session lives rather than asked for up front — a declaration the user never
// has to write. Only grouped members are recorded: an ungrouped session is
// cattle, and remembering its dir would be storage nobody asked for.
//
// Which path is evidence depends on Settings.GhostDir: launch (default) uses
// #{session_path}; last uses the active pane's #{pane_current_path}. An empty
// observation never clears a previously recorded dir.
func (m *railModel) captureDirs(sessions []tmux.Session) {
	dirs := cloneDirs(m.dirs)
	changed := false
	last := GhostDir() == GhostDirLast
	for _, session := range sessions {
		path := session.Path
		if last {
			path = session.CurrentPath
		}
		if path == "" {
			continue
		}
		key := memberKey(session.Name)
		if groupOf(m.groups, key) == "" || dirs[key] == path {
			continue
		}
		dirs[key] = path
		changed = true
	}
	if changed {
		if err := m.persistRail(m.groups, m.collapsed, dirs); err != nil {
			m.flashError(err)
		}
	}
}

// followViewport moves the rail cursor to the row the viewport is showing,
// once per backend-qualified viewed-window change. Inner tmux window changes
// update the current exact ref without replacing previous-session history.
func (m *railModel) followViewport() {
	if m.vp == nil {
		return
	}
	lock := m.vp.Lock()
	m.observeViewport(lock)
	if lock.Sess == "" {
		return
	}
	ref := viewRefOf(lock)
	if ref == m.lastViewed {
		return
	}
	m.lastViewed = ref

	best := -1
	targetGroup := ""
	for _, r := range m.rows {
		if !r.isGroup && r.sess == lock.Sess {
			targetGroup = r.group
			break
		}
	}
	for i, r := range m.visible() {
		if r.isGroup {
			if targetGroup != "" && r.label == targetGroup {
				best = i // a folded group stands in for its hidden viewed member
			}
			continue
		}
		if r.sess != lock.Sess {
			continue
		}
		if (r.isWin || r.flat) && r.window == lock.Win {
			best = i
			break
		}
		if !r.isWin {
			best = i // a collapsed session stands in for its hidden window
		}
	}
	if best >= 0 {
		m.cursor = best
	}
}

// activateRow is what ↵ and a second click on the selected row both do: view a
// session, fold a group. One method, because the keyboard and the mouse
// disagreeing is a bug the user finds and the tests don't — a click on a group
// used to try attaching to a session named after it.
func (m *railModel) activateRow(r railRow) {
	if r.isGroup {
		if err := m.toggleFold(r); err != nil {
			m.flashError(err)
		}
		return
	}
	if r.validity != rowFresh {
		m.flashError(errBackendActionDisabled)
		return
	}
	// A ghost has nothing to attach to yet, so ↵ means summon. It routes
	// through here rather than through the key handler for the same reason
	// folding does: the mouse must get the identical behaviour for free.
	if r.ghost {
		if err := m.summonRow(r); err != nil {
			m.flashError(err)
		}
		m.refresh()
		return
	}
	m.pointRow(r)
	m.refresh()
}

// summonRow brings a declared-but-dead name back. Nothing here is a restore:
// a ghost becomes a NEW session with the declared name in the recorded dir —
// which is the whole of what the row was claiming. No layout replay, no
// command replay.
func (m *railModel) summonRow(r railRow) error {
	if r.validity != rowFresh {
		return errBackendActionDisabled
	}
	dir, gone := summonDir(r.dir)
	if err := tmux.Run("new-session", "-d", "-s", r.sess, "-c", dir); err != nil {
		// A name that already exists is not a failure here: the session sprang
		// to life between render and keypress. Probe through the typed boundary
		// so backend failure cannot masquerade as authoritative absence.
		present, probeErr := tmux.ProbeSession(r.sess)
		if probeErr != nil {
			return fmt.Errorf("tmux unavailable: %w", probeErr)
		}
		if !present {
			return err
		}
	}
	m.refresh()
	m.vp.Point(r.sess, "", false)
	if gone {
		// Say it plainly: the session exists, but not where the row promised.
		m.flashError(fmt.Errorf("dir gone, started in ~"))
	}
	return nil
}

// summonDir resolves where a tmux ghost should be started: its recorded dir if
// that still exists, else home. gone is true only when a dir WAS recorded and
// has since vanished — an unrecorded dir is not a broken promise, so it flashes
// nothing.
func summonDir(dir string) (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "~"
	}
	if dir == "" {
		return home, false
	}
	if _, err := os.Stat(dir); err != nil {
		return home, true
	}
	return dir, false
}

// pointRow routes a row selection to the viewport attach.
func (m *railModel) pointRow(r railRow) {
	if r.isGroup {
		return // a group is a shelf: there is nothing behind it to attach to
	}
	if r.ghost || r.validity != rowFresh {
		return // no proven live process: never attach it
	}
	m.clearViewedDone(r)
	m.vp.Point(r.sess, r.window, r.attached)
}

// suppressDone reports whether a window's done mark should be withheld because
// the user is watching that session (in the viewport) or attached elsewhere.
func (m railModel) suppressDone(sess, window string) bool {
	if m.attached[sess] {
		return true
	}
	lock := m.vp.Lock()
	if lock.Sess == sess && (lock.Win == "" || lock.Win == window) {
		return true
	}
	return false
}

// clearViewedDone clears @ghostmux_done on whatever window the user is about to
// view: the selected window row, or a session row's active window.
func (m railModel) clearViewedDone(r railRow) {
	if r.validity != rowFresh {
		return
	}
	win := r.window
	if r.depth == 0 && win == "" {
		win = m.activeWindowOf(r.sess)
	}
	if win != "" {
		tmux.SetDone(r.sess, win, false)
	}
}

// activeWindowOf returns the index of the session's current window from the
// already-built rows.
func (m railModel) activeWindowOf(sess string) string {
	for _, row := range m.rows {
		if row.depth == 1 && row.sess == sess && row.active {
			return row.window
		}
	}
	return ""
}

// maybeBlink starts the blink timer if a bell exists and no timer is running.
func (m *railModel) maybeBlink() tea.Cmd {
	if !m.blinking && anyBell(m.rows) {
		m.blinking = true
		return blinkTicker()
	}
	return nil
}

// anyBell reports whether any row carries a bell mark.
func anyBell(rows []railRow) bool {
	for _, r := range rows {
		if r.validity == rowFresh && r.bell {
			return true
		}
	}
	return false
}

// attachedMap indexes sessions attached by an outside client.
func attachedMap(sessions []tmux.Session) map[string]bool {
	m := map[string]bool{}
	for _, s := range sessions {
		if s.Attached {
			m[s.Name] = true
		}
	}
	return m
}

func (m *railModel) clamp() {
	n := len(m.visible())
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}
