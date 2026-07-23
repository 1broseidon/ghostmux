package rail

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

type railTick time.Time
type blinkMsg time.Time

type railModel struct {
	rows         []railRow
	cursor       int
	height       int
	hub          string          // session the rail lives in — excluded from the tree
	vp           viewport        // right-hand pane where selections render
	viewportDead bool            // pane was dead this tick — swap the hint line
	done         *doneTracker    // per-pane command-transition tracking (D5)
	attached     map[string]bool // session name → attached elsewhere
	blinking     bool            // 400ms blink timer running (D7)
	blinkPhase   int             // bell blink phase, mod 3 (glyph hidden on phase 2)
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
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			m.cursor++
			m.clamp()
		case "k", "up":
			m.cursor--
			m.clamp()
		case "g":
			m.cursor = 0
		case "G":
			m.cursor = len(m.rows) - 1
		case "r":
			m.refresh()
			m.clamp()
			return m, m.maybeBlink()
		case "enter":
			if m.cursor < len(m.rows) {
				r := m.rows[m.cursor]
				m.clearViewedDone(r)
				m.vp.point(r.sess, r.window)
			}
			m.refresh()
		case "d":
			m.vp.idle()
			m.vp.detached = true
			m.refresh()
		}
	}
	return m, nil
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
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}
