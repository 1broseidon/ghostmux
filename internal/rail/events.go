package rail

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

const (
	refreshChannelPrefix  = "ghostmux-refresh-v1-"
	hookRepairAttempts    = 4
	hookRepairSettleDelay = 150 * time.Millisecond
)

// refreshHooks are additive global hooks that nudge a panel to reload. The
// activity hook is opportunistic: tmux only fires it when the user has enabled
// native activity monitoring. ghostmux never changes that option itself.
var refreshHooks = []string{
	"alert-bell",
	"alert-activity",
	"session-created",
	"session-closed",
	"window-linked",
	"window-unlinked",
	"window-renamed",
	// Fires when a session's current window changes, so the rail live-tracks
	// navigation happening inside its viewport client.
	"session-window-changed",
}

var refreshHookSet = func() map[string]bool {
	set := make(map[string]bool, len(refreshHooks))
	for _, hook := range refreshHooks {
		set[hook] = true
	}
	return set
}()

// refreshMsg is posted by a wait-for listener when one of its hooks fires.
type refreshMsg struct{}

type hookEntry struct {
	hook    string
	index   int
	command string // exact tmux-canonical command returned by show-hooks
}

func (e hookEntry) name() string { return fmt.Sprintf("%s[%d]", e.hook, e.index) }

type hookRunner func(args ...string) (string, error)
type hookWaiter func(context.Context, string) error

type messageSender interface{ Send(tea.Msg) }

// HookLease owns only the additive hook-array entries installed for one panel.
// Every mutating operation, including Close, is serialized by mu. Thus Close
// either precedes an attempted install (which then sees closed) or follows it
// and examines the discovered entry during cleanup.
type HookLease struct {
	mu      sync.Mutex
	channel string
	command string
	entries map[string]hookEntry // exact hook[index] -> installed canonical text
	closed  bool
	cancel  context.CancelFunc

	run  hookRunner
	wait hookWaiter
}

// NewHookLease creates an independent cryptographic identity for one panel.
// It does not require a running tmux server; installation belongs to Listen's
// retry loop so panel startup and non-tmux backends never depend on it.
func NewHookLease() (*HookLease, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, fmt.Errorf("create tmux hook lease: %w", err)
	}
	channel := refreshChannelPrefix + strconv.Itoa(os.Getpid()) + "-" + hex.EncodeToString(random[:])
	return newHookLease(channel, tmux.Runner, waitForRefresh), nil
}

func newHookLease(channel string, run hookRunner, wait hookWaiter) *HookLease {
	return &HookLease{
		channel: channel,
		command: refreshHookCommand(channel),
		entries: make(map[string]hookEntry),
		run:     run,
		wait:    wait,
	}
}

// Listen keeps the lease installed and blocks on its private wait-for channel.
// A missing server, a restart, or an unsupported individual hook is partial
// service rather than a startup error. Before every blocking wait it performs
// a bounded repair cycle, including a delayed stability check after appends.
// The same cycle runs after every signal before the listener blocks again.
func (l *HookLease) Listen(parent context.Context, sender messageSender) {
	ctx, cancel := context.WithCancel(parent)
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		cancel()
		return
	}
	l.cancel = cancel
	l.mu.Unlock()
	defer func() {
		cancel()
		l.mu.Lock()
		l.cancel = nil // one listener belongs to one panel lease
		l.mu.Unlock()
	}()

	const minRetry = 100 * time.Millisecond
	const maxRetry = time.Second
	backoff := minRetry
	for {
		if ctx.Err() != nil {
			return
		}
		repair := l.repairHooks(ctx)
		if !repair.working || repair.unstable {
			if !sleepContext(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, maxRetry)
			continue
		}
		backoff = minRetry

		if err := l.wait(ctx, l.channel); err != nil {
			if ctx.Err() != nil {
				return
			}
			if !sleepContext(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, maxRetry)
			continue
		}
		if sender != nil {
			sender.Send(refreshMsg{})
		}
	}
}

type hookStatus struct {
	working  bool
	complete bool
	changed  bool
	appended map[string]bool
	missing  map[string]bool
}

type hookRepair struct {
	working  bool
	unstable bool
}

