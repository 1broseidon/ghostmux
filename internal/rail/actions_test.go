package rail

import (
	"fmt"
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
	boundKeys := []string{"q", "j", "k", "down", "up", "g", "G", "r", "enter", "tab", "n", "x", "S", "/", "d", "l", "right", "z"}

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
	if len(keyHelpRows()) < 11 {
		t.Errorf("keyHelpTable has %d entries, want >= 11 (screen-6 keymap minus the removed `a`)", len(keyHelpRows()))
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
		"list-sessions": "alpha\t0\ngm-agent-00\t0\n",
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
	if err := m.createSession("   ", ""); err == nil {
		t.Errorf("expected error for blank name")
	}
	if err := m.createSession("", ""); err == nil {
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
	if err := m.createSession("myproj", ""); err != nil {
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
	if err := m.createSession("myproj", ""); err == nil {
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
	if err := m.killSession("alpha", ""); err != nil {
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
	if err := m.killSession("alpha", ""); err != nil {
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

// TestAuxSessionsParseZellij: zellij rows come only from provable list
// output — prose is skipped, marks stay empty, and an EXITED session is kept
// as a ghost because zellij's own label says the name still exists and
// attaching brings it back.
func TestAuxSessionsParseZellij(t *testing.T) {
	origList, origPresent := zellijList, zellijPresent
	zellijPresent = true
	zellijList = func() (string, error) {
		return "alpha [Created 2h ago]\nbeta [Created 1m ago] (EXITED - attach to resurrect)\n" +
			"gamma [Created 5s ago]\nNo active zellij sessions found.\n", nil
	}
	t.Cleanup(func() { zellijList, zellijPresent = origList, origPresent })

	aux := auxSessions()
	if len(aux) != 3 || aux[0].name != "alpha" || aux[1].name != "beta" || aux[2].name != "gamma" {
		t.Fatalf("auxSessions = %+v, want alpha+beta+gamma (prose skipped)", aux)
	}
	if aux[0].exited || !aux[1].exited || aux[2].exited {
		t.Errorf("EXITED not read off the right row: %+v", aux)
	}
	rows := auxRows(aux, ViewState{Backend: "zellij", Sess: "gamma"})
	if !rows[2].inView || rows[0].inView {
		t.Errorf("inView marks wrong: %+v", rows)
	}
	if rows[0].bell || rows[0].act || rows[0].done || rows[0].attached {
		t.Errorf("zellij rows must carry no unproven marks: %+v", rows[0])
	}
	if rows[0].cmd != "zellij" || !rows[0].flat {
		t.Errorf("zellij row rendering fields wrong: %+v", rows[0])
	}
	// The exited row is a ghost: ○ and nothing else, ever.
	ghost := rows[1]
	if !ghost.ghost {
		t.Errorf("EXITED row not rendered as a ghost: %+v", ghost)
	}
	if ghost.bell || ghost.act || ghost.done || ghost.attached || ghost.inView {
		t.Errorf("exited zellij row carries unproven marks: %+v", ghost)
	}
	if ghost.gutter() != "○" {
		t.Errorf("exited zellij gutter = %q, want ○", ghost.gutter())
	}
}

// TestInHostExcludesOwnSession: a standalone frame relaunched inside a
// multiplexer session must not list its own host — selecting that row would
// render the frame inside itself. This is what makes `tmux new -A -s gm
// ghostmux solo` (resume for free) safe.
func TestInHostExcludesOwnSession(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // New() reads state: never the real file
	withFakeRunner(t, map[string]string{
		"list-sessions": "alpha\t0\ngm\t1\n",
		"list-windows": "alpha\t1\tzsh\t1\t0\t0\t0\n" +
			"gm\t1\tghostmux\t1\t0\t0\t0\n",
	})
	m := New(&fakeViewport{}).InHost("", "gm")
	for _, r := range m.rows {
		if r.sess == "gm" {
			t.Errorf("rail listed its own host session: %+v", r)
		}
	}
	if len(m.rows) == 0 {
		t.Errorf("host exclusion removed every row")
	}
}

// TestInHostExcludesOwnAuxSession is the same guarantee on a non-tmux host.
func TestInHostExcludesOwnAuxSession(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // New() reads state: never the real file
	origList, origPresent := zellijList, zellijPresent
	zellijPresent = true
	zellijList = func() (string, error) { return "gm [Created 1m ago]\nother [Created 2m ago]\n", nil }
	t.Cleanup(func() { zellijList, zellijPresent = origList, origPresent })

	m := New(&fakeViewport{}).InHost("zellij", "gm")
	got := m.visibleAux(auxSessions())
	if len(got) != 1 || got[0].name != "other" {
		t.Errorf("visibleAux = %+v, want only [other]", got)
	}
}

// TestCreateRoutesToZellijBackend: the multi-backend promise has to hold for
// making sessions, not just listing them — on a zellij-only box `n` must work.
func TestCreateRoutesToZellijBackend(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir()) // refresh() can persist: never the real file
	var created []string
	origCreate, origPresent := createAux, zellijPresent
	zellijPresent = true
	createAux = func(backend, name string) error {
		created = append(created, backend+":"+name)
		return nil
	}
	t.Cleanup(func() { createAux, zellijPresent = origCreate, origPresent })

	origRunner := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) {
		if args[0] == "new-session" {
			t.Errorf("zellij create fell through to tmux: %v", args)
		}
		return "", nil
	}
	t.Cleanup(func() { tmux.Runner = origRunner })

	m := &railModel{vp: &fakeViewport{}}
	if err := m.createSession("myz", "zellij"); err != nil {
		t.Fatalf("createSession: %v", err)
	}
	if len(created) != 1 || created[0] != "zellij:myz" {
		t.Errorf("createAux calls = %v, want [zellij:myz]", created)
	}
	if lock := m.vp.Lock(); lock.Backend != "zellij" || lock.Sess != "myz" {
		t.Errorf("viewport not pointed at the new zellij session: %+v", lock)
	}
}

// TestBackendKeysMatchWhatIsInstalled: `n` is tmux and `z` is zellij — one
// key per multiplexer, no picker. `z` must not be offered on a box without
// zellij, or the key would only ever produce an error.
func TestBackendKeysMatchWhatIsInstalled(t *testing.T) {
	origTmux, origZellij := tmuxPresent, zellijPresent
	t.Cleanup(func() { tmuxPresent, zellijPresent = origTmux, origZellij })

	tmuxPresent, zellijPresent = func() bool { return true }, false
	if HasBackend("zellij") {
		t.Errorf("zellij advertised while not installed")
	}
	if !HasBackend("tmux") {
		t.Errorf("tmux not advertised while installed")
	}
	tmuxPresent, zellijPresent = func() bool { return false }, true
	if got := backends(); len(got) != 1 || got[0] != "zellij" {
		t.Errorf("zellij-only: backends() = %v, want [zellij]", got)
	}
	if !HasBackend("zellij") {
		t.Errorf("zellij not advertised while installed")
	}
}

// TestZKeyIsInertWithoutZellij: pressing z on a tmux-only box must flash an
// error, never open a prompt that cannot succeed.
func TestZKeyIsInertWithoutZellij(t *testing.T) {
	origZellij := zellijPresent
	zellijPresent = false
	t.Cleanup(func() { zellijPresent = origZellij })

	m := railModel{vp: &fakeViewport{}, collapsed: map[string]bool{}}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	got := next.(railModel)
	if got.mode == modeCreate {
		t.Errorf("z opened a create prompt with zellij absent")
	}
	if !got.errorActive() {
		t.Errorf("z gave no feedback with zellij absent")
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
	if got.createBackend != "" {
		t.Errorf("n targeted %q, want tmux (\"\")", got.createBackend)
	}
}
