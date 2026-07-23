// Package wiring holds every ghostmux command that isn't the rail:
// install/uninstall/ambient/shell/doctor/up/restore/ls, the config snippets
// they write, and the hub launcher.
package wiring

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

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

func CmdInstall() error {
	if err := os.MkdirAll(snippetDir(), 0o755); err != nil {
		return err
	}
	if err := writeGhosttySnippet(ambientOn()); err != nil { // preserve ambient setting
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
	newWindow := false
	rest := args[:0:0]
	for _, a := range args {
		if a == "-w" || a == "--window" {
			newWindow = true
			continue
		}
		rest = append(rest, a)
	}
	if len(rest) < 1 {
		return fmt.Errorf("usage: ghostmux up [-w] <name> [dir]")
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

	// Interactive terminal: attach in place by replacing this process with tmux.
	// -A attaches if the session already exists.
	if !newWindow && isTTY(os.Stdin) {
		tmuxPath, err := exec.LookPath("tmux")
		if err != nil {
			return err
		}
		return syscall.Exec(tmuxPath,
			[]string{"tmux", "new-session", "-A", "-s", name, "-c", dir}, os.Environ())
	}

	// -w, or no terminal to attach in: open a window in the running ghostty
	// instance. Requires ghostty >= 1.3 (`+new-window -e` forwarding, GTK #10809).
	cmd := exec.Command("ghostty", "+new-window", "-e",
		"tmux", "new-session", "-A", "-s", name, "-c", dir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ghostty +new-window: %w", err)
	}
	fmt.Printf("session %q up in %s (new window)\n", name, dir)
	return nil
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// CmdRestore reopens a ghostty window for every unattached tmux session.
// Stateless: tmux owns session truth; ghostmux only does the half tmux
// can't — opening terminal windows. After a ghostty quit/crash the tmux
// server still holds every session; restore brings the windows back.
// (Across reboots, compose with tmux-resurrect: it restores the sessions,
// ghostmux restores the windows.)
func CmdRestore() error {
	out, err := tmux.Runner("list-sessions", "-F", "#{session_name}\t#{session_attached}")
	if err != nil {
		fmt.Println("no tmux server running — nothing to restore")
		return nil
	}
	restored := 0
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		name, attached, ok := strings.Cut(line, "\t")
		if !ok || attached != "0" {
			continue
		}
		cmd := exec.Command("ghostty", "+new-window", "-e",
			"tmux", "attach-session", "-t", "="+name)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("reopen %q: %w", name, err)
		}
		fmt.Println("reopened", name)
		restored++
		time.Sleep(150 * time.Millisecond) // don't strobe the window manager
	}
	if restored == 0 {
		fmt.Println("no orphaned sessions — nothing to restore")
	}
	return nil
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

// ---- ambient mode ----

const gmPrefix = "gm-"

// CmdShell is what `command = ghostmux shell` runs in every new ghostty
// surface. It reclaims the lowest-numbered orphaned gm-* session or creates
// a fresh one, and — on a cold start with multiple orphans — unfolds the
// rest of the workspace by opening a ghostty window per remaining orphan.
func CmdShell() error {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	// Seam rules: never capture the quick terminal, never nest inside tmux.
	if os.Getenv("GHOSTTY_QUICK_TERMINAL") != "" || os.Getenv("TMUX") != "" {
		return syscall.Exec(shell, []string{shell}, os.Environ())
	}

	// Serialize claims so windows opening in parallel can't grab the same
	// session. Held until tmux reports our client attached.
	lock, err := acquireLock()
	if err != nil {
		return err
	}

	orphans, attachedCount := gmSessions()
	var claim string
	if len(orphans) > 0 {
		claim = orphans[0]
		if attachedCount == 0 && len(orphans) > 1 {
			// Cold start: this is the first window of a fresh ghostty.
			// Unfold the rest of the workspace.
			for _, name := range orphans[1:] {
				exec.Command("ghostty", "+new-window", "-e",
					"tmux", "attach-session", "-t", "="+name).Run()
				time.Sleep(150 * time.Millisecond)
			}
		}
	} else {
		claim = freeGMName()
	}

	cwd, err := os.Getwd()
	if err != nil {
		cwd = home()
	}
	// -A: attach when reclaiming, create when fresh — one code path.
	cmd := exec.Command("tmux", "new-session", "-A", "-s", claim, "-c", cwd)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		lock.Close()
		return fmt.Errorf("tmux: %w", err)
	}
	waitAttached(claim)
	lock.Close()
	err = cmd.Wait()
	if exit, ok := err.(*exec.ExitError); ok {
		os.Exit(exit.ExitCode())
	}
	return err
}

// gmSessions returns orphaned gm-* session names sorted by index, and how
// many gm-* sessions currently have a client attached.
func gmSessions() (orphans []string, attached int) {
	out, err := tmux.Runner("list-sessions", "-F", "#{session_name}\t#{session_attached}")
	if err != nil {
		return nil, 0
	}
	idx := []int{}
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		name, att, ok := strings.Cut(line, "\t")
		if !ok || !strings.HasPrefix(name, gmPrefix) {
			continue
		}
		n, err := strconv.Atoi(name[len(gmPrefix):])
		if err != nil {
			continue
		}
		if att == "0" {
			idx = append(idx, n)
		} else {
			attached++
		}
	}
	sort.Ints(idx)
	for _, n := range idx {
		orphans = append(orphans, fmt.Sprintf("%s%d", gmPrefix, n))
	}
	return orphans, attached
}

