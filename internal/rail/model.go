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
)

type railModel struct {
	rows         []railRow
	cursor       int // index into visible() rows, not raw rows
	height       int
	hub          string          // session the rail lives in — excluded from the tree
	vp           viewport        // right-hand pane where selections render
	viewportDead bool            // pane was dead this tick — swap the hint line
	done         *doneTracker    // per-pane command-transition tracking (D5)
	attached     map[string]bool // session name → attached elsewhere
	blinking     bool            // 400ms blink timer running (D7)
	blinkPhase   int             // bell blink phase, mod 3 (glyph hidden on phase 2)

	collapsed map[string]bool // session name → collapsed in the rail (Task 7)

	mode        mode
	filterQuery string          // active filter substring (Task 8)
	killTarget  string          // session pending `x` confirmation (Task 9)
	killBackend string          // backend of killTarget ("" = tmux)
	input       textinput.Model // shared prompt editor for `/` and `n` modes

	errMsg   string    // last action error, shown on the hint line
	errUntil time.Time // errMsg clears once time.Now() passes this

	helpView bool   // `?` toggles the in-pane help page
	selfPane string // the rail's own pane id, for width self-enforcement

	lastViewed string // "sess:win" the cursor last auto-followed the viewport to
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
		m.viewportDead = m.vp.heal()
		m.clamp()
		debugRefresh("tick")
		return m, tea.Batch(railTicker(), m.maybeBlink())
	case refreshMsg:
		m.refresh()
		m.viewportDead = m.vp.heal()
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
		// Self-enforce the rail width: hub creation resizes while detached,
		// and tmux rescales panes proportionally when a client attaches — so
		// the rail owns its own width, on start and on every resize.
		if m.selfPane != "" && m.vp.pane != "" && msg.Width != railWidth {
			tmux.Run("resize-pane", "-t", m.selfPane, "-x", fmt.Sprint(railWidth))
		}
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
			m.pointRow(vis[m.cursor])
		}
		m.refresh()
	case "tab":
		if vis := m.visible(); m.cursor < len(vis) {
			sess := vis[m.cursor].sess
			m.collapsed[sess] = !m.collapsed[sess]
			m.clamp()
		}
	case "l", "right":
		// Focus the viewport — the rail's own affordance, independent of
		// any root-table ctrl+h/l nav bindings.
		if m.vp.pane != "" {
			tmux.Run("select-pane", "-t", m.vp.pane)
		}
	case "d":
		m.vp.idle()
		m.vp.detached = true
		m.refresh()
	case "n":
		m.mode = modeCreate
		m.input = newPromptInput()
		m.errMsg = ""
		return m, textinput.Blink
	case "x":
		if vis := m.visible(); m.cursor < len(vis) {
			m.mode = modeKillConfirm
			m.killTarget = vis[m.cursor].sess
			m.killBackend = vis[m.cursor].backend
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

// updateKillConfirmKey handles the y/n confirmation after `x` (Task 9).
func (m railModel) updateKillConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y":
		target, backend := m.killTarget, m.killBackend
		m.mode = modeNormal
		m.killTarget, m.killBackend = "", ""
		if err := m.killSession(target, backend); err != nil {
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
				m.pointRow(vis[idx])
				m.refresh()
			}
			return m, nil
		}
		m.cursor = idx
	}
	return m, nil
}

// rowAt maps a screen line to a visible-row index, mirroring the View's
// layout math (title + blank above the tree, scroll indicators at the edges).
func (m railModel) rowAt(y int) (int, bool) {
	vis := m.visible()
	height := m.height
	if height <= 0 {
		height = 24
	}
	treeHeight := height - 4
	if treeHeight < 1 {
		treeHeight = 1
	}
	start, end, moreUp, _ := scrollWindow(len(vis), treeHeight, m.cursor)
	line := y - 2 // rows begin below the title and blank line
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

// createSession creates a plain tmux session and points the viewport at it
// (Task 9). Exported behavior via the `n` key.
func (m *railModel) createSession(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name required")
	}
	dir, err := os.UserHomeDir()
	if err != nil || dir == "" {
		dir = "~"
	}
	if err := tmux.Run("new-session", "-d", "-s", name, "-c", dir); err != nil {
		return err
	}
	m.refresh()
	m.vp.point(name, "", false)
	m.refresh()
	return nil
}

// killSession kills a session by name; if it held the viewport lock, the
// viewport goes idle (Task 9, `x` key).
func (m *railModel) killSession(name, backend string) error {
	if backend != "" {
		if err := killAux(backend, name); err != nil {
			return err
		}
		if m.vp.lockBackend == backend && m.vp.lockSess == name {
			m.vp.idle()
		}
		m.refresh()
		return nil
	}
	if err := tmux.Run("kill-session", "-t", "="+name); err != nil {
		return err
	}
	if m.vp.lockSess == name {
		// A grouped shadow would otherwise survive and keep the killed
		// session's windows alive in its group.
		if m.vp.grouped {
			tmux.Run("kill-session", "-t", "="+gmViewPrefix+name)
		}
		m.vp.idle()
	}
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
	// Follow the viewport's client: ctrl+b navigation inside the inner
	// session changes its active window — the lock tracks it so ▸, heal,
	// and the cursor all point at what the viewport actually shows. When
	// grouped, the shadow session carries the viewport's own focus.
	// Aux backends have no observable window focus; nothing to sync.
	if m.vp.lockSess != "" && m.vp.lockBackend == "" {
		target := m.vp.attachTarget()
		for _, w := range windows {
			if w.Session == target && w.Active {
				m.vp.lockWin = w.Index
				break
			}
		}
	}
	m.rows = buildRows(m.hub, m.viewState(), sessions, windows)
	m.rows = append(m.rows, auxRows(auxSessions(), m.viewState())...)
	m.followViewport()
}

// followViewport moves the rail cursor to the row the viewport is showing,
// once per viewed-window change — so ctrl+b navigation in the viewport
// scrolls/highlights the rail live, while leaving the cursor alone when the
// user is browsing other rows with j/k.
func (m *railModel) followViewport() {
	if m.vp.lockSess == "" {
		m.lastViewed = ""
		return
	}
	key := m.vp.lockSess + ":" + m.vp.lockWin
	if key == m.lastViewed {
		return
	}
	m.lastViewed = key
	best := -1
	for i, r := range m.visible() {
		if r.sess != m.vp.lockSess {
			continue
		}
		if r.flat || (r.depth == 1 && r.window == m.vp.lockWin) {
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

// viewState is the viewport's current lock, used for inView marks and done
// suppression.
func (m railModel) viewState() viewState {
	return viewState{lockSess: m.vp.lockSess, lockWin: m.vp.lockWin, lockBackend: m.vp.lockBackend}
}

// pointRow routes a row selection to the right backend's viewport attach.
func (m *railModel) pointRow(r railRow) {
	if r.backend != "" {
		m.vp.pointAux(r.backend, r.sess)
		return
	}
	m.clearViewedDone(r)
	m.vp.point(r.sess, r.window, r.attached)
}

// suppressDone reports whether a window's done mark should be withheld because
// the user is watching that session (in the viewport) or attached elsewhere.
func (m railModel) suppressDone(sess, window string) bool {
	if m.attached[sess] {
		return true
	}
	if m.vp.lockSess == sess && (m.vp.lockWin == "" || m.vp.lockWin == window) {
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
