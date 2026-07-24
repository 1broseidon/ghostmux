// Package wiring holds the small commands that are not the panel itself:
// up, ls, and doctor. The tmux-hosted hub and the ghostty config wiring both
// lived here and are gone — ghostmux owns its own frame now, so there is no
// outer multiplexer to build and no terminal to integrate with.
package wiring

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

// ---- paths ----

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	return h
}

// ---- snippets ----

const ghosttyNav = `# ghostmux: unified split navigation.
# performable: ghostty consumes ctrl+h/j/k/l only when it can actually move to a
# split in that direction; otherwise the key falls through to tmux (and to vim).
keybind = performable:ctrl+h=goto_split:left
keybind = performable:ctrl+j=goto_split:down
keybind = performable:ctrl+k=goto_split:up
keybind = performable:ctrl+l=goto_split:right
`

const tmuxConf = `# ghostmux: tmux base config
set -g default-terminal "tmux-256color"
set -ga terminal-overrides ",xterm-ghostty:RGB"
set -s escape-time 0
set -g mouse on
set -g focus-events on
set -g allow-passthrough on
set -g history-limit 100000
set -g base-index 1
setw -g pane-base-index 1
set -g renumber-windows on

# splits inherit the current pane's working directory
bind '"' split-window -v -c "#{pane_current_path}"
bind % split-window -h -c "#{pane_current_path}"
bind c new-window -c "#{pane_current_path}"

# vim-aware pane navigation: ctrl+h/j/k/l arrive here when ghostty has no
# split in that direction; forward to vim when the pane is running vim.
is_vim="ps -o state= -o comm= -t '#{pane_tty}' | grep -iqE '^[^TXZ ]+ +(\\S+\\/)?g?(view|l?n?vim?x?|fzf)(diff)?$'"
bind-key -n C-h if-shell "$is_vim" 'send-keys C-h' 'select-pane -L'
bind-key -n C-j if-shell "$is_vim" 'send-keys C-j' 'select-pane -D'
bind-key -n C-k if-shell "$is_vim" 'send-keys C-k' 'select-pane -U'
bind-key -n C-l if-shell "$is_vim" 'send-keys C-l' 'select-pane -R'
# ctrl+l is taken by navigation; clear screen with prefix ctrl+l instead
bind-key C-l send-keys C-l
`

// ---- install / uninstall ----

// CmdGhostty routes the optional ghostty integration: `ghostmux ghostty
// install|uninstall` wires (or removes) the unified nav keymap. Ghostty is
// one terminal among many — nothing in the hub depends on it.
func CmdUp(args []string) error {
	rest := args
	if len(rest) < 1 {
		return fmt.Errorf("usage: ghostmux up <name> [dir]")
	}
	name := rest[0]
	dir := home()
	if len(rest) > 1 {
		abs, err := filepath.Abs(rest[1])
		if err != nil {
			return err
		}
		if _, err := os.Stat(abs); err != nil {
			return fmt.Errorf("directory %s: %w", abs, err)
		}
		dir = abs
	}

	// Already inside tmux: never nest — create if needed, then switch this client.
	if os.Getenv("TMUX") != "" {
		if tmux.Run("has-session", "-t", "="+name) != nil {
			if err := tmux.Run("new-session", "-d", "-s", name, "-c", dir); err != nil {
				return fmt.Errorf("tmux new-session: %w", err)
			}
		}
		if err := tmux.Run("switch-client", "-t", "="+name); err != nil {
			return fmt.Errorf("tmux switch-client: %w", err)
		}
		return nil
	}

	// Interactive terminal: attach in place by replacing this process with
	// tmux. -A attaches if the session already exists.
	if isTTY(os.Stdin) {
		tmuxPath, err := exec.LookPath("tmux")
		if err != nil {
			return err
		}
		return syscall.Exec(tmuxPath,
			[]string{"tmux", "new-session", "-A", "-s", name, "-c", dir}, os.Environ())
	}
	return fmt.Errorf("no terminal to attach in")
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func CmdLs() error {
	out, err := tmux.Runner("list-sessions",
		"-F", "#{session_name}\t#{session_windows} windows\t#{?session_attached,attached,detached}\t#{session_path}")
	if err != nil {
		fmt.Println("no tmux sessions")
		return nil
	}
	fmt.Print(out)
	return nil
}

// ---- doctor ----

// CmdDoctor reports what ghostmux can see: which multiplexers are installed,
// what each can prove about its sessions, and whether any stale refresh hooks
// were left behind by a crashed run. It checks the environment, never a hub —
// ghostmux owns its own frame now, so there is no layout to diagnose.
func CmdDoctor() error {
	ok := true
	check := func(label string, pass bool, detail string) {
		mark := "ok  "
		if !pass {
			mark, ok = "warn", false
		}
		fmt.Printf("[%s] %-22s %s\n", mark, label, detail)
	}

	if path, err := exec.LookPath("tmux"); err == nil {
		check("tmux", true, path+"  "+firstLine(runOut("tmux", "-V")))
	} else {
		check("tmux", false, "not installed — tmux sessions will not be listed")
	}
	if path, err := exec.LookPath("zellij"); err == nil {
		check("zellij", true, path+"  "+firstLine(runOut("zellij", "--version")))
	} else {
		check("zellij", false, "not installed — zellij sessions will not be listed")
	}

	if n := len(strings.Fields(runOut("tmux", "list-sessions", "-F", "#{session_name}"))); n > 0 {
		check("tmux sessions", true, fmt.Sprintf("%d visible to the rail", n))
	}

	checkStaleHooks(check)

	if !ok {
		fmt.Println("\nghostmux runs with whatever is installed; each backend is listed\nonly to the extent it can prove its own state.")
	}
	return nil
}

// checkStaleHooks looks for ghostmux's refresh hooks at index [133] left in a
// tmux server by a crashed run. They are harmless but they fire run-shell on
// every event, so a stale set is worth reporting.
func checkStaleHooks(check func(label string, pass bool, detail string)) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return
	}
	out := runOut("tmux", "show-hooks", "-g")
	if strings.Contains(out, "[133]") {
		check("stale hooks", false, "ghostmux hooks at [133] with no rail running — run ghostmux once to clear")
		return
	}
	check("stale hooks", true, "none")
}

func runOut(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return strings.TrimSpace(s)
}
