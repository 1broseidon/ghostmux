// termspike is the Gate 1 fidelity driver: a throwaway fullscreen frame
// running one child inside the embedded terminal widget. It exists to answer
// one question — does internal/term render vim/htop/claude-code/nested-tmux
// faithfully? — before any solo-frame work begins.
//
//	termspike <session>        run `tmux attach -t <session>` in the widget
//	termspike -- <cmd> [args]  run an arbitrary command in the widget
//
// ctrl+q quits (the only key the frame steals). Everything else — including
// ctrl+b, ctrl+c, mouse — goes to the child.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/1broseidon/ghostmux/internal/rail"
	"github.com/1broseidon/ghostmux/internal/term"
)

type model struct {
	w       *term.Widget
	argv    []string
	width   int
	height  int
	started bool
	exited  bool
	exitErr error
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.w.Resize(msg.Width, msg.Height-1)
		if !m.started {
			m.started = true
			if err := m.w.Start(m.argv, nil); err != nil {
				m.exited, m.exitErr = true, err
				return m, tea.Quit
			}
			m.w.Focus()
		}
		return m, nil
	case term.OutputMsg:
		return m, nil // View() re-renders from the emulator
	case term.ExitMsg:
		m.exited, m.exitErr = true, msg.Err
		return m, tea.Quit
	case tea.KeyMsg:
		if msg.String() == "ctrl+q" {
			return m, tea.Quit
		}
		m.w.SendKey(msg)
		return m, nil
	case tea.MouseMsg:
		m.w.SendMouse(msg, 0, 0)
		return m, nil
	}
	return m, nil
}

func (m model) View() string {
	if m.height == 0 {
		return ""
	}
	status := fmt.Sprintf(" termspike · ctrl+q quits · alt-screen:%v · running:%v",
		m.w.IsAltScreen(), m.w.Running())
	return m.w.View() + "\n" + status
}

func main() {
	args := os.Args[1:]
	var argv []string
	switch {
	case len(args) >= 2 && args[0] == "--":
		argv = args[1:]
	case len(args) == 1 && args[0] != "--":
		// The same attach argv solo will exec — honors GHOSTMUX_TMUX_ARGS,
		// so the spike can drive a scratch server too.
		argv = rail.AttachArgv(args[0], "", false)
	default:
		fmt.Fprintln(os.Stderr, "usage: termspike <tmux-session> | termspike -- <cmd> [args]")
		os.Exit(2)
	}

	var p *tea.Program
	w := term.New(80, 24, func(msg tea.Msg) { p.Send(msg) })
	p = tea.NewProgram(model{w: w, argv: argv},
		tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	w.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "termspike:", err)
		os.Exit(1)
	}
}
