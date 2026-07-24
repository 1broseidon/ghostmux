package term

import (
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// widget builds a notify-less widget for pure transcript tests.
func widget(cols, rows int) *Widget {
	return New(cols, rows, nil)
}

// gridLine returns line y of the plain-text frame, right-trimmed.
func gridLine(w *Widget, y int) string {
	lines := strings.Split(w.Text(), "\n")
	if y >= len(lines) {
		return ""
	}
	return strings.TrimRight(lines[y], " ")
}

func TestTextAndCRLF(t *testing.T) {
	w := widget(20, 4)
	w.Write([]byte("hello\r\nworld"))
	if got := gridLine(w, 0); got != "hello" {
		t.Errorf("line 0 = %q, want %q", got, "hello")
	}
	if got := gridLine(w, 1); got != "world" {
		t.Errorf("line 1 = %q, want %q", got, "world")
	}
}

func TestSGRTruecolorSurvivesRender(t *testing.T) {
	w := widget(20, 2)
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
	w := widget(20, 3)
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
	w := widget(20, 5)
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
	w := widget(20, 2)
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
	w := widget(40, 10)
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
	w := widget(10, 3)
	if got := strings.Count(w.View(), "\n"); got != 2 {
		t.Errorf("View has %d newlines, want 2 (rows-1)", got)
	}
}

func TestCursorOverlayOnlyWhenFocusedAndRunning(t *testing.T) {
	w := widget(10, 2)
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
