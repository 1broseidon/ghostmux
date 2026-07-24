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
	killGroup   bool            // the kill target is a group, not a session
	input       textinput.Model // shared prompt editor for `/` and `n` modes

	errMsg   string    // last action error, shown on the hint line
	errUntil time.Time // errMsg clears once time.Now() passes this

	helpView bool // `?` toggles the in-pane help page

	// selfAux is the rail's own host session on a non-tmux backend (the tmux
	// equivalent is `hub`). A frame running inside a multiplexer must not list
	// its own host: selecting it would render the frame inside itself.
	selfAux, selfAuxBackend string

	createBackend string // backend the `n` prompt will create on ("" = tmux)

	// groups is the only rail state not rediscovered from the multiplexers:
	// user intent, loaded once and written back on every change.
	groups []Group

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
	groups, collapsed := loadState()
	m := Model{vp: vp, groups: groups, collapsed: collapsed, done: newDoneTracker()}
	m.rows = applyGroups(railRows("", ViewState{}), m.groups)
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
	m.rows = applyGroups(railRows(m.hub, ViewState{}), m.groups)
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
		if m.helpView {
			switch msg.String() {
			case "?", "esc", "q", "enter":
				m.helpView = false
			}
			return m, nil
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
			m.mode = modeKillConfirm
			m.killTarget = vis[m.cursor].sess
			m.killBackend = vis[m.cursor].backend
			m.killGroup = vis[m.cursor].isGroup
			m.errMsg = ""
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
	case "?":
		m.helpView = true
	}
	return m, nil
}

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
		target, backend, isGroup := m.killTarget, m.killBackend, m.killGroup
		m.mode = modeNormal
		m.killTarget, m.killBackend, m.killGroup = "", "", false
		var err error
		if isGroup {
			err = m.deleteGroup(target)
		} else {
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
	if m.helpView {
		if msg.Action == tea.MouseActionPress {
			m.helpView = false
		}
		return m, nil
	}
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
	m.vp.SyncActiveWindow(windows)
	m.rows = buildRows(m.hub, m.vp.Lock(), sessions, windows)
	m.rows = append(m.rows, auxRows(m.visibleAux(auxSessions()), m.vp.Lock())...)
	m.rows = applyGroups(m.rows, m.groups)
	m.followViewport()
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
	m.pointRow(r)
	m.refresh()
}

// pointRow routes a row selection to the right backend's viewport attach.
func (m *railModel) pointRow(r railRow) {
	if r.isGroup {
		return // a group is a shelf: there is nothing behind it to attach to
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
