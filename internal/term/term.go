// Package term is an embedded terminal widget: a vt emulator fed by one
// child process on a pty. It is the solo frame's viewport surface — but it
// knows nothing about ghostmux: no rail, no tmux package, no wiring imports.
// The evidence law applies to rendering: the view is exactly what the child
// wrote. When the child dies the last real frame stays until the caller
// replaces it — no synthesized content, ever.
package term

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// OutputMsg signals that the child wrote output and the view should redraw.
// Posted coalesced: at most one per coalesceWindow, however fast the child
// streams — a busy agent redraw must not melt the host program.
type OutputMsg struct{}

// ExitMsg signals that the child process exited (or the pty hit EOF). Err is
// cmd.Wait's verdict. A child stopped by Stop posts no ExitMsg — the caller
// asked for that death and is mid-repoint.
type ExitMsg struct{ Err error }

// coalesceWindow bounds OutputMsg frequency.
const coalesceWindow = 30 * time.Millisecond

// stopGrace is how long Stop waits after SIGHUP before SIGKILL.
const stopGrace = 500 * time.Millisecond

// ptyDrainGrace bounds the wait for EOF after a natural exit. Normally the
// reader reaches EOF immediately after draining queued output; the deadline
// handles descendants that inherited a slave descriptor and keep it open.
const ptyDrainGrace = 100 * time.Millisecond

// ptyReadPollInterval bounds how quickly an otherwise blocked reader observes
// a Stop or drain-deadline request. Closing a file descriptor from another
// goroutine does not reliably interrupt an in-flight PTY read on Unix.
const ptyReadPollInterval = 10 * time.Millisecond

const (
	childRunning uint32 = iota
	childStopping
	childExited // a natural exit won the running -> exited transition
	childStopped
)

// child is one pty process. done closes only after Wait has reaped the
// process, the PTY output reader has stopped, and any natural ExitMsg has been
// delivered. state decides whether a racing exit or Stop owns the lifecycle.
type child struct {
	cmd        *exec.Cmd
	ptmx       *os.File
	done       chan struct{}
	readerStop chan struct{}
	readerDone chan struct{}
	state      atomic.Uint32
}

// Widget is an embedded terminal: pointer semantics only — the pty goroutines
// share its state, so value copies would be wrong. It is not a tea.Model; the
// host program owns focus, layout, and message routing, and calls in.
type Widget struct {
	emu    *vt.SafeEmulator
	notify func(tea.Msg) // posts OutputMsg/ExitMsg into the host program

	lifecycleMu sync.Mutex // serializes Start, Stop, and Close
	closed      bool
	inputCloser io.Closer
	inputDone   chan struct{}

	mu    sync.Mutex
	child *child
	sink  io.Writer // test seam: receives forwarded input bytes instead of a pty
	cols  int
	rows  int

	focused       bool
	cursorVisible bool // tracked via the emulator's CursorVisibility callback

	notifyPending bool // one OutputMsg per coalesceWindow
}

// New builds a Widget with an emulator of the given size. notify is how the
// widget posts messages into the host bubbletea program (typically p.Send;
// tests pass a collector).
func New(cols, rows int, notify func(tea.Msg)) *Widget {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	emu := vt.NewSafeEmulator(cols, rows)
	inputCloser, ok := emu.InputPipe().(io.Closer)
	if !ok {
		panic("term: emulator input pipe is not closable")
	}
	w := &Widget{
		emu:           emu,
		notify:        notify,
		inputCloser:   inputCloser,
		inputDone:     make(chan struct{}),
		cols:          cols,
		rows:          rows,
		cursorVisible: true,
	}
	w.emu.SetCallbacks(vt.Callbacks{
		CursorVisibility: func(visible bool) {
			w.mu.Lock()
			w.cursorVisible = visible
			w.mu.Unlock()
		},
	})
	// The persistent forwarder exclusively owns emulator Read. Closing the
	// retained input pipe interrupts a blocked Read so Close can join it before
	// closing the emulator.
	go w.forwardInput()
	return w
}

// forwardInput pumps emulator-encoded input to whichever PTY is current.
// Besides keys, this carries query replies (DSR, DA1, in-band resize).
func (w *Widget) forwardInput() {
	defer close(w.inputDone)
	buf := make([]byte, 4096)
	for {
		n, err := w.emu.Read(buf)
		if n > 0 {
			if dst := w.inputDest(); dst != nil {
				_, _ = dst.Write(buf[:n])
			}
		}
		if err != nil {
			return
		}
	}
}

