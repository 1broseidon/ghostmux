package term

import (
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// widget builds a notify-less widget and registers its cleanup.
func widget(t *testing.T, cols, rows int) *Widget {
	t.Helper()
	w := New(cols, rows, nil)
	t.Cleanup(w.Close)
	return w
}

// gridLine returns line y of the plain-text frame, right-trimmed.
func gridLine(w *Widget, y int) string {
	lines := strings.Split(w.Text(), "\n")
	if y >= len(lines) {
		return ""
	}
	return strings.TrimRight(lines[y], " ")
}

func waitForText(t *testing.T, w *Widget, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(w.Text(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("terminal never rendered %q: %q", want, w.Text())
}

func TestTextAndCRLF(t *testing.T) {
	w := widget(t, 20, 4)
	w.Write([]byte("hello\r\nworld"))
	if got := gridLine(w, 0); got != "hello" {
		t.Errorf("line 0 = %q, want %q", got, "hello")
	}
	if got := gridLine(w, 1); got != "world" {
		t.Errorf("line 1 = %q, want %q", got, "world")
	}
}

func TestSGRTruecolorSurvivesRender(t *testing.T) {
	w := widget(t, 20, 2)
	w.Write([]byte("\x1b[38;2;254;128;25mfire\x1b[0m"))
	if got := gridLine(w, 0); got != "fire" {
		t.Errorf("plain text = %q, want %q", got, "fire")
	}
	styled := w.emu.Render()
	if !strings.Contains(styled, "38;2;254;128;25") {
		t.Errorf("styled render lost the truecolor SGR: %q", styled)
	}
}

func TestAltScreenEnterExit(t *testing.T) {
	w := widget(t, 20, 3)
	w.Write([]byte("main screen"))
	w.Write([]byte("\x1b[?1049h")) // enter alt screen
	if !w.IsAltScreen() {
		t.Fatalf("IsAltScreen = false after 1049h")
	}
	w.Write([]byte("\x1b[2J\x1b[Halt content"))
	if got := gridLine(w, 0); got != "alt content" {
		t.Errorf("alt line 0 = %q, want %q", got, "alt content")
	}
	w.Write([]byte("\x1b[?1049l")) // back to main
	if w.IsAltScreen() {
		t.Fatalf("IsAltScreen = true after 1049l")
	}
	if got := gridLine(w, 0); got != "main screen" {
		t.Errorf("main screen not restored: line 0 = %q", got)
	}
}

func TestCursorAddressingAndClear(t *testing.T) {
	w := widget(t, 20, 5)
	w.Write([]byte("\x1b[2J\x1b[3;5Hmark"))
	if got := gridLine(w, 2); got != "    mark" {
		t.Errorf("line 2 = %q, want %q", got, "    mark")
	}
	w.Write([]byte("\x1b[2J\x1b[H"))
	for y := range 5 {
		if got := gridLine(w, y); got != "" {
			t.Errorf("line %d not cleared: %q", y, got)
		}
	}
}

func TestWideCJKRunes(t *testing.T) {
	w := widget(t, 20, 2)
	w.Write([]byte("日本語 ok"))
	if got := gridLine(w, 0); got != "日本語 ok" {
		t.Errorf("line 0 = %q, want %q", got, "日本語 ok")
	}
	// Wide runes occupy two cells: cursor should be at column 3*2+3 = 9.
	if pos := w.emu.CursorPosition(); pos.X != 9 {
		t.Errorf("cursor X = %d, want 9 (wide runes two cells each)", pos.X)
	}
}

func TestResizeMidStreamNoPanic(t *testing.T) {
	w := widget(t, 40, 10)
	half := strings.Repeat("x", 100) + "\x1b[38;2;1;2" // split mid-SGR
	w.Write([]byte(half))
	w.Resize(20, 5)
	w.Write([]byte(";3mafter"))
	w.Resize(80, 24)
	w.Write([]byte("\r\ndone"))
	// No assertion beyond survival + something rendered.
	if w.Text() == "" {
		t.Errorf("empty render after resize storm")
	}
}

func TestViewJoinsRowsWithNewline(t *testing.T) {
	w := widget(t, 10, 3)
	if got := strings.Count(w.View(), "\n"); got != 2 {
		t.Errorf("View has %d newlines, want 2 (rows-1)", got)
	}
}

func TestCursorOverlayOnlyWhenFocusedAndRunning(t *testing.T) {
	w := widget(t, 10, 2)
	w.Write([]byte("ab"))
	// Not running, not focused: no overlay.
	if strings.Contains(w.View(), "\x1b[7m") {
		t.Errorf("overlay present on unfocused view")
	}
	w.Focus()
	// Focused but no child running: still no overlay (no live cursor).
	if strings.Contains(w.View(), "\x1b[7m") {
		t.Errorf("overlay present with no running child")
	}
}

func TestOverlayCursorReversesCell(t *testing.T) {
	got := overlayCursor("abc\ndef", 1, 1)
	want := "abc\nd\x1b[7me\x1b[27mf"
	if got != want {
		t.Errorf("overlayCursor = %q, want %q", got, want)
	}
	// Past end of line: pads with spaces and reverses a blank.
	got = overlayCursor("ab", 5, 0)
	if !strings.Contains(got, "ab   \x1b[7m \x1b[27m") {
		t.Errorf("overlay past EOL = %q", got)
	}
	// Out-of-range row: frame unchanged.
	if overlayCursor("ab", 0, 3) != "ab" {
		t.Errorf("out-of-range row mutated frame")
	}
}

func TestOutputMsgCoalesced(t *testing.T) {
	var mu sync.Mutex
	var msgs []tea.Msg
	w := New(10, 2, func(m tea.Msg) {
		mu.Lock()
		msgs = append(msgs, m)
		mu.Unlock()
	})
	t.Cleanup(w.Close)
	// Simulate a hot child: many writes inside one coalescing window.
	for range 50 {
		w.Write([]byte("x"))
		w.noteOutput()
	}
	time.Sleep(3 * coalesceWindow)
	mu.Lock()
	n := len(msgs)
	mu.Unlock()
	if n != 1 {
		t.Errorf("got %d OutputMsg for 50 writes in one window, want 1", n)
	}
	// A write after the window fires again — the trailing frame is not lost.
	w.noteOutput()
	time.Sleep(3 * coalesceWindow)
	mu.Lock()
	n = len(msgs)
	mu.Unlock()
	if n != 2 {
		t.Errorf("got %d OutputMsg after second window, want 2", n)
	}
}

func TestCloseInterruptsBlockedInputForwarder(t *testing.T) {
	w := New(10, 2, nil)
	closed := make(chan struct{})
	go func() {
		w.Close()
		close(closed)
	}()

	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked while the input forwarder was waiting in Read")
	}
	select {
	case <-w.inputDone:
	default:
		t.Fatal("Close returned before the input forwarder stopped")
	}
}

func TestConcurrentCloseIsIdempotent(t *testing.T) {
	w := New(10, 2, nil)
	t.Cleanup(w.Close)
	if err := w.Start([]string{"sh", "-c", "while :; do printf x; done"}, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 3 {
				w.Close()
			}
		}()
	}
	joined := make(chan struct{})
	go func() {
		wg.Wait()
		close(joined)
	}()
	select {
	case <-joined:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent Close calls did not return")
	}
}

func TestStartAfterCloseReturnsClosedPipe(t *testing.T) {
	w := New(10, 2, nil)
	w.Close()
	if err := w.Start([]string{"sh", "-c", "exit 0"}, nil); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Start after Close = %v, want io.ErrClosedPipe", err)
	}
	w.Stop()
	w.Close()
}

