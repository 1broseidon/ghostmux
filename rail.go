package main

// rail: a persistent left-pane navigator for tmux under ghostty.
// Ambient, glanceable state — the anti-choose-tree: always visible, live
// attention gutter (bell/activity), enter to jump anywhere.
//
//   ghostmux rail dock   split a 30-col rail pane on the left, full height
//   ghostmux rail        run the TUI in the current pane (inside tmux)
//   ghostmux rail once   print one frame and exit (debugging / agents)

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func cmdRail(args []string) error {
	if len(args) > 0 && args[0] == "once" {
		for _, r := range railRows("") {
			fmt.Println(r.plain())
		}
		return nil
	}
	if os.Getenv("TMUX") == "" {
		return fmt.Errorf("rail runs inside tmux — try `ghostmux up <name>` first, then `ghostmux rail dock`")
	}
	if len(args) > 0 && args[0] == "dock" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		// -b: left of the current pane, -f: full window height.
		return exec.Command("tmux", "split-window", "-hbf", "-l", "30", exe+" rail").Run()
	}

	// Activity tracking is off by default; the gutter needs it. Quiet the
	// on-screen messages — the rail *is* the notification surface.
	exec.Command("tmux", "set-option", "-g", "monitor-activity", "on").Run()
	exec.Command("tmux", "set-option", "-g", "visual-activity", "off").Run()

	// Hub layout: this pane is the rail; the other pane in this window is
	// the viewport, where selected sessions render as a nested tmux client.
	hub := strings.TrimSpace(runOut("tmux", "display-message", "-p", "#{session_name}"))
	viewport := findOrCreateViewport()
	// Keep the pane alive between viewport respawns.
	exec.Command("tmux", "set-option", "-p", "-t", viewport, "remain-on-exit", "on").Run()

	_, err := tea.NewProgram(
		railModel{hub: hub, viewport: viewport, rows: railRows(hub)},
		tea.WithAltScreen()).Run()
	return err
}

// findOrCreateViewport returns the other pane in the rail's window,
// splitting a fresh one if the rail is alone.
func findOrCreateViewport() string {
	mine := os.Getenv("TMUX_PANE")
	for _, id := range tmuxLines("list-panes", "-F", "#{pane_id}") {
		if id != mine && id != "" {
			return id
		}
	}
	out, _ := exec.Command("tmux", "split-window", "-h", "-d", "-P", "-F", "#{pane_id}").Output()
	return strings.TrimSpace(string(out))
}

// ---- data ----

type railRow struct {
	depth    int // 0 session, 1 window, 2 pane
	label    string
	gutter   string // attention marks: ● bell, ~ activity
	active   bool   // tmux's notion of current
	attached bool   // session rows only
	sess     string
	window   string // window index, window/pane rows
	paneID   string // pane rows only
}

func (r railRow) plain() string {
	mark := " "
	if r.active {
		mark = "*"
	}
	return fmt.Sprintf("%s%s%-2s %s", strings.Repeat("  ", r.depth), mark, r.gutter, r.label)
}

// railRows lists sessions and their windows, excluding the hub session the
// rail itself lives in (rendering the hub inside its own viewport would be
// an infinite mirror).
func railRows(hub string) []railRow {
	var rows []railRow
	sessions := tmuxLines("list-sessions", "-F",
		"#{session_name}\t#{session_attached}")
	windows := tmuxLines("list-windows", "-a", "-F",
		"#{session_name}\t#{window_index}\t#{window_name}\t#{window_active}\t#{window_activity_flag}\t#{window_bell_flag}")

	for _, s := range sessions {
		sf := strings.Split(s, "\t")
		if len(sf) < 2 || sf[0] == hub {
			continue
		}
		sess, attached := sf[0], sf[1] != "0"
		rows = append(rows, railRow{
			depth: 0, label: sess, sess: sess, attached: attached,
		})
		for _, w := range windows {
			wf := strings.Split(w, "\t")
			if len(wf) < 6 || wf[0] != sess {
				continue
			}
			gutter := ""
			if wf[5] != "0" {
				gutter += "●"
			}
			if wf[4] != "0" {
				gutter += "~"
			}
			rows = append(rows, railRow{
				depth: 1, label: wf[1] + ":" + wf[2], gutter: gutter,
				active: wf[3] != "0", sess: sess, window: wf[1],
			})
		}
	}
	return rows
}

func tmuxLines(args ...string) []string {
	out, err := exec.Command("tmux", args...).Output()
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// ---- TUI ----

type railTick time.Time

type railModel struct {
	rows     []railRow
	cursor   int
	height   int
	hub      string // session the rail lives in — excluded from the tree
	viewport string // pane id where selections render as a nested client
}

func railTicker() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return railTick(t) })
}

func (m railModel) Init() tea.Cmd { return railTicker() }

func (m railModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case railTick:
		m.rows = railRows(m.hub)
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
			m.jump()
			m.rows = railRows(m.hub)
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

// jump renders the selected session into the viewport pane as a nested
// tmux client. The rail never moves; enter only re-points the viewport.
func (m railModel) jump() {
	if m.cursor >= len(m.rows) || m.viewport == "" {
		return
	}
	r := m.rows[m.cursor]
	// TMUX= lets a client attach from inside tmux; \; chains a window
	// selection after the attach when a window row was chosen.
	attach := fmt.Sprintf("TMUX= tmux attach-session -t '=%s'", r.sess)
	if r.window != "" {
		attach += fmt.Sprintf(" \\; select-window -t '=%s:%s'", r.sess, r.window)
	}
	exec.Command("tmux", "respawn-pane", "-k", "-t", m.viewport, attach).Run()
}

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
	b.WriteString("\n" + railDim.Render("j/k move · enter view → · q quit"))
	return b.String()
}
