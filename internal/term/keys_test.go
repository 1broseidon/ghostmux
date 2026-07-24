package term

import (
	"bytes"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// syncBuffer is a locked byte buffer the input forwarder can write into.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// sinkWidget builds a widget whose forwarded input lands in a test buffer.
func sinkWidget(t *testing.T) (*Widget, *syncBuffer) {
	t.Helper()
	w := New(40, 10, nil)
	sink := &syncBuffer{}
	w.mu.Lock()
	w.sink = sink
	w.mu.Unlock()
	t.Cleanup(w.Close)
	return w, sink
}

// wantBytes polls until the sink holds exactly want (the forwarder is a
// goroutine; input arrives asynchronously).
func wantBytes(t *testing.T, sink *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sink.String() == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Errorf("forwarded bytes = %q, want %q", sink.String(), want)
}

func key(t tea.KeyType, runes ...rune) tea.KeyMsg {
	return tea.KeyMsg{Type: t, Runes: runes}
}

func TestSendKeyPlainRune(t *testing.T) {
	w, sink := sinkWidget(t)
	w.SendKey(key(tea.KeyRunes, 'a'))
	wantBytes(t, sink, "a")
}

func TestSendKeyCtrlChords(t *testing.T) {
	w, sink := sinkWidget(t)
	w.SendKey(key(tea.KeyCtrlC))
	wantBytes(t, sink, "\x03")
	sink.Reset()
	w.SendKey(key(tea.KeyCtrlB)) // the inner tmux prefix — must pass through
	wantBytes(t, sink, "\x02")
	sink.Reset()
	w.SendKey(key(tea.KeyCtrlBackslash))
	wantBytes(t, sink, "\x1c")
}

func TestSendKeyEnterEscBackspace(t *testing.T) {
	w, sink := sinkWidget(t)
	w.SendKey(key(tea.KeyEnter))
	wantBytes(t, sink, "\r")
	sink.Reset()
	w.SendKey(key(tea.KeyEsc))
	wantBytes(t, sink, "\x1b")
	sink.Reset()
	w.SendKey(key(tea.KeyBackspace))
	wantBytes(t, sink, "\x7f")
}

func TestSendKeyArrowsBothDECCKMModes(t *testing.T) {
	w, sink := sinkWidget(t)
	// Normal mode: CSI arrows.
	w.SendKey(key(tea.KeyUp))
	wantBytes(t, sink, "\x1b[A")
	sink.Reset()
	// Application cursor keys (DECCKM set by the child): SS3 arrows.
	w.Write([]byte("\x1b[?1h"))
	w.SendKey(key(tea.KeyUp))
	wantBytes(t, sink, "\x1bOA")
	sink.Reset()
	w.SendKey(key(tea.KeyLeft))
	wantBytes(t, sink, "\x1bOD")
	sink.Reset()
	// Reset DECCKM: back to CSI.
	w.Write([]byte("\x1b[?1l"))
	w.SendKey(key(tea.KeyDown))
	wantBytes(t, sink, "\x1b[B")
}

func TestSendKeyAltPrefix(t *testing.T) {
	w, sink := sinkWidget(t)
	w.SendKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}, Alt: true})
	wantBytes(t, sink, "\x1bf")
}

func TestSendKeyModifiedNavFallback(t *testing.T) {
	w, sink := sinkWidget(t)
	w.SendKey(key(tea.KeyCtrlLeft))
	wantBytes(t, sink, "\x1b[1;5D")
	sink.Reset()
	w.SendKey(key(tea.KeyShiftTab))
	wantBytes(t, sink, "\x1b[Z")
}

func TestSendKeyMultiRuneBurst(t *testing.T) {
	w, sink := sinkWidget(t)
	w.SendKey(key(tea.KeyRunes, 'h', 'i', '!'))
	wantBytes(t, sink, "hi!")
}

func TestSendPasteBracketedModeAware(t *testing.T) {
	w, sink := sinkWidget(t)
	// Child hasn't enabled bracketed paste: literal.
	w.SendKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("plain"), Paste: true})
	wantBytes(t, sink, "plain")
	sink.Reset()
	// Child enables bracketed paste: wrapped.
	w.Write([]byte("\x1b[?2004h"))
	w.SendKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("wrapped"), Paste: true})
	wantBytes(t, sink, "\x1b[200~wrapped\x1b[201~")
}

func TestSendMouseModeAware(t *testing.T) {
	w, sink := sinkWidget(t)
	click := tea.MouseMsg{X: 36, Y: 4, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
	// Child never enabled mouse reporting: encodes to nothing.
	w.SendMouse(click, 31, 0)
	time.Sleep(50 * time.Millisecond)
	if got := sink.String(); got != "" {
		t.Fatalf("mouse bytes sent with no mouse mode: %q", got)
	}
	// Child enables button events + SGR encoding: click lands, translated.
	w.Write([]byte("\x1b[?1002h\x1b[?1006h"))
	w.SendMouse(click, 31, 0)
	// SGR is 1-based: (36-31, 4) → "6;5".
	wantBytes(t, sink, "\x1b[<0;6;5M")
}

func TestSendMouseNegativeCoordsDropped(t *testing.T) {
	w, sink := sinkWidget(t)
	w.Write([]byte("\x1b[?1002h\x1b[?1006h"))
	w.SendMouse(tea.MouseMsg{X: 5, Y: 2, Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft}, 31, 0) // x < offset → left of the widget
	time.Sleep(50 * time.Millisecond)
	if got := sink.String(); got != "" {
		t.Fatalf("out-of-bounds mouse event forwarded: %q", got)
	}
}