// Start launches argv on a fresh pty sized to the widget, stopping any
// previous child first. env nil means: inherit this process's environment
// with TMUX/TMUX_PANE stripped (the child must not think it is nested) and
// TERM=xterm-256color (what the emulator speaks).
func (w *Widget) Start(argv []string, env []string) error {
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
	if w.closed {
		return io.ErrClosedPipe
	}
	w.stopLocked()
	if env == nil {
		env = childEnv()
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	w.mu.Lock()
	cols, rows := w.cols, w.rows
	w.mu.Unlock()
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return err
	}
	c := &child{
		cmd:        cmd,
		ptmx:       ptmx,
		done:       make(chan struct{}),
		readerStop: make(chan struct{}),
		readerDone: make(chan struct{}),
	}
	w.mu.Lock()
	w.child = c
	w.mu.Unlock()

	go w.readOutput(c)
	go w.reap(c)
	return nil
}

// readOutput copies one child's PTY output into the emulator. Polling keeps
// the read single-owner and lets a cancellation request reliably interrupt an
// idle PTY; closing the descriptor concurrently with a blocking Read does not.
func (w *Widget) readOutput(c *child) {
	defer func() {
		_ = c.ptmx.Close()
		close(c.readerDone)
	}()
	buf := make([]byte, 32*1024)
	for {
		select {
		case <-c.readerStop:
			return
		default:
		}
		ready, err := pollPTYReadable(c.ptmx, ptyReadPollInterval)
		if err != nil {
			return
		}
		if !ready {
			continue
		}
		select {
		case <-c.readerStop:
			return
		default:
		}
		n, readErr := c.ptmx.Read(buf)
		if n > 0 {
			_, _ = w.emu.Write(buf[:n])
			w.noteOutput()
		}
		if readErr != nil {
			return
		}
	}
}

// reap owns cmd.Wait and coordinates the reader's final PTY close. A natural
// exit drains queued output to EOF, subject only to a bounded wait for
// inherited slave descriptors. Stop requests a prompt close and wins the state
// transition, so its reaper only has to join the reader. A legitimate ExitMsg
// is delivered before done closes, keeping replacement linearized after the
// notification.
func (w *Widget) reap(c *child) {
	werr := c.cmd.Wait()
	natural := c.state.CompareAndSwap(childRunning, childExited)
	if natural {
		w.drainOutput(c)
	} else {
		<-c.readerDone
		c.state.Store(childStopped)
	}

	if natural && w.notify != nil {
		w.notify(ExitMsg{Err: werr})
	}
	close(c.done)
}

// drainOutput lets readOutput consume all bytes through EOF after a natural
// exit. Descendants can keep the slave open indefinitely, so once the bounded
// window expires it asks readOutput to close the master, then joins it.
func (w *Widget) drainOutput(c *child) {
	timer := time.NewTimer(ptyDrainGrace)
	defer timer.Stop()
	select {
	case <-c.readerDone:
	case <-timer.C:
		close(c.readerStop)
		<-c.readerDone
	}
}

// inputDest is where emulator-encoded input bytes go: the test sink when
// set, else the current child's pty.
func (w *Widget) inputDest() io.Writer {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.sink != nil {
		return w.sink
	}
	if w.child != nil {
		return w.child.ptmx
	}
	return nil
}

// childEnv is the default child environment: TMUX/TMUX_PANE stripped,
// TERM set to what the emulator implements.
func childEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "TMUX=") || strings.HasPrefix(kv, "TMUX_PANE=") ||
			strings.HasPrefix(kv, "TERM=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "TERM=xterm-256color")
}

// Stop terminates the current child, if any: SIGHUP, a short grace, then
// SIGKILL; blocks until reaped so the pty is fully torn down before the next
// Start. The emulator keeps its last frame — the caller decides what replaces
// it.
func (w *Widget) Stop() {
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
	w.stopLocked()
}

// stopLocked implements Stop while lifecycleMu is held.
func (w *Widget) stopLocked() {
	w.mu.Lock()
	c := w.child
	if c == nil {
		w.mu.Unlock()
		return
	}
	stopping := c.state.CompareAndSwap(childRunning, childStopping)
	if stopping {
		close(c.readerStop)
	}
	w.child = nil
	w.mu.Unlock()

	if stopping {
		// Mark stopping before detaching or signaling. readOutput observes the
		// request promptly, closes the master itself, and reap owns its join.
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Signal(syscall.SIGHUP)
		}
	}

	timer := time.NewTimer(stopGrace)
	defer timer.Stop()
	select {
	case <-c.done:
		return
	case <-timer.C:
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		<-c.done
	}
}