// repairHooks gives startup and every signaled event a bounded chance to
// settle. A successful append is not considered stable until a later
// show-hooks query sees it unchanged. If a successfully appended hook keeps
// vanishing, the listener backs off and repairs again instead of blocking
// forever. Hooks that consistently reject set-hook are treated as unsupported
// after this bounded cycle, retaining the working partial lease without a
// polling loop.
func (l *HookLease) repairHooks(ctx context.Context) hookRepair {
	appended := make(map[string]bool)
	var status hookStatus
	for attempt := 0; attempt < hookRepairAttempts; attempt++ {
		status = l.ensureHookStatus(ctx)
		if !status.working {
			return hookRepair{}
		}
		for hook := range status.appended {
			appended[hook] = true
		}
		if status.complete && !status.changed {
			return hookRepair{working: true}
		}
		if attempt+1 < hookRepairAttempts && !sleepContext(ctx, hookRepairSettleDelay) {
			return hookRepair{}
		}
	}

	unstable := status.complete && status.changed
	for hook := range status.missing {
		if appended[hook] {
			unstable = true
			break
		}
	}
	return hookRepair{working: status.working, unstable: unstable}
}

// ensureHooks is the focused-test compatibility boundary. The listener uses
// repairHooks so append-time discovery alone never leads directly to a wait.
func (l *HookLease) ensureHooks(ctx context.Context) bool {
	return l.ensureHookStatus(ctx).working
}

// ensureHookStatus verifies tracked canonical entries across tmux's global
// hook scopes in one process, adopts exact-token entries left by an interrupted
// discovery, and appends missing hooks. It holds mu across external commands
// so Close cannot race an undiscovered successful install.
func (l *HookLease) ensureHookStatus(ctx context.Context) hookStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	status := hookStatus{appended: make(map[string]bool), missing: make(map[string]bool)}
	if l.closed || ctx.Err() != nil {
		return status
	}

	// Hooks span tmux option scopes. In tmux 3.4, window-renamed is a
	// window-scoped global hook and is absent from bare `show-hooks -g`; -gw
	// supplies that array. Keep both shows in one tmux command queue/process.
	out, err := l.run("show-hooks", "-g", ";", "show-hooks", "-gw")
	if err != nil {
		return status
	}
	shown := parseHookEntries(out)
	current := make(map[string]hookEntry, len(shown))
	for _, entry := range shown {
		current[entry.name()] = entry
	}

	// A tracked slot remains ours only while its current canonical command is
	// byte-for-byte identical. Replacement loses ownership without touching the
	// replacement; a fresh additive entry is installed below.
	for name, installed := range l.entries {
		entry, ok := current[name]
		if !ok || entry.command != installed.command {
			delete(l.entries, name)
			status.changed = true
		}
	}
	// Token matching is deliberately structural, not a substring search. This
	// also recovers an append that succeeded just before its combined discovery
	// output became unavailable.
	for _, entry := range shown {
		if channel, _, ok := refreshCommandChannel(entry.command); ok &&
			channel == l.channel && refreshHookSet[entry.hook] {
			if installed, tracked := l.entries[entry.name()]; !tracked || installed.command != entry.command {
				status.changed = true
			}
			l.entries[entry.name()] = entry
		}
	}

	covered := make(map[string]bool, len(refreshHooks))
	for _, entry := range l.entries {
		covered[entry.hook] = true
	}
	for _, hook := range refreshHooks {
		if covered[hook] || ctx.Err() != nil || l.closed {
			continue
		}
		// The append and discovery execute in one tmux command queue. No array
		// index is selected by ghostmux, and -a is never omitted.
		out, err := l.run(
			"set-hook", "-ag", hook, l.command,
			";", "show-hooks", "-g", hook,
		)
		if err != nil {
			continue // unsupported hook or transient failure; retain the rest
		}
		for _, entry := range parseHookEntries(out) {
			channel, _, ok := refreshCommandChannel(entry.command)
			if entry.hook == hook && ok && channel == l.channel {
				l.entries[entry.name()] = entry
				covered[hook] = true
				status.appended[hook] = true
				status.changed = true
			}
		}
	}
	for _, hook := range refreshHooks {
		if !covered[hook] {
			status.missing[hook] = true
		}
	}
	status.working = len(l.entries) > 0
	status.complete = len(status.missing) == 0
	return status
}