func TestRepeatedStartReapsPriorChildBeforeFailure(t *testing.T) {
	w := widget(t, 40, 5)
	if err := w.Start([]string{"sleep", "30"}, nil); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	w.mu.Lock()
	first := w.child
	w.mu.Unlock()

	if err := w.Start([]string{"/definitely/not/a/ghostmux-command"}, nil); err == nil {
		t.Fatal("second Start unexpectedly succeeded")
	}
	select {
	case <-first.done:
	default:
		t.Fatal("repeated Start did not reap the prior child before returning")
	}
	select {
	case <-first.readerDone:
	default:
		t.Fatal("repeated Start did not join the prior PTY reader")
	}
	if w.Running() {
		t.Fatal("failed replacement Start left a child running")
	}
}

func TestStartStopStartForwardsInput(t *testing.T) {
	w := widget(t, 40, 8)
	startEcho := func(prefix string) {
		t.Helper()
		argv := []string{
			"sh", "-c",
			`stty -echo; IFS= read -r line; printf '%s:%s\r\n' "$1" "$line"; sleep 30`,
			"sh", prefix,
		}
		if err := w.Start(argv, nil); err != nil {
			t.Fatalf("Start %s child: %v", prefix, err)
		}
	}

	startEcho("first")
	w.SendText("alpha\r")
	waitForText(t, w, "first:alpha")
	w.Stop()
	startEcho("second")
	w.SendText("beta\r")
	waitForText(t, w, "second:beta")
}

