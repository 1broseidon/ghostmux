package rail

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

const (
	testChannelOne = "ghostmux-refresh-v1-101-000102030405060708090a0b0c0d0e0f"
	testChannelTwo = "ghostmux-refresh-v1-202-101112131415161718191a1b1c1d1e1f"
)

type fakeHookServer struct {
	mu             sync.Mutex
	running        bool
	hooks          map[string]map[int]string
	calls          [][]string
	started        chan struct{}
	release        chan struct{}
	blockOne       bool
	failHooks      map[string]bool
	failCounts     map[string]int
	vanishAfterAdd map[string]int
}

func newFakeHookServer() *fakeHookServer {
	return &fakeHookServer{running: true, hooks: make(map[string]map[int]string)}
}

func (s *fakeHookServer) run(args ...string) (string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, append([]string(nil), args...))
	if !s.running {
		s.mu.Unlock()
		return "", errors.New("no server")
	}
	if len(args) == 0 {
		s.mu.Unlock()
		return "", errors.New("empty command")
	}
	switch args[0] {
	case "show-hooks":
		if len(args) == 5 && args[1] == "-g" && args[2] == ";" &&
			args[3] == "show-hooks" && args[4] == "-gw" {
			out := s.showScopeLocked(false) + s.showScopeLocked(true)
			s.mu.Unlock()
			return out, nil
		}
		target := ""
		if len(args) > 2 {
			target = args[2]
		}
		out := s.showLocked(target)
		s.mu.Unlock()
		return out, nil
	case "set-hook":
		if len(args) >= 4 && args[1] == "-ag" {
			hook, command := args[2], args[3]
			if s.failHooks[hook] || s.failCounts[hook] > 0 {
				if s.failCounts[hook] > 0 {
					s.failCounts[hook]--
				}
				s.mu.Unlock()
				return "", errors.New("hook unsupported")
			}
			if s.hooks[hook] == nil {
				s.hooks[hook] = make(map[int]string)
			}
			index := 0
			for {
				if _, occupied := s.hooks[hook][index]; !occupied {
					break
				}
				index++
			}
			s.hooks[hook][index] = command
			block := s.blockOne
			if block {
				s.blockOne = false
				close(s.started)
			}
			s.mu.Unlock()
			if block {
				<-s.release
			}
			s.mu.Lock()
			out := s.showLocked(hook)
			if s.vanishAfterAdd[hook] > 0 {
				s.vanishAfterAdd[hook]--
				delete(s.hooks[hook], index)
			}
			s.mu.Unlock()
			return out, nil
		}
		if len(args) == 3 && args[1] == "-gu" {
			entries := parseHookEntries(args[2] + " placeholder")
			if len(entries) != 1 {
				s.mu.Unlock()
				return "", fmt.Errorf("bad unset target %q", args[2])
			}
			delete(s.hooks[entries[0].hook], entries[0].index)
			s.mu.Unlock()
			return "", nil
		}
	}
	s.mu.Unlock()
	return "", fmt.Errorf("unexpected command %v", args)
}

func (s *fakeHookServer) showLocked(target string) string {
	return s.showFilteredLocked(target, nil)
}

func (s *fakeHookServer) showScopeLocked(window bool) string {
	return s.showFilteredLocked("", func(hook string) bool {
		return (hook == "window-renamed") == window
	})
}

func (s *fakeHookServer) showFilteredLocked(target string, include func(string) bool) string {
	var lines []string
	for hook, indexed := range s.hooks {
		if include != nil && !include(hook) {
			continue
		}
		for index, command := range indexed {
			name := fmt.Sprintf("%s[%d]", hook, index)
			if target != "" && target != hook && target != name {
				continue
			}
			lines = append(lines, name+" "+command)
		}
	}
	sort.Strings(lines)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

func (s *fakeHookServer) seed(hook string, index int, command string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hooks[hook] == nil {
		s.hooks[hook] = make(map[int]string)
	}
	s.hooks[hook][index] = command
}

func (s *fakeHookServer) replace(name, command string) {
	entries := parseHookEntries(name + " placeholder")
	if len(entries) != 1 {
		panic("invalid fake replacement")
	}
	s.seed(entries[0].hook, entries[0].index, command)
}

func (s *fakeHookServer) restart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks = make(map[string]map[int]string)
	s.running = true
}

