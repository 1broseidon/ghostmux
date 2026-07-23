package rail

import (
	"fmt"
	"strings"
	"testing"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

// TestKeyHelpCoversBoundKeys is the Task 10 single-source-of-truth check:
// every key Update() dispatches on in normal mode must be documented in
// keyHelpTable (rendered by both the `?` popup and `rail help`).
func TestKeyHelpCoversBoundKeys(t *testing.T) {
	// boundKeys mirrors updateNormalKey's switch cases exactly; keep the two
	// lists in sync when the keymap changes.
	boundKeys := []string{"q", "j", "k", "down", "up", "g", "G", "r", "enter", "tab", "n", "a", "x", "/", "d", "?"}

	var haystack strings.Builder
	for _, k := range keyHelpTable {
		haystack.WriteString(strings.ToLower(k.key))
		haystack.WriteByte(' ')
	}
	hay := haystack.String()

	alias := map[string]string{
		"down":  "↓",
		"up":    "↑",
		"enter": "↵",
	}

	for _, key := range boundKeys {
		needle := key
		if a, ok := alias[key]; ok {
			needle = a
		}
		if !strings.Contains(hay, strings.ToLower(needle)) {
			t.Errorf("bound key %q (needle %q) not documented in keyHelpTable", key, needle)
		}
	}
	if len(keyHelpTable) < 12 {
		t.Errorf("keyHelpTable has %d entries, want >= 12 (screen-6 keymap)", len(keyHelpTable))
	}
}

func TestPlainFilteredDimsNonMatchingInPlace(t *testing.T) {
	rows := []railRow{
		{depth: 0, sess: "gm-agent-00", label: "gm-agent-00"},
		{depth: 1, sess: "gm-agent-00", label: "1:claude", active: true},
		{depth: 0, sess: "alpha", label: "alpha"},
		{depth: 1, sess: "alpha", label: "1:zsh", active: true},
	}
	var plainOut, filteredOut []string
	for _, r := range rows {
		plainOut = append(plainOut, r.plain())
		filteredOut = append(filteredOut, r.plainFiltered("agent"))
	}
	// order/positions identical; dimmed rows differ only by the leading marker.
	if len(plainOut) != len(filteredOut) {
		t.Fatalf("row count mismatch")
	}
	wantDim := []bool{false, false, true, true}
	for i, line := range filteredOut {
		if wantDim[i] {
			if !strings.HasPrefix(line, "·") {
				t.Errorf("row %d (%q) expected dim prefix, got %q", i, plainOut[i], line)
			}
			if line[len("·"):] != plainOut[i] {
				t.Errorf("row %d dimmed body changed: got %q, want %q", i, line[len("·"):], plainOut[i])
			}
		} else {
			if strings.HasPrefix(line, "·") {
				t.Errorf("row %d (%q) unexpectedly dimmed", i, plainOut[i])
			}
			if line[len(" "):] != plainOut[i] {
				t.Errorf("row %d body changed: got %q, want %q", i, line[len(" "):], plainOut[i])
			}
		}
	}
}

func TestCmdOnceFilterMatchesSpecExample(t *testing.T) {
	withFakeRunner(t, map[string]string{
		"list-sessions": "alpha\t0\ngm-agent-00\t0\n",
		"list-windows": "alpha\t1\tzsh\t1\t0\t0\t0\n" +
			"gm-agent-00\t1\tclaude\t1\t0\t0\t0\n",
	})
	rows := railRows("", viewState{})
	var dimmed []string
	for _, r := range rows {
		if strings.HasPrefix(r.plainFiltered("agent"), "·") {
			dimmed = append(dimmed, r.sess)
		}
	}
	for _, sess := range dimmed {
		if sess != "alpha" {
			t.Errorf("unexpected dimmed session %q", sess)
		}
	}
	if len(dimmed) != 1 { // alpha is single-window → one flat row
		t.Errorf("expected exactly alpha's flat row dimmed, got %v", dimmed)
	}
}

func TestCreateSessionRequiresName(t *testing.T) {
	m := &railModel{}
	if err := m.createSession("   "); err == nil {
		t.Errorf("expected error for blank name")
	}
	if err := m.createSession(""); err == nil {
		t.Errorf("expected error for empty name")
	}
}

func TestCreateSessionRunsTmuxAndPointsViewport(t *testing.T) {
	var calls []string
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "", nil
	}
	t.Cleanup(func() { tmux.Runner = orig })

	m := &railModel{vp: viewport{pane: "%1", idleCmd: "ghostmux rail idle"}}
	if err := m.createSession("myproj"); err != nil {
		t.Fatalf("createSession: %v", err)
	}
	found := false
	for _, c := range calls {
		if strings.HasPrefix(c, "new-session -d -s myproj") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a `new-session -d -s myproj ...` call, got %v", calls)
	}
	if m.vp.lockSess != "myproj" {
		t.Errorf("viewport not pointed at new session: lockSess=%q", m.vp.lockSess)
	}
}

