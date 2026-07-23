package rail

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/1broseidon/ghostmux/internal/tmux"
	"github.com/1broseidon/ghostmux/internal/wiring"
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
	filterQuery string // active filter substring (Task 8)
	createInput string // in-progress `n` prompt text (Task 9)
	killTarget  string // session pending `x` confirmation (Task 9)

	errMsg   string    // last action error, shown on the hint line
	errUntil time.Time // errMsg clears once time.Now() passes this

	helpView bool   // `?` toggles the in-pane help page
	selfPane string // the rail's own pane id, for width self-enforcement
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
			r := vis[m.cursor]
			m.clearViewedDone(r)
			m.vp.point(r.sess, r.window)
		}
		m.refresh()
	case "tab":
		if vis := m.visible(); m.cursor < len(vis) {
			sess := vis[m.cursor].sess
			m.collapsed[sess] = !m.collapsed[sess]
			m.clamp()
		}
	case "d":
		m.vp.idle()
		m.vp.detached = true
		m.refresh()
	case "n":
		m.mode = modeCreate
		m.createInput = ""
		m.errMsg = ""
	case "a":
		if err := m.agentSession(); err != nil {
			m.flashError(err)
		}
	case "x":
		if vis := m.visible(); m.cursor < len(vis) {
			m.mode = modeKillConfirm
			m.killTarget = vis[m.cursor].sess
			m.errMsg = ""
		}
	case "/":
		m.mode = modeFilter
		m.errMsg = ""
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

// updateFilterKey handles keys while typing a filter query (`/`, Task 8).
func (m railModel) updateFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.filterQuery = ""
		m.mode = modeNormal
		m.clamp()
	case "enter":
		m.mode = modeNormal // keeps the filter active; second esc clears it
		m.clamp()
	case "backspace":
		if n := len([]rune(m.filterQuery)); n > 0 {
			r := []rune(m.filterQuery)
			m.filterQuery = string(r[:n-1])
		}
	default:
		if msg.Type == tea.KeyRunes {
			m.filterQuery += string(msg.Runes)
		}
	}
	return m, nil
}

// updateCreateKey handles keys while typing a new-session name (`n`, Task 9).
func (m railModel) updateCreateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeNormal
		m.createInput = ""
	case "enter":
		name := m.createInput
		m.mode = modeNormal
		m.createInput = ""
		if err := m.createSession(name); err != nil {
			m.flashError(err)
		}
	case "backspace":
		if n := len([]rune(m.createInput)); n > 0 {
			r := []rune(m.createInput)
			m.createInput = string(r[:n-1])
		}
	default:
		if msg.Type == tea.KeyRunes {
			m.createInput += string(msg.Runes)
		}
	}
	return m, nil
}

// updateKillConfirmKey handles the y/n confirmation after `x` (Task 9).
func (m railModel) updateKillConfirmKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y":
		target := m.killTarget
		m.mode = modeNormal
		m.killTarget = ""
		if err := m.killSession(target); err != nil {
			m.flashError(err)
		}
	case "n", "esc":
		m.mode = modeNormal
		m.killTarget = ""
	}
	return m, nil
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
	m.vp.point(name, "")
	m.refresh()
	return nil
}

// agentSession creates the lowest-free gm-agent-NN session with no prompt and
// points the viewport at it (Task 9, `a` key).
func (m *railModel) agentSession() error {
	name := wiring.FreeName("gm-agent-", "%02d")
	return m.createSession(name)
}

// killSession kills a session by name; if it held the viewport lock, the
// viewport goes idle (Task 9, `x` key).
func (m *railModel) killSession(name string) error {
	if err := tmux.Run("kill-session", "-t", "="+name); err != nil {
		return err
	}
	if m.vp.lockSess == name {
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
	m.rows = buildRows(m.hub, m.viewState(), sessions, windows)
}

// viewState is the viewport's current lock, used for inView marks and done
// suppression.
func (m railModel) viewState() viewState {
	return viewState{lockSess: m.vp.lockSess, lockWin: m.vp.lockWin}
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