func (s *fakeHookServer) removeLeaseHook(hook, channel string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for index, command := range s.hooks[hook] {
		got, _, ok := refreshCommandChannel(command)
		if ok && got == channel {
			delete(s.hooks[hook], index)
		}
	}
}

func (s *fakeHookServer) setVanishAfterAdd(hook string, count int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vanishAfterAdd == nil {
		s.vanishAfterAdd = make(map[string]int)
	}
	s.vanishAfterAdd[hook] = count
}

func (s *fakeHookServer) setHookCallCount(hook string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, call := range s.calls {
		if len(call) >= 3 && call[0] == "set-hook" && call[1] == "-ag" && call[2] == hook {
			count++
		}
	}
	return count
}

func (s *fakeHookServer) ghostEntries() []hookEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var entries []hookEntry
	for hook, indexed := range s.hooks {
		for index, command := range indexed {
			if _, _, ok := refreshCommandChannel(command); ok {
				entries = append(entries, hookEntry{hook: hook, index: index, command: command})
			}
		}
	}
	return entries
}

func TestNewHookLeaseUsesIndependentVersionedRandomChannels(t *testing.T) {
	first, err := NewHookLease()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewHookLease()
	if err != nil {
		t.Fatal(err)
	}
	if first.channel == second.channel {
		t.Fatalf("two leases reused channel %q", first.channel)
	}
	if _, ok := parseRefreshChannel(first.channel); !ok {
		t.Fatalf("first channel has invalid shape %q", first.channel)
	}
	if _, ok := parseRefreshChannel(second.channel); !ok {
		t.Fatalf("second channel has invalid shape %q", second.channel)
	}
	first.Close()
	second.Close()
}