func TestCreateSessionPropagatesTmuxError(t *testing.T) {
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) {
		if args[0] == "new-session" {
			return "", fmt.Errorf("duplicate session: myproj")
		}
		return "", nil
	}
	t.Cleanup(func() { tmux.Runner = orig })

	m := &railModel{}
	if err := m.createSession("myproj"); err == nil {
		t.Errorf("expected tmux error to propagate")
	}
}

func TestAgentSessionPicksLowestFreeNN(t *testing.T) {
	orig := tmux.Runner
	var newSessionArgs []string
	tmux.Runner = func(args ...string) (string, error) {
		if args[0] == "list-sessions" {
			return "gm-agent-00\n", nil
		}
		if args[0] == "new-session" {
			newSessionArgs = args
		}
		return "", nil
	}
	t.Cleanup(func() { tmux.Runner = orig })

	m := &railModel{vp: viewport{pane: "%1", idleCmd: "x"}}
	if err := m.agentSession(); err != nil {
		t.Fatalf("agentSession: %v", err)
	}
	if m.vp.lockSess != "gm-agent-01" {
		t.Errorf("agentSession created %q, want gm-agent-01", m.vp.lockSess)
	}
	joined := strings.Join(newSessionArgs, " ")
	if !strings.Contains(joined, "gm-agent-01") {
		t.Errorf("new-session args = %v, want gm-agent-01", newSessionArgs)
	}
}

func TestKillSessionIdlesViewportIfLocked(t *testing.T) {
	var calls []string
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return "", nil
	}
	t.Cleanup(func() { tmux.Runner = orig })

	m := &railModel{vp: viewport{pane: "%1", idleCmd: "ghostmux rail idle", lockSess: "alpha"}}
	if err := m.killSession("alpha"); err != nil {
		t.Fatalf("killSession: %v", err)
	}
	if m.vp.lockSess != "" {
		t.Errorf("viewport should have gone idle, lockSess=%q", m.vp.lockSess)
	}
	found := false
	for _, c := range calls {
		if c == "kill-session -t =alpha" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected kill-session -t =alpha, got %v", calls)
	}
}

func TestKillSessionLeavesUnrelatedViewportAlone(t *testing.T) {
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) { return "", nil }
	t.Cleanup(func() { tmux.Runner = orig })

	m := &railModel{vp: viewport{pane: "%1", idleCmd: "x", lockSess: "other"}}
	if err := m.killSession("alpha"); err != nil {
		t.Fatalf("killSession: %v", err)
	}
	if m.vp.lockSess != "other" {
		t.Errorf("unrelated viewport lock changed: %q", m.vp.lockSess)
	}
}

func TestMoveCursorSkipsDimmedRows(t *testing.T) {
	m := &railModel{
		rows: []railRow{
			{depth: 0, sess: "agent", label: "agent"},
			{depth: 0, sess: "dotfiles", label: "dotfiles"},
			{depth: 0, sess: "gm-agent-01", label: "gm-agent-01"},
		},
		filterQuery: "agent",
		collapsed:   map[string]bool{},
	}
	m.cursor = 0
	m.moveCursor(1) // should skip dotfiles (row 1), land on gm-agent-01 (row 2)
	if m.cursor != 2 {
		t.Errorf("moveCursor(1) landed on %d, want 2 (skipping dimmed row 1)", m.cursor)
	}
}
