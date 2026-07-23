// Package rail is a persistent left-pane navigator for tmux under ghostty.
// Ambient, glanceable state — the anti-choose-tree: always visible, live
// attention gutter (bell/activity), enter to jump anywhere.
//
//	ghostmux rail        run the TUI in the current pane (inside tmux)
//	ghostmux rail once   print one frame and exit (debugging / agents)
//	ghostmux rail idle    render the viewport idle placeholder (internal)
package rail

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

// CmdRail runs `ghostmux rail`.
func CmdRail(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "once":
			for _, r := range railRows("", viewState{}) {
				fmt.Println(r.plain())
			}
			return nil
		case "idle":
			return cmdIdle()
		}
	}
	if os.Getenv("TMUX") == "" {
		return fmt.Errorf("rail runs inside tmux — try `ghostmux up <name>` first, then `ghostmux hub`")
	}

	// Activity tracking is off by default; the gutter needs it. Quiet the
	// on-screen messages — the rail *is* the notification surface.
	tmux.Run("set-option", "-g", "monitor-activity", "on")
	tmux.Run("set-option", "-g", "visual-activity", "off")

	// The other pane in this window is the viewport, where selected sessions
	// render as a nested tmux client. In the hub it's built by `ghostmux hub`.
	sess := strings.TrimSpace(tmux.Output("display-message", "-p", "#{session_name}"))
	vpPane := discoverViewport(sess, os.Getenv("TMUX_PANE"))
	// Keep the pane alive between viewport respawns.
	tmux.Run("set-option", "-p", "-t", vpPane, "remain-on-exit", "on")

	// Event-driven refresh (D6): global hooks at [133] pulse a wait-for channel
	// a listener goroutine blocks on. Hooks are torn down and the listener
	// killed on every exit path.
	installHooks()
	defer removeHooks()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	vp := viewport{pane: vpPane, idleCmd: selfExe() + " rail idle"}
	p := tea.NewProgram(
		railModel{hub: sess, vp: vp, rows: railRows(sess, viewState{}), done: newDoneTracker()},
		tea.WithAltScreen())
	go waitLoop(ctx, p)
	_, err := p.Run()
	cancel() // stop the wait-for listener and kill its blocking child

	// Tear the hooks down here, before the hub self-kill: kill-session '=hub'
	// destroys the pane this process runs in, so a deferred cleanup would never
	// execute on the hub path. (The defer still covers panic/early-error paths.)
	removeHooks()

	// In the dedicated hub session, quitting the rail tears the hub down; a
	// manual `ghostmux rail` in another session just exits.
	if sess == hubSession {
		tmux.Run("kill-session", "-t", "=hub")
	}
	return err
}

// selfExe resolves this binary's path for spawning subcommands (rail idle).
func selfExe() string {
	exe, err := os.Executable()
	if err != nil {
		return "ghostmux"
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return exe
}

// cmdIdle renders the centered idle placeholder (DESIGN.md screen 4) sized to
// the pane, then blocks — holding the viewport pane open between session views.
func cmdIdle() error {
	const (
		orange = "\x1b[38;2;254;128;25m" // ▸ accent (#fe8019)
		dim    = "\x1b[38;2;80;73;69m"   // placeholder text (#504945)
		reset  = "\x1b[0m"
	)
	w, h := paneSize()
	lines := []struct {
		text   string
		accent bool // leading ▸ rendered in orange, rest dim
	}{
		{"▸ ghostmux", true},
		{"the rail is watching", false},
		{"tmux new -s work  →  it appears", false},
	}

	var b strings.Builder
	b.WriteString("\x1b[2J\x1b[H")
	for range (h - len(lines)) / 2 {
		b.WriteString("\r\n")
	}
	for _, ln := range lines {
		pad := (w - len([]rune(ln.text))) / 2
		if pad < 0 {
			pad = 0
		}
		b.WriteString(strings.Repeat(" ", pad))
		if ln.accent {
			b.WriteString(orange + "▸" + reset + dim + ln.text[len("▸"):] + reset)
		} else {
			b.WriteString(dim + ln.text + reset)
		}
		b.WriteString("\r\n")
	}
	os.Stdout.WriteString(b.String())
	io.ReadAll(os.Stdin) // block until the pane is respawned onto a session
	return nil
}

// paneSize reports the viewport pane's width and height, defaulting to 80x24.
func paneSize() (int, int) {
	out := tmux.Output("display-message", "-p", "-t", os.Getenv("TMUX_PANE"),
		"#{pane_width}\t#{pane_height}")
	w, h := 80, 24
	if f := strings.Split(strings.TrimSpace(out), "\t"); len(f) == 2 {
		if n, err := strconv.Atoi(f[0]); err == nil && n > 0 {
			w = n
		}
		if n, err := strconv.Atoi(f[1]); err == nil && n > 0 {
			h = n
		}
	}
	return w, h
}
