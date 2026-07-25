package rail

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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
	killDelete
)

// verb is the word the confirm prompt uses.
func (k killKind) verb() string {
	switch k {
	case killUngroup:
		return "ungroup"
	case killForget:
		return "forget"
	case killDelete:
		return "delete"
	}
	return "kill"
}

// kindOf reads what `x` on a row would destroy. A zellij ghost is asked about
// at press time because only zellij can say whether a serialized session is
// still behind the name (delete) or nothing is left but our own declaration
// (forget) — and between render and keypress that can change.
func kindOf(r railRow) killKind {
	switch {
	case r.isGroup:
		return killUngroup
	case r.ghost && r.backend != "" && AuxSessionExists(r.backend, r.sess):
		return killDelete
	case r.ghost:
		return killForget
	}
	return killLive
}

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
	hub          string          // session the rail lives in — excluded from the tree
	vp           Viewport        // where selections render (pane / embedded pty)
	viewportDead bool            // pane was dead this tick — swap the hint line
	done         *doneTracker    // per-pane command-transition tracking (D5)
	attached     map[string]bool // session name → attached elsewhere
	blinking     bool            // 400ms blink timer running (D7)
	blinkPhase   int             // bell blink phase, mod 3 (glyph hidden on phase 2)

	collapsed map[string]bool // session name → collapsed in the rail (Task 7)

	mode        mode
	filterQuery string          // active filter substring (Task 8)
	killTarget  string          // session or group pending `x` confirmation
	killBackend string          // backend of killTarget ("" = tmux)
	killKind    killKind        // what `x` is about to destroy, captured at press
	input       textinput.Model // shared prompt editor for `/` and `n` modes

	errMsg   string    // last action error, shown on the hint line
	errUntil time.Time // errMsg clears once time.Now() passes this

	// selfAux is the rail's own host session on a non-tmux backend (the tmux
	// equivalent is `hub`). A frame running inside a multiplexer must not list
	// its own host: selecting it would render the frame inside itself.
	selfAux, selfAuxBackend string

	createBackend string // backend the `n` prompt will create on ("" = tmux)

	// groups is the only rail state not rediscovered from the multiplexers:
	// user intent, loaded once and written back on every change.
	groups []Group

	// dirs is the observed working directory of each grouped member, memberKey
	// → path. Evidence, recorded while the session is alive, so that when it
	// dies the ghost can say where it will come back.
	dirs map[string]string

	lastViewed string // "sess:win" the cursor last auto-followed the viewport to
}

// Model is the rail's public face for composition: the solo frame embeds it
// as a child bubbletea model, sharing the exact keymap/modes/refresh brain
// classic runs.
type Model = railModel

// New builds a rail model over a Viewport. The frame owns layout and chrome;
// the rail owns rows, marks, modes and keys. Use InHost to exclude the session
// the frame itself is running inside, if any.
func New(vp Viewport) Model {
	groups, collapsed, dirs := loadState()
	m := Model{vp: vp, groups: groups, collapsed: collapsed, dirs: dirs, done: newDoneTracker()}
	m.rows = applyGroups(railRows("", ViewState{}), m.groups, m.dirs)
	return m
}

// InHost tells the rail which multiplexer session it is itself running inside
// (backend "" = tmux), so it never lists — or nests into — its own host. A
// standalone frame is stateless: everything it shows is rediscovered from the
// muxes each tick, so it can simply be relaunched anywhere. Running it inside
// a session is how it gets resume for free, and this is what makes that safe.
func (m Model) InHost(backend, sess string) Model {
	if sess == "" {
		return m
	}
	if backend == "" {
		m.hub = sess
	} else {
		m.selfAux, m.selfAuxBackend = sess, backend
	}
	m.rows = applyGroups(railRows(m.hub, ViewState{}), m.groups, m.dirs)
	return m
}