func freeGMName() string {
	return FreeName(gmPrefix, "%d")
}

// FreeName returns the lowest-numbered "<prefix><numFmt(n)>" session name not
// currently in use, e.g. FreeName("gm-", "%d") → "gm-0", "gm-1", ...;
// FreeName("gm-agent-", "%02d") → "gm-agent-00", "gm-agent-01", ... Shared by
// CmdShell's orphan-claiming and the rail's `a` (new agent session) key.
func FreeName(prefix, numFmt string) string {
	used := map[string]bool{}
	out := tmux.Output("list-sessions", "-F", "#{session_name}")
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		used[line] = true
	}
	for n := 0; ; n++ {
		if name := prefix + fmt.Sprintf(numFmt, n); !used[name] {
			return name
		}
	}
}

func acquireLock() (*os.File, error) {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	f, err := os.OpenFile(filepath.Join(dir, "ghostmux.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func waitAttached(name string) {
	for range 20 {
		out, err := tmux.Runner("display-message", "-p", "-t", "="+name, "#{session_attached}")
		if err == nil && strings.TrimSpace(out) != "0" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// CmdAmbient toggles `command = ghostmux shell` in the ghostty snippet.
func CmdAmbient(args []string) error {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off") {
		return fmt.Errorf("usage: ghostmux ambient on|off")
	}
	on := args[0] == "on"
	if err := os.MkdirAll(snippetDir(), 0o755); err != nil {
		return err
	}
	if err := writeGhosttySnippet(on); err != nil {
		return err
	}
	if on {
		fmt.Println("ambient mode ON: every new ghostty surface is a persistent tmux session")
		fmt.Println("reload ghostty config (ctrl+shift+,) — applies to new windows")
	} else {
		fmt.Println("ambient mode OFF: new surfaces run your plain shell again")
		fmt.Println("reload ghostty config (ctrl+shift+,)")
	}
	return nil
}

func writeGhosttySnippet(ambient bool) error {
	content := ghosttyNav
	if ambient {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		content += fmt.Sprintf(`
# ambient mode: every surface is a persistent tmux session (gm-*).
# quick terminal and nested-tmux surfaces are exempted by ghostmux shell.
command = %s shell
`, exe)
	}
	return os.WriteFile(ghosttySnippet(), []byte(content), 0o644)
}

func ambientOn() bool { return fileContains(ghosttySnippet(), "\ncommand = ") }

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

	ghosttyVer := firstLine(runOut("ghostty", "+version"))
	check("ghostty", ghosttyVer != "", ghosttyVer)
	if strings.Contains(ghosttyVer, " 1.2.") || strings.Contains(ghosttyVer, " 1.1.") || strings.Contains(ghosttyVer, " 1.0.") {
		fmt.Println("       note: ghostty < 1.3 — performable: fall-through for goto_split may")
		fmt.Println("       swallow keys in some cases (fixed upstream in 1.3); upgrade if nav misbehaves")
	}

	tmuxVer := firstLine(tmux.Output("-V"))
	check("tmux", tmuxVer != "", tmuxVer)

	terminfo := exec.Command("infocmp", "-x", "xterm-ghostty").Run() == nil
	check("terminfo xterm-ghostty", terminfo, "")

	gWired := fileContains(ghosttyConfig(), markerBegin)
	check("ghostty config wired", gWired, ghosttyConfig())

	tWired := fileContains(tmuxConfig(), markerBegin)
	check("tmux config wired", tWired, tmuxConfig())

	inGhostty := os.Getenv("TERM_PROGRAM") == "ghostty" || strings.Contains(os.Getenv("TERM"), "ghostty")
	check("running inside ghostty", inGhostty, "(informational)")

	ambient := "off"
	if ambientOn() {
		ambient = "on"
	}
	fmt.Printf("  [--] %-28s %s\n", "ambient mode", ambient)

	if !ok {
		fmt.Println("\nrun `ghostmux install` to wire configs")
	}
	return nil
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
