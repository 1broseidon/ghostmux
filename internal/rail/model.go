package rail

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type railTick time.Time

type railModel struct {
	rows         []railRow
	cursor       int
	height       int
	hub          string   // session the rail lives in — excluded from the tree
	vp           viewport // right-hand pane where selections render
	viewportDead bool     // pane was dead this tick — swap the hint line
}

func railTicker() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return railTick(t) })
}

func (m railModel) Init() tea.Cmd { return railTicker() }

func (m railModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case railTick:
		m.rows = railRows(m.hub)
		m.viewportDead = m.vp.heal()
		m.clamp()
		return m, railTicker()
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
			m.rows = railRows(m.hub)
			m.clamp()
		case "enter":
			if m.cursor < len(m.rows) {
				r := m.rows[m.cursor]
				m.vp.point(r.sess, r.window)
			}
			m.rows = railRows(m.hub)
		case "d":
			m.vp.idle()
			m.vp.detached = true
		}
	}
	return m, nil
}

func (m *railModel) clamp() {
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}
