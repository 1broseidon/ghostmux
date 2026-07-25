// Package app is the ghostmux panel: the program `ghostmux` starts. It owns
// the outer frame — draws the rail itself and renders the selected session
// through an embedded terminal emulator running one child (`tmux attach`,
// `zellij attach`, whatever the row says).
//
// The inner multiplexers keep everything that makes them worth using:
// persistence, session truth, their own keymaps. ghostmux is only the frame.
// What that buys: one bar instead of two nested ones, keybindings that are
// in-process (so nothing can be stolen from the program you are using), no
// host abstraction, a cockpit a zellij-only user can run with tmux absent —
// and, because two panels share nothing but the muxes they read, a per-client
// view: two terminals can watch different sessions without fighting.
package app

import (
	"context"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/1broseidon/ghostmux/internal/rail"
	"github.com/1broseidon/ghostmux/internal/term"
	"github.com/1broseidon/ghostmux/internal/tmux"
)

// Input focus: exactly two places a keystroke can go.
const (
	focusRail = iota
	focusViewport
)

// defaultToggles are the keys the frame reserves for rail ⇄ viewport. More
// than one, because the failure is silent and unfixable from inside: a
// desktop environment (1Password, a WM shortcut) can grab a chord before the
// terminal ever sees it, and then the toggle is simply dead with no error to
// read. A second accepted key means a grabbed default is an annoyance, not a
// lockout. GHOSTMUX_TOGGLE replaces the list entirely (comma-separated),
// matching classic's @ghostmux_toggle in spirit — env, not a config file.
//
// Every reserved key is one stolen from the program in the viewport, so this
// list stays short, and stays clear of tmux's ctrl+b and zellij's
// ctrl+g/p/t/n/h/s/o/q.
var defaultToggles = []string{`ctrl+\`, "f12"}

// dividerCol is the single column between rail and viewport.
const dividerCol = 1

type soloModel struct {
	rail        rail.Model
	vp          *ptyViewport
	focus       int
	w, h        int
	toggles     map[string]bool
	toggleLabel string // the first accepted toggle, as shown in the bottom bar

	// The frame owns two pieces of chrome the rail cannot: a floating help
	// overlay and a settings mode. Both are keyed here and nowhere else — one
	// behavior, one place — so no second interception can drift out of sync
	// with this one.
	overlayHelp bool
	settings    *settingsModel // nil = not in settings mode
}

// toggleKeys resolves the accepted toggle keys in three layers: defaults, then
// the state file, then GHOSTMUX_TOGGLE (a comma-separated list). Env last
// because it is the layer a user reaches for when the panel is already broken
// — a grabbed chord, a strange terminal — and a stored setting must not be
// able to override the escape hatch. An env value of only separators falls
// back rather than leaving the frame with no way out of the viewport.
func toggleKeys() []string {
	keys := defaultToggles
	if saved := rail.LoadSettings().Toggle; len(saved) > 0 {
		keys = saved
	}
	var env []string
	for _, k := range strings.Split(os.Getenv("GHOSTMUX_TOGGLE"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			env = append(env, k)
		}
	}
	if len(env) > 0 {
		return env
	}
	return keys
}

// toggleEnvLocked reports whether GHOSTMUX_TOGGLE is deciding the binding. The
// settings pane asks, because a field the user cannot change has to say why
// rather than accept an edit and discard it.
func toggleEnvLocked() bool {
	for _, k := range strings.Split(os.Getenv("GHOSTMUX_TOGGLE"), ",") {
		if strings.TrimSpace(k) != "" {
			return true
		}
	}
	return false
}

func newSolo(vp *ptyViewport) soloModel {
	applySettings(rail.LoadSettings())
	keys := toggleKeys()
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[k] = true
	}
	// Tell the rail what we reserved so `?` reports the truth.
	rail.SetToggleKeys(keys...)
	r := rail.New(vp)
	// The panel is stateless — everything it draws is rediscovered from the
	// muxes each tick — so relaunching it anywhere rebuilds the same cockpit.
	// That makes `tmux new -A -s gm ghostmux` a complete answer to resume, and
	// this is what keeps it safe: never list the session hosting us, or ↵ on
	// that row renders the panel inside itself.
	if backend, sess := hostSession(); sess != "" {
		r = r.InHost(backend, sess)
	}
	return soloModel{rail: r, vp: vp, focus: focusRail, toggles: set, toggleLabel: keys[0]}
}

// applySettings pushes the stored settings into the packages that own the
// behaviour. Called at boot and again after every edit, so "saved" and
// "applied" are the same code path — a setting that only takes effect on the
// next launch is a setting the user has to be told about.
func applySettings(s rail.Settings) {
	if s.RailWidth != 0 {
		rail.SetWidth(s.RailWidth)
	}
	rail.AddAgentCmds(s.Agents)
}

// hostSession identifies the multiplexer session this frame is running inside,
// if any: ("" , name) for tmux, ("zellij", name) for zellij, ("", "") when the
// frame owns the terminal directly.
func hostSession() (string, string) {
	if s := strings.TrimSpace(os.Getenv("ZELLIJ_SESSION_NAME")); s != "" {
		return "zellij", s
	}
	if os.Getenv("TMUX") != "" {
		if s := strings.TrimSpace(tmux.Output("display-message", "-p", "#{session_name}")); s != "" {
			return "", s
		}
	}
	return "", ""
}

func (m soloModel) Init() tea.Cmd { return m.rail.Init() }

func (m soloModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.resize(msg.Width, msg.Height)

	case term.OutputMsg:
		return m, nil // View() re-reads the emulator

	case term.ExitMsg:
		// Evidence law: the last real frame stays until heal decides what
		// replaces it on the next tick. No synthesized "dead" screen.
		return m, nil

	case tea.KeyMsg:
		// The frame's own chrome comes first, and this is the ONLY place it is
		// keyed. While the overlay is up any key dismisses it — including a
		// toggle key, which closes and does NOT also flip focus, because a key
		// that did two things at once would be one the user cannot use to mean
		// either. While settings is open, keys are its own and the toggle is
		// inert: there is no viewport on screen to hand the keyboard to.
		if m.overlayHelp {
			return m.closeOverlay(), nil
		}
		if m.settings != nil {
			return m.updateSettingsKey(msg)
		}
		// `?` and `,` are only the frame's while the rail has focus and is not
		// mid-prompt. Typing a filter, a name, or answering y/n, they are just
		// characters — a reserved key that silently eats one is the worst
		// failure a keymap can have.
		if !msg.Paste && m.focus == focusRail && !m.rail.InPrompt() {
			switch msg.String() {
			case "?":
				m.overlayHelp = true
				return m, nil
			case ",":
				return m.openSettings(), nil
			}
		}
		if !msg.Paste && m.toggles[msg.String()] {
			return m.setFocus(1 - m.focus), nil
		}
		if m.focus == focusViewport {
			if msg.Paste {
				m.vp.w.SendPaste(string(msg.Runes))
			} else {
				m.vp.w.SendKey(msg)
			}
			return m, nil
		}
		return m.updateRail(msg)

	case tea.MouseMsg:
		return m.updateMouse(msg)
	}
	// Everything else — the rail's own tick, refresh, and blink messages —
	// belongs to the rail. Its types are unexported by design.
	return m.updateRail(msg)
}

// updateMouse routes by coordinates, not focus: clicking the viewport while
// the rail has focus should hit the viewport, and vice versa. A press inside
// the viewport also moves focus there, which is what a click means.
func (m soloModel) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.overlayHelp {
		if msg.Action == tea.MouseActionPress {
			return m.closeOverlay(), nil
		}
		return m, nil
	}
	if m.settings != nil {
		return m, nil // the rail is not on screen: a click has nothing to hit
	}
	if msg.X < rail.Width() {
		if msg.Action == tea.MouseActionPress && m.focus != focusRail {
			m = m.setFocus(focusRail)
		}
		return m.updateRail(msg)
	}
	if msg.X < rail.Width()+dividerCol {
		return m, nil // the divider swallows its own column
	}
	if msg.Action == tea.MouseActionPress && m.focus != focusViewport {
		m = m.setFocus(focusViewport)
	}
	m.vp.w.SendMouse(msg, rail.Width()+dividerCol, 0)
	return m, nil
}

// updateRail forwards a message to the shared rail brain and picks up any
// focus request the rail made through Viewport.FocusViewport (the l/→ key).
func (m soloModel) updateRail(msg tea.Msg) (tea.Model, tea.Cmd) {
	r, cmd := m.rail.Update(msg)
	if rm, ok := r.(rail.Model); ok {
		m.rail = rm
	}
	if m.vp.takeFocusRequest() {
		m = m.setFocus(focusViewport)
	}
	return m, cmd
}

// resize is the whole of "the geometry changed": the terminal resized, or the
// rail width did. One function, because a width setting that resized the rail
// but not the child's pty would leave every program in the viewport wrapping
// at the wrong column.
func (m soloModel) resize(w, h int) (tea.Model, tea.Cmd) {
	m.w, m.h = w, h
	vw, bodyH := m.viewportSize()
	m.vp.w.Resize(vw, bodyH)
	return m.updateRail(tea.WindowSizeMsg{Width: rail.Width(), Height: bodyH})
}

// closeOverlay is the one dismissal path for the help overlay.
func (m soloModel) closeOverlay() soloModel {
	m.overlayHelp = false
	return m
}

// setFocus moves input focus, telling the widget so its cursor overlay and
// focus-events reporting follow.
func (m soloModel) setFocus(f int) soloModel {
	m.focus = f
	if f == focusViewport {
		m.vp.w.Focus()
	} else {
		m.vp.w.Blur()
	}
	return m
}

// viewportSize is the embedded terminal's cell size: everything right of the
// divider, above the status row.
func (m soloModel) viewportSize() (int, int) {
	vw := m.w - rail.Width() - dividerCol
	if vw < 1 {
		vw = 1
	}
	bodyH := m.h - 1
	if bodyH < 1 {
		bodyH = 1
	}
	return vw, bodyH
}

func (m soloModel) View() string {
	if m.w == 0 || m.h == 0 {
		return ""
	}
	vw, bodyH := m.viewportSize()

	var b strings.Builder
	if m.settings != nil {
		b.WriteString(m.settingsView(vw, bodyH))
	} else {
		railLines := block(m.rail.View(), rail.Width(), bodyH)
		vpLines := block(m.viewportView(vw, bodyH), vw, bodyH)
		div := m.dividerStyle().Render("│")
		for i := range bodyH {
			b.WriteString(railLines[i])
			b.WriteString(div)
			b.WriteString(vpLines[i])
			b.WriteByte('\n')
		}
	}
	b.WriteString(m.statusLine(m.w))

	// The overlay is composited last, over a finished frame — which is why the
	// fleet underneath stays live while it is up: nothing about the frame
	// changed, something was drawn on top of it.
	if m.overlayHelp {
		return helpOverlay(b.String(), m.w, m.h)
	}
	return b.String()
}

// dividerStyle tints the divider to show which side has the keyboard —
// chrome, not signal (DESIGN.md §1), and the only focus indicator the frame
// needs: the cursor itself shows the rest.
func (m soloModel) dividerStyle() lipgloss.Style {
	fg := hexDividerIdle
	if m.focus == focusRail {
		fg = hexDividerRail
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(fg))
}

// viewportView is the embedded terminal's frame, or the idle placeholder when
// nothing is locked.
func (m soloModel) viewportView(w, h int) string {
	if m.vp.Lock().Sess == "" {
		return idleView(w, h)
	}
	return m.vp.w.View()
}

const (
	hexDividerIdle = "#504945"
	hexDividerRail = "#98971a"
	hexIdleAccent  = "#fe8019"
	hexIdleText    = "#504945"
)

// idleView centers rail.IdleLines in a w×h block — the same placeholder
// classic renders through a `ghostmux rail idle` subprocess, drawn in-process
// here because solo has no pane to respawn.
func idleView(w, h int) string {
	lines := rail.IdleLines()
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color(hexIdleAccent))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color(hexIdleText))

	out := make([]string, 0, h)
	for range max((h-len(lines))/2, 0) {
		out = append(out, "")
	}
	for _, ln := range lines {
		pad := max((w-len([]rune(ln.Text)))/2, 0)
		body := dim.Render(ln.Text)
		if ln.Accent {
			body = accent.Render("▸") + dim.Render(ln.Text[len("▸"):])
		}
		out = append(out, strings.Repeat(" ", pad)+body)
	}
	return strings.Join(out, "\n")
}

// block cuts or pads s into exactly h lines of exactly w columns, ANSI-aware,
// and resets styling at every line end so no color bleeds across the divider.
func block(s string, w, h int) []string {
	lines := strings.Split(s, "\n")
	out := make([]string, h)
	for i := range h {
		ln := ""
		if i < len(lines) {
			ln = lines[i]
		}
		out[i] = pad(ln, w) + "\x1b[0m"
	}
	return out
}

// pad trims or space-fills one line to exactly w display columns.
func pad(s string, w int) string {
	width := ansi.StringWidth(s)
	switch {
	case width > w:
		return ansi.Cut(s, 0, w)
	case width < w:
		return s + strings.Repeat(" ", w-width)
	}
	return s
}

// truncate cuts s to at most w display columns, resetting styling after.
func truncate(s string, w int) string {
	if ansi.StringWidth(s) <= w {
		return s
	}
	return ansi.Cut(s, 0, w) + "\x1b[0m"
}

// Run is bare `ghostmux`: the panel.
func Run(args []string) error {
	// tmux may not be installed at all — a zellij-only box is a first-class
	// case for solo. Everything tmux-side is guarded on that, so the frame
	// still runs (and the rail still lists zellij sessions).
	_, tmuxErr := exec.LookPath("tmux")
	hasTmux := tmuxErr == nil

	if hasTmux {
		// The gutter needs activity tracking; the rail is the notification
		// surface, so tmux's own messages stay off.
		tmux.Run("set-option", "-g", "monitor-activity", "on")
		tmux.Run("set-option", "-g", "visual-activity", "off")
		rail.InstallHooks()
		defer rail.RemoveHooks()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var p *tea.Program
	vp := newPtyViewport(80, 24, func(msg tea.Msg) {
		if p != nil {
			p.Send(msg)
		}
	})
	p = tea.NewProgram(newSolo(vp), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if hasTmux {
		go rail.WaitLoop(ctx, p)
	}
	_, err := p.Run()

	cancel()
	vp.w.Close() // SIGHUP the child; the inner session survives, as always
	if hasTmux {
		rail.RemoveHooks()
	}
	return err
}