func TestStopJoinsPTYOutputReader(t *testing.T) {
	w := widget(t, 40, 5)
	if err := w.Start([]string{"sleep", "30"}, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	w.mu.Lock()
	c := w.child
	w.mu.Unlock()
	if c == nil {
		t.Fatal("Start did not install a child")
	}

	w.Stop()
	if c.cmd.ProcessState == nil {
		t.Fatal("Stop returned before the process was reaped")
	}
	select {
	case <-c.readerDone:
	default:
		t.Fatal("Stop returned before the PTY output reader stopped")
	}
	select {
	case <-c.done:
	default:
		t.Fatal("Stop returned before the child lifecycle completed")
	}
	if state := c.state.Load(); state != childStopped {
		t.Fatalf("stopped child state = %d, want %d", state, childStopped)
	}
	w.Stop()
}

func TestNaturalExitDrainsFinalPTYOutput(t *testing.T) {
	const sentinel = "GHOSTMUX-FINAL-PTY-SENTINEL"
	w := New(80, 8, nil)
	if err := w.Start([]string{
		"sh", "-c",
		"dd if=/dev/zero bs=262144 count=1 2>/dev/null; printf '\\r\\n" + sentinel + "\\r\\n'",
	}, nil); err != nil {
		w.Close()
		t.Fatalf("Start: %v", err)
	}
	w.mu.Lock()
	c := w.child
	w.mu.Unlock()

	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		w.Close()
		t.Fatal("natural child exit did not finish")
	}
	w.Close()
	if got := w.Text(); !strings.Contains(got, sentinel) {
		t.Fatalf("final sentinel was not rendered after exit and Close: %q", got)
	}
	if state := c.state.Load(); state != childExited {
		t.Fatalf("naturally exited child state = %d, want %d", state, childExited)
	}
}

