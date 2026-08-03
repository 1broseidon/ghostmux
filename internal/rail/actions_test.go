package rail

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

// TestKeyHelpCoversBoundKeys is the Task 10 single-source-of-truth check:
// every key Update() dispatches on in normal mode must be documented in
// keyHelpTable (rendered by both the `?` popup and `rail help`).
func TestKeyHelpCoversBoundKeys(t *testing.T) {
	// boundKeys mirrors updateNormalKey's switch cases exactly; keep the two
	// lists in sync when the keymap changes.
	// `?` and `,` are not here: the frame intercepts them, so updateNormalKey
	// no longer dispatches on either. The table still documents them.
	boundKeys := []string{
		"q", "j", "k", "down", "up", "enter", "a", "m", "u", "J", "K", "n", "x", "S", "/", "d",
		"h", "l", "right", "`", "]", "v",
	}

	var haystack strings.Builder
	for _, k := range keyHelpRows() {
		haystack.WriteString(strings.ToLower(k.key))
		haystack.WriteByte(' ')
	}
	hay := haystack.String()

	alias := map[string]string{
		"down":  "↓",
		"up":    "↑",
		"enter": "↵",
		"right": "→",
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
	if len(keyHelpRows()) < 12 || len(keyHelpRows()) > 22 {
		t.Errorf("keyHelpTable has %d entries, want a short operator set (12–22)", len(keyHelpRows()))
	}
}

func TestPlainFilteredDimsNonMatchingInPlace(t *testing.T) {
	rows := []railRow{
		{depth: 0, sess: "gm-agent-00", label: "gm-agent-00"},
		{depth: 1, isWin: true, sess: "gm-agent-00", label: "1:claude", active: true},
		{depth: 0, sess: "alpha", label: "alpha"},
		{depth: 1, isWin: true, sess: "alpha", label: "1:zsh", active: true},
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
		"list-sessions": "alpha\t0\t\t\ngm-agent-00\t0\t\t\n",
		"list-windows": "alpha\t1\tzsh\t1\t0\t0\t0\n" +
			"gm-agent-00\t1\tclaude\t1\t0\t0\t0\n",
	})
	rows := railRows("", ViewState{})
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

	m := &railModel{vp: &fakeViewport{}}
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
	if m.vp.Lock().Sess != "myproj" {
		t.Errorf("viewport not pointed at new session: lockSess=%q", m.vp.Lock().Sess)
	}
}

// TestCreateSessionUsesCurrentPaneCwd: when CreateDir is current and the
// viewport lock has a proven pane path, n starts there instead of home.
func TestCreateSessionUsesCurrentPaneCwd(t *testing.T) {
	cwd := t.TempDir()
	origMode := CreateDir()
	t.Cleanup(func() { SetCreateDir(PersistCreateDir(origMode)) })
	SetCreateDir(CreateDirCurrent)

	var gotDir string
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) {
		if len(args) >= 6 && args[0] == "new-session" {
			gotDir = args[5]
		}
		return "", nil
	}
	t.Cleanup(func() { tmux.Runner = orig })

	vp := &fakeViewport{}
	vp.Point("alpha", "", false)
	m := &railModel{
		vp: vp,
		tmuxCache: tmuxCache{
			hasSnapshot: true,
			snapshot: tmux.Snapshot{
				Sessions: []tmux.Session{{Name: "alpha", CurrentPath: cwd}},
			},
		},
	}
	if err := m.createSession("myproj"); err != nil {
		t.Fatalf("createSession: %v", err)
	}
	if gotDir != cwd {
		t.Fatalf("new-session -c = %q, want viewport cwd %q", gotDir, cwd)
	}

	// An idle viewport (no lock) falls back to home.
	home, _ := os.UserHomeDir()
	vp.lock = ViewState{}
	if err := m.createSession("other"); err != nil {
		t.Fatalf("createSession: %v", err)
	}
	if gotDir != home && gotDir != "~" {
		t.Fatalf("empty lock did not fall back to home: %q", gotDir)
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

// TestAmbientAgentDetection: agent-ness is observed from the foreground
// command, never declared by a name or a separate session type.
func TestAmbientAgentDetection(t *testing.T) {
	for cmd, want := range map[string]bool{
		"claude": true, "codex": true, "aider": true,
		"npm": false, "vite": false, "zsh": false, "go": false,
	} {
		if got := isAgentCmd(cmd); got != want {
			t.Errorf("isAgentCmd(%q) = %v, want %v", cmd, got, want)
		}
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

	m := &railModel{vp: &fakeViewport{}}
	if err := m.killSession("alpha"); err != nil {
		t.Fatalf("killSession: %v", err)
	}
	if m.vp.Lock().Sess != "" {
		t.Errorf("viewport should have gone idle, lockSess=%q", m.vp.Lock().Sess)
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

	m := &railModel{vp: &fakeViewport{lock: ViewState{Sess: "other"}}}
	if err := m.killSession("alpha"); err != nil {
		t.Fatalf("killSession: %v", err)
	}
	if m.vp.Lock().Sess != "other" {
		t.Errorf("unrelated viewport lock changed: %q", m.vp.Lock().Sess)
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

// TestInHostExcludesOwnSession: a standalone frame relaunched inside a tmux
// session must not list its own host — selecting that row would render the
// frame inside itself. This is what makes `tmux new -A -s gm ghostmux solo`
// (resume for free) safe.
func TestInHostExcludesOwnSession(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // New() reads state: never the real file
	withFakeRunner(t, map[string]string{
		"list-sessions": "alpha\t0\t\t\ngm\t1\t\t\n",
		"list-windows": "alpha\t1\tzsh\t1\t0\t0\t0\n" +
			"gm\t1\tghostmux\t1\t0\t0\t0\n",
	})
	m := New(&fakeViewport{}).InHost("gm")
	for _, r := range m.rows {
		if r.sess == "gm" {
			t.Errorf("rail listed its own host session: %+v", r)
		}
	}
	if len(m.rows) == 0 {
		t.Errorf("host exclusion removed every row")
	}
}

// TestNKeyCreatesOnTmux pins the default: n is tmux, always.
func TestNKeyCreatesOnTmux(t *testing.T) {
	m := railModel{vp: &fakeViewport{}, collapsed: map[string]bool{}}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	got := next.(railModel)
	if got.mode != modeCreate {
		t.Fatalf("n did not open the create prompt")
	}
}
