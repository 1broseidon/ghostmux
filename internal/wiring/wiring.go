// Package wiring holds every ghostmux command that isn't the rail:
// the hub launcher, up/ls/doctor, and the optional ghostty integration
// (nav-keymap config wiring).
package wiring

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

const (
	markerBegin = "# >>> ghostmux >>>"
	markerEnd   = "# <<< ghostmux <<<"
)

// ---- paths ----

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	return h
}

func snippetDir() string     { return filepath.Join(home(), ".config", "ghostmux") }
func ghosttySnippet() string { return filepath.Join(snippetDir(), "ghostty.conf") }
func tmuxSnippet() string    { return filepath.Join(snippetDir(), "tmux.conf") }
func ghosttyConfig() string  { return filepath.Join(home(), ".config", "ghostty", "config") }
func tmuxConfig() string     { return filepath.Join(home(), ".tmux.conf") }

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
func CmdGhostty(args []string) error {
	if len(args) != 1 || (args[0] != "install" && args[0] != "uninstall") {
		return fmt.Errorf("usage: ghostmux ghostty install|uninstall")
	}
	if args[0] == "install" {
		return CmdInstall()
	}
	return CmdUninstall()
}

func CmdInstall() error {
	if err := os.MkdirAll(snippetDir(), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(ghosttySnippet(), []byte(ghosttyNav), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(tmuxSnippet(), []byte(tmuxConf), 0o644); err != nil {
		return err
	}
	fmt.Println("wrote", ghosttySnippet())
	fmt.Println("wrote", tmuxSnippet())

	ghosttyBlock := fmt.Sprintf("config-file = ?%s", ghosttySnippet())
	changed, err := ensureBlock(ghosttyConfig(), ghosttyBlock)
	if err != nil {
		return fmt.Errorf("ghostty config: %w", err)
	}
	report(ghosttyConfig(), changed)

	tmuxBlock := fmt.Sprintf("source-file %s", tmuxSnippet())
	changed, err = ensureBlock(tmuxConfig(), tmuxBlock)
	if err != nil {
		return fmt.Errorf("tmux config: %w", err)
	}
	report(tmuxConfig(), changed)

	fmt.Println("\nto activate:")
	fmt.Println("  ghostty: press ctrl+shift+, (reload_config) or restart ghostty")
	fmt.Println("  tmux:    tmux source-file ~/.tmux.conf   (existing sessions)")
	return nil
}

func CmdUninstall() error {
	for _, f := range []string{ghosttyConfig(), tmuxConfig()} {
		changed, err := removeBlock(f)
		if err != nil {
			return err
		}
		if changed {
			fmt.Println("removed ghostmux block from", f)
		}
	}
	fmt.Println("snippets left in place under", snippetDir())
	return nil
}

func report(path string, changed bool) {
	if changed {
		fmt.Println("updated", path)
	} else {
		fmt.Println("already wired:", path)
	}
}

// ensureBlock appends a marker-delimited block containing line to path,
// creating the file if needed. Returns whether the file was modified.
func ensureBlock(path, line string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	content := string(data)
	if strings.Contains(content, markerBegin) {
		return false, nil
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += fmt.Sprintf("\n%s\n%s\n%s\n", markerBegin, line, markerEnd)
	return true, os.WriteFile(path, []byte(content), 0o644)
}

// removeBlock strips the marker-delimited block from path if present.
func removeBlock(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	content := string(data)
	begin := strings.Index(content, markerBegin)
	end := strings.Index(content, markerEnd)
	if begin == -1 || end == -1 || end < begin {
		return false, nil
	}
	head := strings.TrimRight(content[:begin], "\n")
	tail := strings.TrimLeft(content[end+len(markerEnd):], "\n")
	out := head
	if out != "" {
		out += "\n"
	}
	if tail != "" {
		out += "\n" + tail
	}
	return true, os.WriteFile(path, []byte(out), 0o644)
}

// ---- sessions ----

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

// ---- hub ----

// CmdHub is the single command that creates (or attaches to) the dedicated
// `hub` session: the rail+viewport layout, built by ghostmux, never by
// claiming an existing pane. See docs/SPEC.md §2 (Task 3). Idempotent: a
// healthy hub is only attached; a single-pane hub (rail pane died) is rebuilt.
func CmdHub(args []string) error {
	noAttach, newWindow := false, false
	for _, a := range args {
		switch a {
		case "--no-attach":
			noAttach = true
		case "-w", "--window":
			newWindow = true
		default:
			return fmt.Errorf("usage: ghostmux hub [-w] [--no-attach]")
		}
	}
	exe := selfExe()
	if tmux.Run("has-session", "-t", "=hub") != nil {
		if err := buildHub(exe); err != nil {
			return err
		}
	} else if hubPaneCount() < 2 {
		// Rail pane died, leaving a single-pane window: the rail's in-memory
		// state died with it, so a clean rebuild loses nothing recoverable.
		tmux.Run("kill-session", "-t", "=hub")
		if err := buildHub(exe); err != nil {
			return err
		}
	}
	if noAttach {
		return nil
	}
	return attachHub(newWindow)
}

// buildHub creates the hub session and its rail+viewport layout.
func buildHub(exe string) error {
	if err := tmux.Run("new-session", "-d", "-s", "hub", "-n", "rail", exe+" rail"); err != nil {
		return fmt.Errorf("tmux new-session: %w", err)
	}
	// Hub chrome needs no prefix: ctrl+b passes straight to the inner client
	// (D1). Session-scoped so a manual `rail` elsewhere keeps its prefix.
	// (set-option rejects the '=hub' exact-match form in tmux 3.4; plain
	// 'hub' still resolves exactly since the session exists.)
	tmux.Run("set-option", "-t", "hub", "prefix", "None")
	tmux.Run("set-option", "-t", "hub", "prefix2", "None")
	// Session-scoped mouse on: clicking between the rail and viewport panes
	// focuses them without needing prefix navigation, regardless of the
	// user's own tmux mouse setting (which stays untouched everywhere else).
	tmux.Run("set-option", "-t", "hub", "mouse", "on")
	ensureToggleBind()
	hubChrome()

	panes := tmux.Lines("list-panes", "-t", "=hub", "-F", "#{pane_id}")
	if len(panes) == 0 {
		return fmt.Errorf("hub: rail pane not found")
	}
	railPane := panes[0]
	vp := strings.TrimSpace(tmux.Output("split-window", "-h", "-d", "-l", "75%",
		"-t", railPane, "-P", "-F", "#{pane_id}"))
	if vp == "" {
		return fmt.Errorf("hub: viewport split failed")
	}
	tmux.Run("resize-pane", "-t", railPane, "-x", "30")
	tmux.Run("set-option", "-p", "-t", vp, "remain-on-exit", "on")
	tmux.Run("respawn-pane", "-k", "-t", vp, exe+" rail idle")
	return nil
}

// ensureToggleBind makes the hub keyboard-complete on any box with exactly
// ONE global key: prefix None removes every prefix command, so without it
// there is no keyboard way back from the viewport to the rail. Rules, in
// order: never steal keys from apps (outside the hub the bind forwards the
// key untouched); respect existing bindings (user's or a plugin's — we bind
// nothing if the key is taken); configurable without a config file (set
// @ghostmux_toggle in tmux.conf). Default C-\ — unclaimed by common TUIs,
// and vim-tmux-navigator muscle memory for "previous pane".
func ensureToggleBind() {
	key := strings.TrimSpace(tmux.Output("show-options", "-gv", "@ghostmux_toggle"))
	if key == "" {
		key = `C-\`
	}
	// Query the specific key: a table dump would false-positive on chords
	// inside tmux's default mouse-menu definitions.
	if _, err := tmux.Runner("list-keys", "-T", "root", key); err == nil {
		return // already bound by the user or a plugin: theirs wins
	}
	tmux.Run("bind-key", "-n", key,
		"if-shell", "-F", "#{==:#{session_name},hub}",
		"select-pane -t :.+", "send-keys "+key)
}

// hubChrome themes the hub session per docs/DESIGN.md §1/§4 — thin muted pane
// divider and the hand-rolled Gruvbox status bar. Everything session- or
// hub-window-scoped: the user's own tmux theme is untouched everywhere else
// (the "no status theming" cut in SPEC §7 is about user sessions; the hub is
// ghostmux chrome).
func hubChrome() {
	// Divider: quiet single line, muted green when the rail side is active
	// (chrome, not signal — DESIGN.md §1 col-31 divider).
	tmux.Run("set-option", "-t", "hub", "pane-border-style", "fg=#504945,bg=default")
	tmux.Run("set-option", "-t", "hub", "pane-active-border-style", "fg=#98971a,bg=default")

	// Status bar (DESIGN.md §4): bg1 row, gold `hub` block left, current
	// window in bg2 with a gold star, right side ghostty version · date ·
	// time · green `gm` block at the extreme right.
	tmux.Run("set-option", "-t", "hub", "status-style", "bg=#3c3836,fg=#a89984")
	tmux.Run("set-option", "-t", "hub", "status-left",
		"#[bg=#d79921,fg=#1d2021,bold] hub #[bg=#3c3836] ")
	tmux.Run("set-option", "-t", "hub", "status-left-length", "12")
	tmux.Run("set-option", "-t", "hub", "status-right",
		"#H #[fg=#665c54]│#[fg=#a89984] %d-%b #[fg=#665c54]│#[fg=#a89984] %H:%M #[bg=#689d6a,fg=#1d2021,bold] gm ")
	tmux.Run("set-option", "-t", "hub", "status-right-length", "48")
	tmux.Run("set-option", "-w", "-t", "hub:0", "window-status-format", " #I:#W ")
	tmux.Run("set-option", "-w", "-t", "hub:0", "window-status-current-format",
		"#[bg=#504945,fg=#fbf1c7] #I:#W#[fg=#fabd2f]*#[bg=#504945] ")
	tmux.Run("set-option", "-w", "-t", "hub:0", "window-status-separator", "")
}

// hubPaneCount reports how many panes the hub window has (0 if no server).
func hubPaneCount() int {
	panes := tmux.Lines("list-panes", "-t", "=hub", "-F", "#{pane_id}")
	if len(panes) == 1 && panes[0] == "" {
		return 0
	}
	return len(panes)
}

// attachHub attaches to the hub per environment: switch inside tmux, exec
// attach on a TTY, or open a ghostty window (-w, or no TTY to attach in).
func attachHub(newWindow bool) error {
	if os.Getenv("TMUX") != "" {
		return tmux.Run("switch-client", "-t", "=hub")
	}
	if !newWindow && isTTY(os.Stdin) {
		tmuxPath, err := exec.LookPath("tmux")
		if err != nil {
			return err
		}
		argv := append([]string{"tmux"}, tmux.Argv("attach", "-t", "=hub")...)
		return syscall.Exec(tmuxPath, argv, os.Environ())
	}
	args := append([]string{"+new-window", "-e", "tmux"}, tmux.Argv("attach", "-t", "=hub")...)
	cmd := exec.Command("ghostty", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ghostty +new-window: %w", err)
	}
	return nil
}

// selfExe resolves this binary's path for spawning `ghostmux rail`/`rail idle`.
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

// ---- doctor ----

func CmdDoctor() error {
	ok := true
	check := func(label string, pass bool, detail string) {
		mark := "ok"
		if !pass {
			mark = "!!"
			ok = false
		}
		fmt.Printf("  [%s] %-28s %s\n", mark, label, detail)
	}

	// Ghostty checks only where ghostty exists — the hub is terminal-agnostic
	// and these rows would be noise under iTerm2 or a bare ssh box.
	if _, err := exec.LookPath("ghostty"); err == nil {
		ghosttyVer := firstLine(runOut("ghostty", "+version"))
		check("ghostty", ghosttyVer != "", ghosttyVer)
		terminfo := exec.Command("infocmp", "-x", "xterm-ghostty").Run() == nil
		check("terminfo xterm-ghostty", terminfo, "")
		gWired := fileContains(ghosttyConfig(), markerBegin)
		check("ghostty nav wired", gWired, ghosttyConfig())
	}
	tmuxVer := firstLine(tmux.Output("-V"))
	check("tmux", tmuxVer != "", tmuxVer)

	tWired := fileContains(tmuxConfig(), markerBegin)
	check("tmux config wired", tWired, tmuxConfig())

	term := os.Getenv("TERM_PROGRAM")
	if term == "" {
		term = os.Getenv("TERM")
	}
	fmt.Printf("  [--] %-28s %s\n", "terminal", term+" (informational)")

	if tmux.Run("has-session", "-t", "=hub") == nil {
		checkHub(check)
	}
	checkStaleHooks(check)

	if !ok {
		fmt.Println("\nrun `ghostmux ghostty install` to wire the nav keymap (ghostty only)")
	}
	return nil
}

// checkHub runs the hub-specific checks (D1, Task 3) when a hub session
// exists: layout (2 panes, rail pane 30 wide), prefix None, mouse on.
func checkHub(check func(label string, pass bool, detail string)) {
	panes := tmux.Lines("list-panes", "-t", "=hub", "-F", "#{pane_id}\t#{pane_width}")
	railWidth := -1
	npanes := 0
	if !(len(panes) == 1 && panes[0] == "") {
		npanes = len(panes)
		if npanes > 0 {
			if f := strings.SplitN(panes[0], "\t", 2); len(f) == 2 {
				if w, err := strconv.Atoi(f[1]); err == nil {
					railWidth = w
				}
			}
		}
	}
	check("hub layout", npanes == 2, fmt.Sprintf("%d panes", npanes))
	check("hub rail width", railWidth == 30, fmt.Sprintf("%d", railWidth))

	prefix := strings.TrimSpace(tmux.Output("show-options", "-t", "hub", "prefix"))
	check("hub prefix", strings.Contains(prefix, "None"), prefix)

	mouse := strings.TrimSpace(tmux.Output("show-options", "-t", "hub", "mouse"))
	check("hub mouse", strings.Contains(mouse, "on"), mouse)
}

// checkStaleHooks warns when ghostmux's [133] refresh hooks (D6) are still
// installed but no rail process is running anywhere (pgrep-free: no pane
// anywhere has pane_current_command == the ghostmux binary's own name).
func checkStaleHooks(check func(label string, pass bool, detail string)) {
	hooks := tmux.Output("show-hooks", "-g")
	if !strings.Contains(hooks, "[133]") {
		return // nothing installed, nothing to warn about
	}
	railRunning := false
	for _, cmd := range tmux.Lines("list-panes", "-a", "-F", "#{pane_current_command}") {
		if cmd == "ghostmux" {
			railRunning = true
			break
		}
	}
	check("stale ghostmux hooks", railRunning,
		map[bool]string{true: "", false: "run `ghostmux hub` or `tmux set-hook -gu <name>[133]`"}[railRunning])
}

func runOut(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func fileContains(path, needle string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), needle)
}