// visibleAux drops the rail's own host from the aux session list.
func (m railModel) visibleAux(aux []auxSession) []auxSession {
	if m.selfAux == "" {
		return aux
	}
	out := aux[:0:0]
	for _, a := range aux {
		if a.backend == m.selfAuxBackend && a.name == m.selfAux {
			continue
		}
		out = append(out, a)
	}
	return out
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
		m.viewportDead = m.vp.Heal()
		m.clamp()
		debugRefresh("tick")
		return m, tea.Batch(railTicker(), m.maybeBlink())
	case refreshMsg:
		m.refresh()
		m.viewportDead = m.vp.Heal()
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
	case "g":
		m.cursor = 0
	case "G":
		m.cursor = len(m.visible()) - 1
		m.clamp()
	case "r":
		m.refresh()
		m.clamp()
		return m, m.maybeBlink()
	case "enter":
		if vis := m.visible(); m.cursor < len(vis) {
			m.activateRow(vis[m.cursor])
		}
	case "tab":
		if vis := m.visible(); m.cursor < len(vis) {
			m.toggleFold(vis[m.cursor])
		}
	case "a":
		// Name the shelf before you fill it: empty groups are legal.
		m.mode = modeGroup
		m.input = newPromptInput()
		m.errMsg = ""
		return m, textinput.Blink
	case "J", "K":
		dir := 1
		if msg.String() == "K" {
			dir = -1
		}
		if err := m.moveRow(dir); err != nil {
			m.flashError(err)
		}
	case "l", "right":
		m.vp.FocusViewport()
	case "d":
		m.vp.Detach()
		m.refresh()
	case "n":
		return m.startCreate("")
	case "z":
		// One key per multiplexer beats a picker: `n` is the default (tmux),
		// `z` is zellij. Nothing to discover, nothing to cycle. `z` simply
		// isn't offered when zellij isn't installed.
		if !zellijPresent {
			m.flashError(fmt.Errorf("zellij not installed"))
			return m, nil
		}
		return m.startCreate("zellij")
	case "x":
		if vis := m.visible(); m.cursor < len(vis) {
			r := vis[m.cursor]
			m.mode = modeKillConfirm
			m.killTarget = r.sess
			m.killBackend = r.backend
			m.killKind = kindOf(r)
			m.errMsg = ""
		}
	case "S":
		// The fleet verb: one press brings a whole declared workspace back.
		if vis := m.visible(); m.cursor < len(vis) {
			m.summonGroup(vis[m.cursor])
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
		backend := m.createBackend
		m.mode = modeNormal
		if err := m.createSession(name, backend); err != nil {
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

// updateKillConfirmKey handles the y/n confirmation after `x` (Task 9).
func (m railModel) updateKillConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y":
		target, backend, kind := m.killTarget, m.killBackend, m.killKind
		m.mode = modeNormal
		m.killTarget, m.killBackend, m.killKind = "", "", killLive
		var err error
		switch kind {
		case killUngroup:
			err = m.deleteGroup(target)
		case killForget:
			// Nothing to kill: the session is already gone. All that is left is
			// our own declaration, so `x` on a ghost prunes the state file —
			// which is how it stops accumulating names you can no longer see.
			m.forgetMember(memberKey(backend, target))
			m.refresh()
		case killDelete:
			err = m.deleteGhost(target, backend)
		default:
			err = m.killSession(target, backend)
		}
		if err != nil {
			m.flashError(err)
		}
	case "n", "esc":
		m.mode = modeNormal
		m.killTarget = ""
	}
	return m, nil
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

// flashError records an action error for the hint-line flash (3s, Task 9).
func (m *railModel) flashError(err error) {
	m.errMsg = err.Error()
	m.errUntil = time.Now().Add(3 * time.Second)
}

// errorActive reports whether the error flash is still within its window.
func (m railModel) errorActive() bool {
	return m.errMsg != "" && time.Now().Before(m.errUntil)
}

// startCreate opens the new-session prompt targeting one backend.
func (m railModel) startCreate(backend string) (tea.Model, tea.Cmd) {
	m.mode = modeCreate
	m.input = newPromptInput()
	m.createBackend = backend
	m.errMsg = ""
	return m, textinput.Blink
}

// backendLabel names a backend for the prompt ("" = tmux).
func backendLabel(backend string) string {
	if backend == "" {
		return "tmux"
	}
	return backend
}

// createSession creates a session on the given backend ("" = tmux) and points
// the viewport at it (Task 9). Exposed via the `n` key; tab cycles backends,
// so a zellij-only box can still create — the multi-backend promise has to
// hold for making sessions, not just for listing them.
func (m *railModel) createSession(name, backend string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name required")
	}
	if backend != "" {
		if err := createAux(backend, name); err != nil {
			return err
		}
		m.refresh()
		m.vp.PointAux(backend, name)
		m.refresh()
		return nil
	}
	dir, err := os.UserHomeDir()
	if err != nil || dir == "" {
		dir = "~"
	}
	if err := tmux.Run("new-session", "-d", "-s", name, "-c", dir); err != nil {
		return err
	}
	m.refresh()
	m.vp.Point(name, "", false)
	m.refresh()
	return nil
}

// killSession kills a session by name; the viewport cleans up its shadow and
// goes idle if it held the lock (Task 9, `x` key).
func (m *railModel) killSession(name, backend string) error {
	if backend != "" {
		if err := killAux(backend, name); err != nil {
			return err
		}
		m.vp.OnKill(name, backend)
		m.forgetMember(memberKey(backend, name))
		m.refresh()
		return nil
	}
	if err := tmux.Run("kill-session", "-t", "="+name); err != nil {
		return err
	}
	m.vp.OnKill(name, "")
	m.forgetMember(memberKey("", name))
	m.refresh()
	return nil
}

// deleteGhost removes a backend's serialized session, and the declaration that
// was pointing at it. Both, because deleting only the serialized half would
// leave a grouped member as a pure declaration ghost — the row would still be
// there after an `x` the user watched succeed.
func (m *railModel) deleteGhost(name, backend string) error {
	if err := deleteAux(backend, name); err != nil {
		return err
	}
	m.vp.OnKill(name, backend)
	m.forgetMember(memberKey(backend, name))
	m.refresh()
	return nil
}

// summonGroup is `S` on a group header: start every dead member at once. No
// confirm, because creating a session takes nothing away — and a fleet that
// needs six confirmations is not the one keystroke this is supposed to be.
// The viewport is left alone: S is about the fleet, not about what you are
// looking at. On any other row it does nothing; ↵ already says it better.
func (m *railModel) summonGroup(r railRow) {
	if !r.isGroup {
		return
	}
	// m.rows, not visible(): a folded group is still a fleet, and S must not
	// mean something different depending on a disclosure triangle.
	for _, row := range m.rows {
		if !row.ghost || row.isGroup || row.group != r.label {
			continue
		}
		if row.backend != "" {
			// One call covers both zellij ghosts: on a session zellij still
			// lists as EXITED, `attach --create-background` resurrects it
			// (verified) — on a name it has forgotten, it creates it. Neither
			// needs a client, so S never steals the viewport.
			createAux(row.backend, row.sess)
			continue
		}
		dir, _ := summonDir(row.dir)
		// A member that will not start stays a ghost and says so on its own
		// row; one failure must not abort the rest of the fleet.
		tmux.Run("new-session", "-d", "-s", row.sess, "-c", dir)
	}
	m.refresh()
}

// refresh reloads the fleet, runs done-tracking, and rebuilds the rows. It is
// the shared body of both the 1s tick (fallback + transition sampler, D6) and
// the event-driven refreshMsg.
func (m *railModel) refresh() {
	sessions := tmux.Sessions()
	windows := tmux.Windows()
	m.attached = attachedMap(sessions)
	if m.done != nil {
		m.done.observe(windows, m.hub, m.suppressDone)
	}
	m.captureDirs(sessions)
	m.vp.SyncActiveWindow(windows)
	m.rows = buildRows(m.hub, m.vp.Lock(), sessions, windows)
	m.rows = append(m.rows, auxRows(m.visibleAux(auxSessions()), m.vp.Lock())...)
	m.rows = applyGroups(m.rows, m.groups, m.dirs)
	m.followViewport()
}

// captureDirs records where each grouped tmux session is actually running. It
// is the whole of the ghost's memory, and it is taken from evidence while the
// session lives rather than asked for up front — a declaration the user never
// has to write. Only grouped members are recorded: an ungrouped session is
// cattle, and remembering its dir would be storage nobody asked for.
//
// zellij members never get a dir. The zellij CLI proves no working directory,
// and inventing one would be exactly the inference this rail refuses.
func (m *railModel) captureDirs(sessions []tmux.Session) {
	changed := false
	for _, s := range sessions {
		if s.Path == "" {
			continue
		}
		key := memberKey("", s.Name)
		if groupOf(m.groups, key) == "" {
			continue
		}
		if m.dirs[key] == s.Path {
			continue
		}
		if m.dirs == nil {
			m.dirs = map[string]string{}
		}
		m.dirs[key] = s.Path
		changed = true
	}
	// One write per refresh at most: this runs on a 1s tick, so a save per
	// changed member would rewrite the state file several times a second.
	if changed {
		m.saveState()
	}
}

// followViewport moves the rail cursor to the row the viewport is showing,
// once per viewed-window change — so ctrl+b navigation in the viewport
// scrolls/highlights the rail live, while leaving the cursor alone when the
// user is browsing other rows with j/k.
func (m *railModel) followViewport() {
	lock := m.vp.Lock()
	if lock.Sess == "" {
		m.lastViewed = ""
		return
	}
	key := lock.Sess + ":" + lock.Win
	if key == m.lastViewed {
		return
	}
	m.lastViewed = key
	best := -1
	for i, r := range m.visible() {
		if r.sess != lock.Sess {
			continue
		}
		if r.flat || (r.depth == 1 && r.window == lock.Win) {
			best = i
			break
		}
		if r.depth == 0 {
			best = i // session row stands in when its windows are collapsed
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
		m.toggleFold(r)
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
// a tmux ghost becomes a NEW session with the declared name in the recorded
// dir — which is the whole of what the row was claiming — and a zellij ghost
// is zellij's own resurrection, relayed. No layout replay, no command replay.
func (m *railModel) summonRow(r railRow) error {
	if r.backend != "" {
		// Ask the backend now rather than trusting the row: if it still lists
		// the name (EXITED), attaching IS the resurrection and creating would
		// be wrong; if it has forgotten the name entirely, only the
		// declaration is left and a fresh session is what we owe the user.
		if !AuxSessionExists(r.backend, r.sess) {
			if err := createAux(r.backend, r.sess); err != nil {
				return err
			}
			m.refresh()
		}
		m.vp.PointAux(r.backend, r.sess)
		return nil
	}
	dir, gone := summonDir(r.dir)
	if err := tmux.Run("new-session", "-d", "-s", r.sess, "-c", dir); err != nil {
		// A name that already exists is not a failure here: the session sprang
		// to life between the render and the keypress, which is the outcome we
		// were after. Ask tmux instead of reading the message — exec hands us
		// "exit status 1" and keeps tmux's own words ("duplicate session: api")
		// on stderr, where a text match would never see them.
		if !sessionExists(r.sess) {
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

// sessionExists asks tmux whether a session is there right now.
func sessionExists(name string) bool {
	return tmux.Run("has-session", "-t", "="+name) == nil
}

// pointRow routes a row selection to the right backend's viewport attach.
func (m *railModel) pointRow(r railRow) {
	if r.isGroup {
		return // a group is a shelf: there is nothing behind it to attach to
	}
	if r.ghost {
		return // nothing is running behind a ghost: summon it, never attach it
	}
	if r.backend != "" {
		m.vp.PointAux(r.backend, r.sess)
		return
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
		if r.bell {
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