// Close linearizes with installation, cancels the blocking wait-for child, and
// compare-before-unsets every exact slot. A missing server or a replaced slot
// is left alone. Close is idempotent, though the panel has one explicit call.
func (l *HookLease) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	if l.cancel != nil {
		l.cancel()
	}

	names := make([]string, 0, len(l.entries))
	for name := range l.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		installed := l.entries[name]
		out, err := l.run("show-hooks", "-g", name)
		if err != nil {
			continue
		}
		var current *hookEntry
		for _, entry := range parseHookEntries(out) {
			if entry.name() == name {
				copy := entry
				current = &copy
				break
			}
		}
		if current == nil || current.command != installed.command {
			continue
		}
		_, _ = l.run("set-hook", "-gu", name)
	}
	clear(l.entries)
}

func waitForRefresh(ctx context.Context, channel string) error {
	cmd := exec.CommandContext(ctx, "tmux", tmux.Argv("wait-for", channel)...)
	return cmd.Run()
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// refreshHookCommand safely crosses both parsers involved in a hook: tmux's
// command parser and run-shell's POSIX shell. Every nested tmux argv element is
// independently single-quoted; the complete shell command is then encoded as
// one tmux double-quoted argument. This path intentionally does not use the
// presentation-only ArgvString concatenation.
func refreshHookCommand(channel string) string {
	argv := append([]string{"tmux"}, tmux.Argv("wait-for", "-S", channel)...)
	words := make([]string, len(argv))
	for i, arg := range argv {
		words[i] = shellQuote(arg)
	}
	// The hook only emits a signal, so run it in the background rather than
	// holding tmux's event command queue open for the helper process.
	return "run-shell -b " + tmuxDoubleQuote(strings.Join(words, " "))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func tmuxDoubleQuote(value string) string {
	var b strings.Builder
	b.Grow(len(value) + 2)
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\', '"', '$':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '#':
			// run-shell expands formats. ## reaches the shell as one literal #.
			b.WriteString("##")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func parseHookEntries(out string) []hookEntry {
	out = strings.TrimSuffix(out, "\n")
	if out == "" {
		return nil
	}
	var entries []hookEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSuffix(line, "\r")
		space := strings.IndexByte(line, ' ')
		if space <= 0 || space == len(line)-1 {
			continue
		}
		name, command := line[:space], line[space+1:]
		open, close := strings.LastIndexByte(name, '['), strings.LastIndexByte(name, ']')
		if open <= 0 || close != len(name)-1 || open+1 == close {
			continue
		}
		index, err := strconv.Atoi(name[open+1 : close])
		if err != nil || index < 0 {
			continue
		}
		entries = append(entries, hookEntry{hook: name[:open], index: index, command: command})
	}
	return entries
}

// RefreshHookEntry is one exact doctor-visible lease capability.
type RefreshHookEntry struct {
	Hook    string
	Index   int
	Command string
}

func (e RefreshHookEntry) Name() string { return fmt.Sprintf("%s[%d]", e.Hook, e.Index) }

// RefreshHookLeaseInfo groups all recognized entries sharing one versioned
// channel. PID is reporting evidence only; HookLease cleanup never consults it.
type RefreshHookLeaseInfo struct {
	Channel  string
	PID      int
	Entries  []RefreshHookEntry
	Missing  []string
	Expected int
	Complete bool
}

// RecognizeRefreshHookLeases strictly recognizes commands emitted by
// refreshHookCommand and groups the eight expected hook entries by channel.
// It is shared with doctor so reporting and ownership use one grammar.
func RecognizeRefreshHookLeases(out string) []RefreshHookLeaseInfo {
	byChannel := make(map[string]*RefreshHookLeaseInfo)
	for _, entry := range parseHookEntries(out) {
		if !refreshHookSet[entry.hook] {
			continue
		}
		channel, pid, ok := refreshCommandChannel(entry.command)
		if !ok {
			continue
		}
		lease := byChannel[channel]
		if lease == nil {
			lease = &RefreshHookLeaseInfo{Channel: channel, PID: pid}
			byChannel[channel] = lease
		}
		lease.Entries = append(lease.Entries, RefreshHookEntry{
			Hook: entry.hook, Index: entry.index, Command: entry.command,
		})
	}

	channels := make([]string, 0, len(byChannel))
	for channel := range byChannel {
		channels = append(channels, channel)
	}
	sort.Strings(channels)
	leases := make([]RefreshHookLeaseInfo, 0, len(channels))
	for _, channel := range channels {
		lease := byChannel[channel]
		sort.Slice(lease.Entries, func(i, j int) bool {
			if lease.Entries[i].Hook == lease.Entries[j].Hook {
				return lease.Entries[i].Index < lease.Entries[j].Index
			}
			return lease.Entries[i].Hook < lease.Entries[j].Hook
		})
		seen := make(map[string]bool, len(lease.Entries))
		for _, entry := range lease.Entries {
			seen[entry.Hook] = true
		}
		lease.Expected = len(refreshHooks)
		for _, hook := range refreshHooks {
			if !seen[hook] {
				lease.Missing = append(lease.Missing, hook)
			}
		}
		lease.Complete = len(lease.Missing) == 0 && len(lease.Entries) == len(refreshHooks)
		leases = append(leases, *lease)
	}
	return leases
}

func refreshCommandChannel(command string) (string, int, bool) {
	const prefix = "run-shell -b \""
	if !strings.HasPrefix(command, prefix) || !strings.HasSuffix(command, "\"") {
		return "", 0, false
	}
	encoded := command[len(prefix) : len(command)-1]
	shell, ok := decodeTmuxDoubleQuoted(encoded)
	if !ok {
		return "", 0, false
	}
	argv, ok := parseShellQuotedArgv(shell)
	if !ok || len(argv) < 4 || argv[0] != "tmux" ||
		argv[len(argv)-3] != "wait-for" || argv[len(argv)-2] != "-S" {
		return "", 0, false
	}
	channel := argv[len(argv)-1]
	pid, ok := parseRefreshChannel(channel)
	return channel, pid, ok
}

func decodeTmuxDoubleQuoted(value string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' {
			b.WriteByte(value[i])
			continue
		}
		i++
		if i >= len(value) || (value[i] != '\\' && value[i] != '"' && value[i] != '$') {
			return "", false
		}
		b.WriteByte(value[i])
	}
	return b.String(), true
}