func TestNaturalExitDrainDeadlineHandlesInheritedSlave(t *testing.T) {
	w := widget(t, 80, 8)
	pidFile := t.TempDir() + "/descendant.pid"
	killDescendant := func() {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	t.Cleanup(killDescendant)

	started := time.Now()
	if err := w.Start([]string{
		"sh", "-c",
		`(trap '' HUP; sleep 30) & printf '%s\n' "$!" >"$1"`,
		"sh", pidFile,
	}, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	w.mu.Lock()
	c := w.child
	w.mu.Unlock()

	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		t.Fatal("inherited slave descriptor prevented bounded reap")
	}
	elapsed := time.Since(started)
	if elapsed < ptyDrainGrace/2 {
		t.Fatalf("reader finished in %v; inherited slave did not exercise drain deadline", elapsed)
	}
	if state := c.state.Load(); state != childExited {
		t.Fatalf("deadline-drained child state = %d, want %d", state, childExited)
	}
	select {
	case <-c.readerDone:
	default:
		t.Fatal("bounded drain returned before joining PTY reader")
	}
	killDescendant()
}

func TestNaturalExitNotificationPrecedesDoneAndReplacement(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	w := New(40, 5, func(m tea.Msg) {
		if _, ok := m.(ExitMsg); !ok {
			return
		}
		once.Do(func() { close(entered) })
		<-release
	})
	released := false
	defer func() {
		if !released {
			close(release)
		}
		w.Close()
	}()

	if err := w.Start([]string{"sh", "-c", "exit 0"}, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	w.mu.Lock()
	first := w.child
	w.mu.Unlock()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("natural ExitMsg was not delivered")
	}
	select {
	case <-first.done:
		t.Fatal("child done closed before ExitMsg delivery returned")
	default:
	}

	replaced := make(chan error, 1)
	go func() {
		replaced <- w.Start([]string{"sleep", "30"}, nil)
	}()
	select {
	case err := <-replaced:
		t.Fatalf("replacement finished before ExitMsg delivery: %v", err)
	case <-time.After(3 * coalesceWindow):
	}

	close(release)
	released = true
	select {
	case err := <-replaced:
		if err != nil {
			t.Fatalf("replacement Start: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replacement did not finish after ExitMsg delivery")
	}
	select {
	case <-first.done:
	default:
		t.Fatal("replacement finished before prior child done closed")
	}
}

func TestStopReplacementRaceDoesNotNotifyStoppedGeneration(t *testing.T) {
	var exits atomic.Int64
	w := New(40, 5, func(m tea.Msg) {
		if _, ok := m.(ExitMsg); ok {
			exits.Add(1)
		}
	})
	defer w.Close()

	stopped := 0
	for i := range 12 {
		if err := w.Start([]string{"sh", "-c", "stty -echo; IFS= read -r _"}, nil); err != nil {
			t.Fatalf("iteration %d old Start: %v", i, err)
		}
		w.mu.Lock()
		old := w.child
		w.mu.Unlock()
		before := exits.Load()

		barrier := make(chan struct{})
		writeDone := make(chan struct{})
		replaced := make(chan error, 1)
		go func() {
			<-barrier
			_, _ = old.ptmx.Write([]byte("exit\n"))
			close(writeDone)
		}()
		go func(delay bool) {
			<-barrier
			if delay {
				time.Sleep(time.Millisecond)
			}
			replaced <- w.Start([]string{"sleep", "30"}, nil)
		}(i%2 == 1)
		close(barrier)

		if err := <-replaced; err != nil {
			t.Fatalf("iteration %d replacement Start: %v", i, err)
		}
		<-writeDone
		after := exits.Load()
		switch state := old.state.Load(); state {
		case childStopped:
			stopped++
			if after != before {
				t.Fatalf("iteration %d stopped generation emitted stale ExitMsg: before=%d after=%d", i, before, after)
			}
		case childExited:
			if after != before+1 {
				t.Fatalf("iteration %d natural generation notifications = %d, want 1", i, after-before)
			}
		default:
			t.Fatalf("iteration %d old child ended in lifecycle state %d", i, state)
		}
		w.Stop()
	}
	if stopped == 0 {
		t.Fatal("stress race did not exercise a stopped generation")
	}
}

func TestStartRunsChildAndPostsExit(t *testing.T) {
	var mu sync.Mutex
	var exits []ExitMsg
	w := New(40, 5, func(m tea.Msg) {
		if e, ok := m.(ExitMsg); ok {
			mu.Lock()
			exits = append(exits, e)
			mu.Unlock()
		}
	})
	defer w.Close()
	if err := w.Start([]string{"sh", "-c", "printf 'pty says hi'"}, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(w.Text(), "pty says hi") && !w.Running() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(w.Text(), "pty says hi") {
		t.Fatalf("child output never reached the grid: %q", w.Text())
	}
	if w.Running() {
		t.Fatalf("Running() still true after child exit")
	}
	mu.Lock()
	n := len(exits)
	mu.Unlock()
	if n != 1 {
		t.Errorf("got %d ExitMsg, want 1", n)
	}
}

func TestStopPostsNoExitMsg(t *testing.T) {
	var mu sync.Mutex
	var exits int
	w := New(40, 5, func(m tea.Msg) {
		if _, ok := m.(ExitMsg); ok {
			mu.Lock()
			exits++
			mu.Unlock()
		}
	})
	defer w.Close()
	if err := w.Start([]string{"sleep", "30"}, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !w.Running() {
		t.Fatalf("child not running after Start")
	}
	w.Stop()
	if w.Running() {
		t.Fatalf("Running() true after Stop")
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	n := exits
	mu.Unlock()
	if n != 0 {
		t.Errorf("Stop posted %d ExitMsg, want 0 (caller-initiated death)", n)
	}
}

func TestChildEnvStripsTmuxSetsTerm(t *testing.T) {
	t.Setenv("TMUX", "/tmp/fake,1,0")
	t.Setenv("TMUX_PANE", "%9")
	env := childEnv()
	for _, kv := range env {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") {
			t.Errorf("child env leaks %q", kv)
		}
	}
	found := false
	for _, kv := range env {
		if kv == "TERM=xterm-256color" {
			found = true
		}
	}
	if !found {
		t.Errorf("TERM=xterm-256color missing from child env")
	}
}
