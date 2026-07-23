package rail

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	railTitle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	railSession = lipgloss.NewStyle().Bold(true)
	railDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	railBell    = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	railAct     = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	railCursor  = lipgloss.NewStyle().Reverse(true)
	railActive  = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
)

func (m railModel) View() string {
	var b strings.Builder
	b.WriteString(railTitle.Render("ghostmux rail") + "\n\n")
	for i, r := range m.rows {
		line := strings.Repeat(" ", r.depth*2)
		switch {
		case r.depth == 0:
			name := railSession.Render(r.label)
			if r.attached {
				name += railActive.Render(" ●")
			}
			line += name
		case r.active:
			line += railActive.Render(r.label)
		default:
			line += railDim.Render(r.label)
		}
		if strings.Contains(r.gutter, "●") {
			line += " " + railBell.Render("●")
		}
		if strings.Contains(r.gutter, "~") {
			line += " " + railAct.Render("~")
		}
		if i == m.cursor {
			line = railCursor.Render(line)
		}
		b.WriteString(line + "\n")
	}
	hint := "j/k move · enter view → · q quit"
	if m.viewportDead {
		hint = "↵ re-point viewport"
	}
	b.WriteString("\n" + railDim.Render(hint))
	return b.String()
}