// parseShellQuotedArgv accepts only the exact single-quoted word grammar
// emitted by shellQuote, including its close/escape/reopen representation of
// a literal quote.
func parseShellQuotedArgv(command string) ([]string, bool) {
	if command == "" {
		return nil, false
	}
	var argv []string
	for pos := 0; pos < len(command); {
		if command[pos] != '\'' {
			return nil, false
		}
		pos++
		var word strings.Builder
		for {
			end := strings.IndexByte(command[pos:], '\'')
			if end < 0 {
				return nil, false
			}
			word.WriteString(command[pos : pos+end])
			pos += end + 1
			if pos == len(command) || command[pos] == ' ' {
				break
			}
			if pos+2 >= len(command) || command[pos] != '\\' ||
				command[pos+1] != '\'' || command[pos+2] != '\'' {
				return nil, false
			}
			word.WriteByte('\'')
			pos += 3
		}
		argv = append(argv, word.String())
		if pos == len(command) {
			break
		}
		pos++
		if pos == len(command) || command[pos] == ' ' {
			return nil, false
		}
	}
	return argv, true
}

func parseRefreshChannel(channel string) (int, bool) {
	if !strings.HasPrefix(channel, refreshChannelPrefix) {
		return 0, false
	}
	rest := strings.TrimPrefix(channel, refreshChannelPrefix)
	dash := strings.IndexByte(rest, '-')
	if dash <= 0 || dash == len(rest)-1 {
		return 0, false
	}
	pid, err := strconv.Atoi(rest[:dash])
	if err != nil || pid <= 0 {
		return 0, false
	}
	random := rest[dash+1:]
	if len(random) != 32 {
		return 0, false
	}
	for _, r := range random {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return 0, false
		}
	}
	return pid, true
}

// debugRefresh logs the refresh source and a timestamp to stderr when
// GHOSTMUX_DEBUG=1, used by the event-latency acceptance check.
func debugRefresh(source string) {
	if os.Getenv("GHOSTMUX_DEBUG") == "1" {
		fmt.Fprintf(os.Stderr, "ghostmux refresh source=%s t=%s\n", source, time.Now().Format(time.RFC3339Nano))
	}
}