func TestHookLeaseAdditiveDiscoveryAndExactCleanup(t *testing.T) {
	server := newFakeHookServer()
	server.seed("alert-bell", 133, "display-message user-hook")
	lease := newHookLease(testChannelOne, server.run, nil)
	if !lease.ensureHooks(context.Background()) {
		t.Fatal("lease did not install any hooks")
	}
	if len(lease.entries) != len(refreshHooks) {
		t.Fatalf("discovered entries = %d, want %d", len(lease.entries), len(refreshHooks))
	}
	for _, call := range server.calls {
		if len(call) > 0 && call[0] == "set-hook" && (len(call) < 2 || call[1] != "-ag") {
			t.Fatalf("non-additive hook install: %v", call)
		}
	}
	server.mu.Lock()
	if got := server.hooks["alert-bell"][133]; got != "display-message user-hook" {
		server.mu.Unlock()
		t.Fatalf("occupied user index changed to %q", got)
	}
	server.mu.Unlock()

	lease.Close()
	if got := server.ghostEntries(); len(got) != 0 {
		t.Fatalf("exact cleanup left ghost entries: %+v", got)
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if got := server.hooks["alert-bell"][133]; got != "display-message user-hook" {
		t.Fatalf("cleanup removed user index: %q", got)
	}
}

func TestHookLeaseVerificationReadsSessionAndWindowGlobalScopes(t *testing.T) {
	server := newFakeHookServer()
	lease := newHookLease(testChannelOne, server.run, nil)
	if !lease.ensureHooks(context.Background()) {
		t.Fatal("initial install failed")
	}
	status := lease.ensureHookStatus(context.Background())
	if !status.complete || status.changed {
		t.Fatalf("mixed-scope verification = %+v", status)
	}
	server.mu.Lock()
	windowEntries := len(server.hooks["window-renamed"])
	server.mu.Unlock()
	if windowEntries != 1 {
		t.Fatalf("window-global hook was treated as missing and duplicated: %d entries", windowEntries)
	}
	lease.Close()
}

func TestHookLeasePartialFailureRetainsWorkingHooks(t *testing.T) {
	server := newFakeHookServer()
	server.failHooks = map[string]bool{"alert-activity": true}
	lease := newHookLease(testChannelOne, server.run, nil)
	if !lease.ensureHooks(context.Background()) {
		t.Fatal("one unsupported hook disabled the entire lease")
	}
	if got := len(lease.entries); got != len(refreshHooks)-1 {
		t.Fatalf("partial install retained %d hooks, want %d", got, len(refreshHooks)-1)
	}
	for _, entry := range lease.entries {
		if entry.hook == "alert-activity" {
			t.Fatal("failed hook was recorded as installed")
		}
	}
	lease.Close()
}

func TestHookLeaseBoundedStartupRepairFixesTransientSingleHookFailure(t *testing.T) {
	server := newFakeHookServer()
	server.failCounts = map[string]int{"window-renamed": 1}
	waitStarted := make(chan struct{})
	var once sync.Once
	lease := newHookLease(testChannelOne, server.run, func(ctx context.Context, _ string) error {
		once.Do(func() { close(waitStarted) })
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		lease.Listen(ctx, nil)
		close(done)
	}()

	select {
	case <-waitStarted:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("listener blocked before repairing transient startup failure")
	}
	if got := len(server.ghostEntries()); got != len(refreshHooks) {
		cancel()
		t.Fatalf("startup repair reached wait with %d hooks, want %d", got, len(refreshHooks))
	}
	if got := server.setHookCallCount("window-renamed"); got < 2 {
		cancel()
		t.Fatalf("transient hook install attempts = %d, want at least 2", got)
	}
	cancel()
	lease.Close()
	<-done
}

func TestHookLeaseRepairsVanishedAppendAfterEverySignal(t *testing.T) {
	server := newFakeHookServer()
	secondWait := make(chan struct{})
	waits := 0
	lease := newHookLease(testChannelOne, server.run, func(ctx context.Context, _ string) error {
		waits++
		if waits == 1 {
			// Reproduce tmux accepting and showing an append while the signaled
			// startup callback is still settling, then losing that entry.
			server.removeLeaseHook("window-renamed", testChannelOne)
			server.setVanishAfterAdd("window-renamed", 1)
			return nil
		}
		close(secondWait)
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		lease.Listen(ctx, nil)
		close(done)
	}()

	select {
	case <-secondWait:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("listener did not finish post-signal stability repair")
	}
	if got := len(server.ghostEntries()); got != len(refreshHooks) {
		cancel()
		t.Fatalf("post-signal repair reached wait with %d hooks, want %d", got, len(refreshHooks))
	}
	if got := server.setHookCallCount("window-renamed"); got < 3 {
		cancel()
		t.Fatalf("vanished append attempts = %d, want initial + two repairs", got)
	}
	cancel()
	lease.Close()
	<-done
}

func TestHookLeaseUnsupportedHookDegradesWithoutPolling(t *testing.T) {
	server := newFakeHookServer()
	server.failHooks = map[string]bool{"alert-activity": true}
	waitStarted := make(chan struct{})
	var once sync.Once
	lease := newHookLease(testChannelOne, server.run, func(ctx context.Context, _ string) error {
		once.Do(func() { close(waitStarted) })
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		lease.Listen(ctx, nil)
		close(done)
	}()

	select {
	case <-waitStarted:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("partial lease never entered wait")
	}
	if got := len(server.ghostEntries()); got != len(refreshHooks)-1 {
		cancel()
		t.Fatalf("unsupported-hook lease has %d entries", got)
	}
	if got := server.setHookCallCount("alert-activity"); got != hookRepairAttempts {
		cancel()
		t.Fatalf("bounded unsupported attempts = %d, want %d", got, hookRepairAttempts)
	}
	calls := server.setHookCallCount("alert-activity")
	time.Sleep(2 * hookRepairSettleDelay)
	if got := server.setHookCallCount("alert-activity"); got != calls {
		cancel()
		t.Fatalf("unsupported hook busy-polled while waiting: %d -> %d calls", calls, got)
	}
	cancel()
	lease.Close()
	<-done
}

func TestHookLeaseReplacementSafeCleanup(t *testing.T) {
	server := newFakeHookServer()
	lease := newHookLease(testChannelOne, server.run, nil)
	lease.ensureHooks(context.Background())
	var replaced hookEntry
	for _, entry := range lease.entries {
		replaced = entry
		break
	}
	server.replace(replaced.name(), "display-message replacement")
	lease.Close()

	server.mu.Lock()
	defer server.mu.Unlock()
	if got := server.hooks[replaced.hook][replaced.index]; got != "display-message replacement" {
		t.Fatalf("replacement was removed or changed: %q", got)
	}
}

func TestTwoHookLeasesCoexistAndCloseIndependently(t *testing.T) {
	server := newFakeHookServer()
	first := newHookLease(testChannelOne, server.run, nil)
	second := newHookLease(testChannelTwo, server.run, nil)
	first.ensureHooks(context.Background())
	second.ensureHooks(context.Background())
	if got := len(server.ghostEntries()); got != 2*len(refreshHooks) {
		t.Fatalf("coexisting entries = %d, want %d", got, 2*len(refreshHooks))
	}

	first.Close()
	entries := server.ghostEntries()
	if len(entries) != len(refreshHooks) {
		t.Fatalf("first close left %d entries, want second lease's %d", len(entries), len(refreshHooks))
	}
	for _, entry := range entries {
		channel, _, _ := refreshCommandChannel(entry.command)
		if channel != testChannelTwo {
			t.Fatalf("first lease survived close: %+v", entry)
		}
	}
	second.Close()
}

func TestHookLeaseRetriesNoServerAndReinstallsAfterRestart(t *testing.T) {
	server := newFakeHookServer()
	server.running = false
	waitStarted := make(chan struct{}, 1)
	lease := newHookLease(testChannelOne, server.run, func(ctx context.Context, _ string) error {
		select {
		case waitStarted <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		lease.Listen(ctx, nil)
		close(done)
	}()

	time.Sleep(120 * time.Millisecond) // permit at least one unavailable attempt
	server.mu.Lock()
	server.running = true
	server.mu.Unlock()
	select {
	case <-waitStarted:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("listener did not retry installation when server appeared")
	}
	if got := len(server.ghostEntries()); got != len(refreshHooks) {
		cancel()
		t.Fatalf("retry installed %d hooks, want %d", got, len(refreshHooks))
	}

	server.restart()
	if !lease.ensureHooks(context.Background()) {
		cancel()
		t.Fatal("lease did not reinstall after server restart")
	}
	if got := len(server.ghostEntries()); got != len(refreshHooks) {
		cancel()
		t.Fatalf("restart reinstall produced %d hooks", got)
	}
	cancel()
	lease.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocking listener did not stop on cancellation")
	}
}

func TestHookLeaseCloseInstallRaceCannotLeak(t *testing.T) {
	server := newFakeHookServer()
	server.started = make(chan struct{})
	server.release = make(chan struct{})
	server.blockOne = true
	lease := newHookLease(testChannelOne, server.run, nil)

	installed := make(chan struct{})
	go func() {
		lease.ensureHooks(context.Background())
		close(installed)
	}()
	<-server.started
	closed := make(chan struct{})
	go func() {
		lease.Close()
		close(closed)
	}()
	close(server.release)
	<-installed
	<-closed
	if got := server.ghostEntries(); len(got) != 0 {
		t.Fatalf("close/install race leaked entries: %+v", got)
	}
	if lease.ensureHooks(context.Background()) {
		t.Fatal("closed lease installed again")
	}
}

func TestRefreshHookRecognitionIsExactAndGroupsOneToken(t *testing.T) {
	var lines []string
	for i, hook := range refreshHooks {
		lines = append(lines, fmt.Sprintf("%s[%d] %s", hook, i+10, refreshHookCommand(testChannelOne)))
	}
	lines = append(lines,
		"alert-bell[99] run-shell \"'echo' 'ghostmux-refresh-v1-101-000102030405060708090a0b0c0d0e0f'\"",
		"after-select-window[5] "+refreshHookCommand(testChannelOne),
		"alert-bell[100] run-shell -b \"'tmux' 'wait-for' '-S' 'ghostmux-refresh-v1-101-short'\"",
	)
	leases := RecognizeRefreshHookLeases(strings.Join(lines, "\n") + "\n")
	if len(leases) != 1 || !leases[0].Complete || leases[0].PID != 101 || len(leases[0].Entries) != 8 {
		t.Fatalf("recognized leases = %+v", leases)
	}
}

func TestRefreshHookCommandShellQuotesTmuxArgs(t *testing.T) {
	t.Setenv("GHOSTMUX_TMUX_ARGS", "-L socket';touch /tmp/ghostmux-must-not-exist")
	command := refreshHookCommand(testChannelOne)
	encoded := strings.TrimSuffix(strings.TrimPrefix(command, "run-shell -b \""), "\"")
	shell, ok := decodeTmuxDoubleQuoted(encoded)
	if !ok {
		t.Fatalf("cannot decode generated command %q", command)
	}
	argv, ok := parseShellQuotedArgv(shell)
	if !ok {
		t.Fatalf("cannot parse generated shell argv %q", shell)
	}
	want := []string{"tmux", "-L", "socket';touch", "/tmp/ghostmux-must-not-exist", "wait-for", "-S", testChannelOne}
	if strings.Join(argv, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("nested argv = %q, want %q", argv, want)
	}
}

func TestScratchTmuxHookLeaseNoServerAndRestart(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	socket := fmt.Sprintf("gm-hook-restart-%d", time.Now().UnixNano())
	t.Setenv("GHOSTMUX_TMUX_ARGS", "-L "+socket+" -f /dev/null")
	t.Cleanup(func() { _ = tmux.Run("kill-server") })
	lease := newHookLease(testChannelOne, tmux.Runner, nil)
	if lease.ensureHooks(context.Background()) {
		t.Fatal("lease reported hooks before a server existed")
	}
	if err := tmux.Run("new-session", "-d", "-s", "first"); err != nil {
		t.Fatal(err)
	}
	if !lease.ensureHooks(context.Background()) || len(lease.entries) != len(refreshHooks) {
		t.Fatalf("no-server recovery entries = %d", len(lease.entries))
	}
	if err := tmux.Run("kill-server"); err != nil {
		t.Fatal(err)
	}
	var restartErr error
	for attempt := 0; attempt < 20; attempt++ {
		restartErr = tmux.Run("new-session", "-d", "-s", "second")
		if restartErr == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if restartErr != nil {
		t.Fatal(restartErr)
	}
	if !lease.ensureHooks(context.Background()) || len(lease.entries) != len(refreshHooks) {
		t.Fatalf("restart reinstall entries = %d", len(lease.entries))
	}
	lease.Close()
	out, _ := tmux.Runner("show-hooks", "-g", ";", "show-hooks", "-gw")
	if got := len(RecognizeRefreshHookLeases(out)); got != 0 {
		t.Fatalf("restart lease cleanup left %d recognized leases", got)
	}
}

func TestScratchTmuxHookLeasesPreserveOccupiedIndexAndScalars(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	originalRunner := tmux.Runner
	socket := fmt.Sprintf("gm-hook-lease-%d", time.Now().UnixNano())
	t.Setenv("GHOSTMUX_TMUX_ARGS", "-L "+socket+" -f /dev/null")
	t.Cleanup(func() {
		_ = tmux.Run("kill-server")
		tmux.Runner = originalRunner
	})
	if err := tmux.Run("new-session", "-d", "-s", "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := tmux.Run("set-option", "-g", "monitor-activity", "off"); err != nil {
		t.Fatal(err)
	}
	if err := tmux.Run("set-option", "-g", "visual-activity", "on"); err != nil {
		t.Fatal(err)
	}
	if err := tmux.Run("set-hook", "-g", "alert-bell[133]", "display-message user-hook"); err != nil {
		t.Fatal(err)
	}

	first := newHookLease(testChannelOne, tmux.Runner, nil)
	second := newHookLease(testChannelTwo, tmux.Runner, nil)
	if !first.ensureHooks(context.Background()) || !second.ensureHooks(context.Background()) {
		t.Fatal("scratch lease install failed")
	}
	out, err := tmux.Runner("show-hooks", "-g", ";", "show-hooks", "-gw")
	if err != nil {
		t.Fatal(err)
	}
	leases := RecognizeRefreshHookLeases(out)
	if len(leases) != 2 || !leases[0].Complete || !leases[1].Complete ||
		!strings.Contains(out, "alert-bell[133] display-message user-hook") {
		t.Fatalf("scratch hooks not complete and additive:\n%s", out)
	}
	if got := strings.TrimSpace(tmux.Output("show-options", "-gv", "monitor-activity")); got != "off" {
		t.Fatalf("monitor-activity changed to %q", got)
	}
	if got := strings.TrimSpace(tmux.Output("show-options", "-gv", "visual-activity")); got != "on" {
		t.Fatalf("visual-activity changed to %q", got)
	}

	first.Close()
	out, _ = tmux.Runner("show-hooks", "-g", ";", "show-hooks", "-gw")
	leases = RecognizeRefreshHookLeases(out)
	if len(leases) != 1 || leases[0].Channel != testChannelTwo || !leases[0].Complete || !strings.Contains(out, "alert-bell[133] display-message user-hook") {
		t.Fatalf("independent close damaged hooks:\n%s", out)
	}
	second.Close()
	out, _ = tmux.Runner("show-hooks", "-g", ";", "show-hooks", "-gw")
	if len(RecognizeRefreshHookLeases(out)) != 0 || !strings.Contains(out, "alert-bell[133] display-message user-hook") {
		t.Fatalf("scratch cleanup damaged user hook:\n%s", out)
	}
}