// Close joins the child and both widget-owned emulator goroutines before
// closing the emulator. The widget is dead after this.
func (w *Widget) Close() {
	w.lifecycleMu.Lock()
	defer w.lifecycleMu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	w.stopLocked()
	_ = w.inputCloser.Close()
	<-w.inputDone
	_ = w.emu.Close()
}

// Running reports whether a child is alive on the pty.
func (w *Widget) Running() bool {
	w.mu.Lock()
	c := w.child
	w.mu.Unlock()
	if c == nil {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

// Size returns the widget's current grid, for callers (like a group wall)
// that must size a new tmux session to match before ever starting a child.
func (w *Widget) Size() (cols, rows int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cols, w.rows
}

// Resize resizes the emulator grid and the pty (the child sees SIGWINCH).
func (w *Widget) Resize(cols, rows int) {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	w.mu.Lock()
	w.cols, w.rows = cols, rows
	c := w.child
	w.mu.Unlock()
	w.emu.Resize(cols, rows)
	if c != nil {
		pty.Setsize(c.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	}
}

// Focus marks the widget focused: the cursor overlay renders and the
// emulator reports focus to the child (focus-events mode).
func (w *Widget) Focus() {
	w.mu.Lock()
	w.focused = true
	w.mu.Unlock()
	w.emu.Focus()
}

// Blur is Focus's inverse.
func (w *Widget) Blur() {
	w.mu.Lock()
	w.focused = false
	w.mu.Unlock()
	w.emu.Blur()
}

// Focused reports whether the widget is focused.
func (w *Widget) Focused() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.focused
}

// View renders the emulator's current frame as a styled string (lines joined
// by \n), embeddable directly in a bubbletea View. Render() carries no
// cursor, so when the widget is focused, the child runs, and the child hasn't
// hidden it, the cursor cell is overlaid in reverse video — a presentation of
// the emulator's real cursor state, not invented content.
func (w *Widget) View() string {
	frame := w.emu.Render()
	w.mu.Lock()
	focused, visible := w.focused, w.cursorVisible
	w.mu.Unlock()
	if !focused || !visible || !w.Running() {
		return frame
	}
	pos := w.emu.CursorPosition()
	return overlayCursor(frame, pos.X, pos.Y)
}

// Text is the frame as plain text, styling stripped — the test seam's
// assertion surface.
func (w *Widget) Text() string {
	return ansi.Strip(w.emu.Render())
}

// IsAltScreen reports whether the child is on the alternate screen.
func (w *Widget) IsAltScreen() bool { return w.emu.IsAltScreen() }

// SendText writes literal text as input (the emulator encodes it onto the
// pty via the forwarder).
func (w *Widget) SendText(s string) { w.emu.SendText(s) }

// SendPaste pastes text, bracketed if the child enabled bracketed paste.
func (w *Widget) SendPaste(s string) { w.emu.Paste(s) }

// Write feeds bytes straight into the emulator — the headless test seam:
// canned transcripts in, rendered grid out. Not used on the live pty path.
func (w *Widget) Write(b []byte) (int, error) {
	return w.emu.Write(b)
}

// noteOutput posts at most one OutputMsg per coalesceWindow, and always one
// within a window of the last write — the trailing frame is never lost.
func (w *Widget) noteOutput() {
	if w.notify == nil {
		return
	}
	w.mu.Lock()
	if w.notifyPending {
		w.mu.Unlock()
		return
	}
	w.notifyPending = true
	w.mu.Unlock()
	time.AfterFunc(coalesceWindow, func() {
		w.mu.Lock()
		w.notifyPending = false
		w.mu.Unlock()
		w.notify(OutputMsg{})
	})
}

// overlayCursor reverses the video of the cell at (x, y) in a rendered frame.
func overlayCursor(frame string, x, y int) string {
	lines := strings.Split(frame, "\n")
	if y < 0 || y >= len(lines) {
		return frame
	}
	line := lines[y]
	width := ansi.StringWidth(line)
	if x < 0 {
		return frame
	}
	left := ansi.Cut(line, 0, x)
	cell := " "
	right := ""
	if x < width {
		cell = ansi.Cut(line, x, x+1)
		right = ansi.Cut(line, x+1, width)
	} else if x > width {
		left = line + strings.Repeat(" ", x-width)
	}
	// \x1b[7m reverse … \x1b[27m un-reverse, preserving the cell's own colors.
	lines[y] = left + "\x1b[7m" + cell + "\x1b[27m" + right
	return strings.Join(lines, "\n")
}
