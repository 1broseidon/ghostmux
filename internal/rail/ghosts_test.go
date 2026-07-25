package rail

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

// withoutZellij pins the aux backend off. Tests that refresh() would otherwise
// shell out to whatever zellij is really running on the box, and a fleet test
// must assert on its fixture, not on the developer's sessions.
func withoutZellij(t *testing.T) {
	t.Helper()
	orig := zellijPresent
	zellijPresent = false
	t.Cleanup(func() { zellijPresent = orig })
}

// withZellijList fakes the zellij session list (and marks zellij present).
func withZellijList(t *testing.T, out string) {
	t.Helper()
	origList, origPresent := zellijList, zellijPresent
	zellijPresent = true
	zellijList = func() (string, error) { return out, nil }
	t.Cleanup(func() { zellijList, zellijPresent = origList, origPresent })
}

// recordTmux swaps in a Runner that records every call and answers err to
// new-session (nil = success).
func recordTmux(t *testing.T, calls *[]string, newSessionErr error) {
	t.Helper()
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) {
		*calls = append(*calls, strings.Join(args, " "))
		if args[0] == "new-session" {
			return "", newSessionErr
		}
		return "", nil
	}
	t.Cleanup(func() { tmux.Runner = orig })
}

func key(s string) tea.KeyMsg {
	if s == "enter" {
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// ghostAt returns the index of the first ghost row in the visible tree.
func ghostAt(t *testing.T, m *railModel) int {
	t.Helper()
	for i, r := range m.visible() {
		if r.ghost {
			return i
		}
	}
	t.Fatalf("no ghost row in %+v", m.visible())
	return 0
}

// TestApplyGroupsSynthesizesDeclarationGhosts is the heart of Design A: a
// grouped name whose session is gone is not deleted from the rail, because the
// grouping IS the declaration that it belongs there. Both halves are facts —
// declared here, not running now — and the row asserts nothing else.
func TestApplyGroupsSynthesizesDeclarationGhosts(t *testing.T) {
	live := sessionRow("api")
	live.bell = true
	dirs := map[string]string{"tmux:web": "/home/g/Projects/web"}
	out := applyGroups([]railRow{live},
		[]Group{{Name: "work", Members: []string{"tmux:api", "tmux:web"}}}, dirs)

	if len(out) != 3 {
		t.Fatalf("want header + live + ghost, got %d rows: %+v", len(out), out)
	}
	hdr := out[0]
	if hdr.count != 1 || hdr.ghostCount != 1 {
		t.Errorf("header counts = %d live / %d ghost, want 1/1", hdr.count, hdr.ghostCount)
	}
	g := out[2]
	if !g.ghost || g.sess != "web" || g.label != "web" {
		t.Fatalf("declared-but-dead member did not become a ghost: %+v", g)
	}
	if g.depth != 1 || g.group != "work" || !g.flat || g.backend != "" {
		t.Errorf("ghost row shape wrong: %+v", g)
	}
	if g.dir != "/home/g/Projects/web" {
		t.Errorf("ghost dir = %q, want the recorded one", g.dir)
	}
	if g.bell || g.done || g.act || g.inView || g.attached {
		t.Errorf("ghost carries a mark it cannot have earned: %+v", g)
	}
	if g.gutter() != "○" {
		t.Errorf("ghost gutter = %q, want ○", g.gutter())
	}
	// The header inherited the LIVE member's bell, not the ghost's nothing.
	if !hdr.bell {
		t.Errorf("header lost its live member's bell: %+v", hdr)
	}
}

// TestGhostOnlyGroupCountsAndAggregates: a group with nothing running is a
// declaration. It reports its dead as dead and carries no marks at all.
func TestGhostOnlyGroupCountsAndAggregates(t *testing.T) {
	out := applyGroups(nil,
		[]Group{{Name: "agents", Members: []string{"tmux:a", "tmux:b"}}}, nil)
	hdr := out[0]
	if hdr.count != 0 || hdr.ghostCount != 2 {
		t.Errorf("header counts = %d live / %d ghost, want 0/2", hdr.count, hdr.ghostCount)
	}
	if hdr.bell || hdr.done || hdr.act || hdr.inView {
		t.Errorf("ghost-only header aggregated a mark from nothing: %+v", hdr)
	}
	if out[1].dir != "" || out[2].dir != "" {
		t.Errorf("unrecorded dirs must stay empty: %+v", out[1:])
	}
}

// TestGhostGutterAdmitsNothingElse: ○ is the only glyph a ghost may show, even
// if some other code path sets a flag on it.
func TestGhostGutterAdmitsNothingElse(t *testing.T) {
	r := railRow{ghost: true, bell: true, done: true, act: true, inView: true, sess: "api"}
	if got := r.gutter(); got != "○" {
		t.Errorf("ghost gutter = %q, want ○ alone", got)
	}
	if !strings.Contains(r.plain(), "○") {
		t.Errorf("`rail once` hides ghosts: %q", r.plain())
	}
	if !strings.Contains(r.marks(), "ghost") {
		t.Errorf("marks() = %q, want a ghost flag", r.marks())
	}
}

// TestSummonTmuxGhostStartsInRecordedDir: ↵ on a ghost creates the declared
// name in the declared dir and views it. Nothing is "restored" — a new session
// with that name and dir is the entire claim the row was making.
func TestSummonTmuxGhostStartsInRecordedDir(t *testing.T) {
	withoutZellij(t)
	dir := t.TempDir()
	var calls []string
	recordTmux(t, &calls, nil)

	vp := &fakeViewport{}
	m := &railModel{vp: vp, collapsed: map[string]bool{}}
	m.activateRow(railRow{ghost: true, flat: true, label: "api", sess: "api", dir: dir})

	want := "new-session -d -s api -c " + dir
	found := false
	for _, c := range calls {
		if c == want {
			found = true
		}
	}
	if !found {
		t.Errorf("summon did not run %q, calls: %v", want, calls)
	}
	if vp.Lock().Sess != "api" {
		t.Errorf("summoned session not viewed: %+v", vp.Lock())
	}
}

// TestSummonToleratesADuplicateSession: the session may spring to life between
// the render and the keypress. That is the outcome we wanted, not an error —
// but only when tmux confirms the name really is there.
func TestSummonToleratesADuplicateSession(t *testing.T) {
	withoutZellij(t)
	var calls []string
	recordTmux(t, &calls, fmt.Errorf("exit status 1")) // real tmux keeps its words on stderr

	vp := &fakeViewport{}
	m := &railModel{vp: vp, collapsed: map[string]bool{}}
	if err := m.summonRow(railRow{ghost: true, sess: "api"}); err != nil {
		t.Fatalf("duplicate session treated as a failure: %v", err)
	}
	if vp.Lock().Sess != "api" {
		t.Errorf("existing session was not viewed: %+v", vp.Lock())
	}

	// ...and when tmux says the name is NOT there, the failure is real.
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) { return "", fmt.Errorf("exit status 1") }
	t.Cleanup(func() { tmux.Runner = orig })
	vp2 := &fakeViewport{}
	m2 := &railModel{vp: vp2, collapsed: map[string]bool{}}
	if err := m2.summonRow(railRow{ghost: true, sess: "api"}); err == nil {
		t.Errorf("a genuine new-session failure was swallowed")
	}
	if vp2.Lock().Sess != "" {
		t.Errorf("viewport pointed at a session that was never created: %+v", vp2.Lock())
	}
}

// TestSummonWithoutADirFallsBackToHome: an unrecorded dir is not a broken
// promise (nothing was promised), so it starts at home and flashes nothing. A
// dir that WAS recorded and has vanished says so.
func TestSummonWithoutADirFallsBackToHome(t *testing.T) {
	withoutZellij(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir on this box")
	}
	var calls []string
	recordTmux(t, &calls, nil)

	m := &railModel{vp: &fakeViewport{}, collapsed: map[string]bool{}}
	m.summonRow(railRow{ghost: true, sess: "api"})
	if want := "new-session -d -s api -c " + home; calls[0] != want {
		t.Errorf("unrecorded dir: ran %q, want %q", calls[0], want)
	}
	if m.errorActive() {
		t.Errorf("unrecorded dir flashed an error: %q", m.errMsg)
	}

	calls = nil
	m2 := &railModel{vp: &fakeViewport{}, collapsed: map[string]bool{}}
	m2.summonRow(railRow{ghost: true, sess: "api", dir: "/nonexistent/ghostmux-spec"})
	if want := "new-session -d -s api -c " + home; calls[0] != want {
		t.Errorf("vanished dir: ran %q, want %q", calls[0], want)
	}
	if !m2.errorActive() || !strings.Contains(m2.errMsg, "dir gone") {
		t.Errorf("vanished dir said nothing: %q", m2.errMsg)
	}
}

// TestSummonZellijExitedResurrectsWithoutCreating: zellij's EXITED sessions are
// its own feature. Attaching IS the resurrection, so we relay it — creating
// would throw the serialized session away.
func TestSummonZellijExitedResurrectsWithoutCreating(t *testing.T) {
	withZellijList(t, "myz [Created 1m ago] (EXITED - attach to resurrect)\n")
	var created []string
	origCreate := createAux
	createAux = func(backend, name string) error {
		created = append(created, backend+":"+name)
		return nil
	}
	t.Cleanup(func() { createAux = origCreate })

	vp := &fakeViewport{}
	m := &railModel{vp: vp, collapsed: map[string]bool{}}
	m.summonRow(railRow{ghost: true, flat: true, sess: "myz", label: "myz", backend: "zellij"})

	if len(created) != 0 {
		t.Errorf("resurrection went through create: %v", created)
	}
	if lock := vp.Lock(); lock.Backend != "zellij" || lock.Sess != "myz" {
		t.Errorf("viewport not attached to the resurrected session: %+v", lock)
	}
}

// TestSummonZellijDeclarationGhostCreates: a name zellij has forgotten
// entirely leaves only our declaration, so a fresh session is what we owe.
func TestSummonZellijDeclarationGhostCreates(t *testing.T) {
	withZellijList(t, "No active zellij sessions found.\n")
	var created []string
	origCreate := createAux
	createAux = func(backend, name string) error {
		created = append(created, backend+":"+name)
		return nil
	}
	t.Cleanup(func() { createAux = origCreate })

	vp := &fakeViewport{}
	m := &railModel{vp: vp, collapsed: map[string]bool{}}
	m.summonRow(railRow{ghost: true, flat: true, sess: "myz", label: "myz", backend: "zellij"})

	if len(created) != 1 || created[0] != "zellij:myz" {
		t.Errorf("createAux calls = %v, want [zellij:myz]", created)
	}
	if vp.Lock().Sess != "myz" {
		t.Errorf("created session not viewed: %+v", vp.Lock())
	}
}

// TestPointRowRefusesGhosts is the belt to activateRow's braces: no path may
// attach the viewport to a name with nothing behind it.
func TestPointRowRefusesGhosts(t *testing.T) {
	vp := &fakeViewport{}
	m := &railModel{vp: vp, collapsed: map[string]bool{}}
	m.pointRow(railRow{ghost: true, sess: "api", label: "api"})
	m.pointRow(railRow{ghost: true, sess: "myz", label: "myz", backend: "zellij"})
	if len(vp.points) != 0 {
		t.Errorf("pointRow attached to a ghost: %v", vp.points)
	}
}

// TestXOnADeclarationGhostForgets: nothing is left to kill, so `x` prunes the
// declaration — which is how the state file stops accumulating names the user
// can no longer see. The prompt says the real verb.
func TestXOnADeclarationGhostForgets(t *testing.T) {
	withoutZellij(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	withFakeRunner(t, map[string]string{
		"list-sessions": "api\t0\t/tmp\n",
		"list-windows":  "api\t1\tzsh\t1\t0\t0\t0\n",
	})
	m := railModel{
		vp: &fakeViewport{}, collapsed: map[string]bool{},
		groups: []Group{{Name: "work", Members: []string{"tmux:api", "tmux:web"}}},
		dirs:   map[string]string{"tmux:web": "/home/g/web"},
	}
	m.refresh()
	m.cursor = ghostAt(t, &m)

	n1, _ := m.Update(key("x"))
	m1 := n1.(railModel)
	if m1.killKind != killForget {
		t.Fatalf("x on a declaration ghost armed %q, want forget", m1.killKind.verb())
	}
	if !strings.Contains(m1.hintLine(), "forget web") {
		t.Errorf("confirm prompt does not name the real action: %q", m1.hintLine())
	}

	n2, _ := m1.Update(key("y"))
	m2 := n2.(railModel)
	if groupOf(m2.groups, "tmux:web") != "" {
		t.Errorf("forget left the member in its group: %+v", m2.groups)
	}
	if _, ok := m2.dirs["tmux:web"]; ok {
		t.Errorf("forget left the dir behind: %+v", m2.dirs)
	}
	b, err := os.ReadFile(groupsPath())
	if err != nil {
		t.Fatalf("forget did not save state: %v", err)
	}
	if strings.Contains(string(b), "tmux:web") {
		t.Errorf("state file still declares the forgotten member: %s", b)
	}
}

// TestXOnAZellijExitedGhostDeletes: there is no process to kill, but there IS
// a serialized session — so the verb is delete, and the declaration goes with
// it, because one x should fully remove what you can see.
func TestXOnAZellijExitedGhostDeletes(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	withFakeRunner(t, map[string]string{"list-sessions": "", "list-windows": ""})
	withZellijList(t, "myz [Created 1m ago] (EXITED - attach to resurrect)\n")
	var deleted []string
	origDelete := deleteAux
	deleteAux = func(backend, name string) error {
		deleted = append(deleted, backend+":"+name)
		return nil
	}
	t.Cleanup(func() { deleteAux = origDelete })

	m := railModel{
		vp: &fakeViewport{}, collapsed: map[string]bool{},
		groups: []Group{{Name: "work", Members: []string{"zellij:myz"}}},
	}
	m.refresh()
	m.cursor = ghostAt(t, &m)

	n1, _ := m.Update(key("x"))
	m1 := n1.(railModel)
	if m1.killKind != killDelete {
		t.Fatalf("x on an EXITED zellij ghost armed %q, want delete", m1.killKind.verb())
	}
	if !strings.Contains(m1.hintLine(), "delete myz") {
		t.Errorf("confirm prompt does not name the real action: %q", m1.hintLine())
	}

	n2, _ := m1.Update(key("y"))
	m2 := n2.(railModel)
	if len(deleted) != 1 || deleted[0] != "zellij:myz" {
		t.Errorf("deleteAux calls = %v, want [zellij:myz]", deleted)
	}
	if groupOf(m2.groups, "zellij:myz") != "" {
		t.Errorf("deleting the session left a pure declaration ghost: %+v", m2.groups)
	}
}

// TestDirCaptureRecordsGroupedSessionsOnce: dirs are evidence, taken while the
// session lives. Grouped only (an ungrouped session is cattle), and written
// once per change — this runs on a 1s tick, so an unchanged path must not
// rewrite the file.
func TestDirCaptureRecordsGroupedSessionsOnce(t *testing.T) {
	withoutZellij(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	withFakeRunner(t, map[string]string{
		"list-sessions": "api\t0\t/home/g/Projects/api\nstray\t0\t/tmp\n",
		"list-windows": "api\t1\tzsh\t1\t0\t0\t0\n" +
			"stray\t1\tzsh\t1\t0\t0\t0\n",
	})
	m := &railModel{
		vp: &fakeViewport{}, collapsed: map[string]bool{},
		groups: []Group{{Name: "work", Members: []string{"tmux:api"}}},
		dirs:   map[string]string{},
	}
	m.refresh()

	if m.dirs["tmux:api"] != "/home/g/Projects/api" {
		t.Errorf("grouped session's dir not captured: %+v", m.dirs)
	}
	if _, ok := m.dirs["tmux:stray"]; ok {
		t.Errorf("ungrouped session's dir recorded: %+v", m.dirs)
	}
	b, err := os.ReadFile(groupsPath())
	if err != nil {
		t.Fatalf("dir capture did not save state: %v", err)
	}
	if !strings.Contains(string(b), `"dirs"`) {
		t.Errorf("state file has no dirs map: %s", b)
	}

	// Deleting the file is the sharpest way to ask "did it write again?".
	if err := os.Remove(groupsPath()); err != nil {
		t.Fatal(err)
	}
	m.refresh()
	if _, err := os.Stat(groupsPath()); err == nil {
		t.Errorf("an unchanged dir rewrote the state file")
	}
}

// TestDirsRoundTripAndOldFilesLoad: the new key must survive a relaunch, and a
// file written before it existed must still load — the state file is the one
// thing a relaunch cannot rediscover.
func TestDirsRoundTripAndOldFilesLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	groups := []Group{{Name: "work", Members: []string{"tmux:api"}}}
	if err := saveState(groups, nil, map[string]string{"tmux:api": "/srv/api"}); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	_, _, dirs := loadState()
	if dirs["tmux:api"] != "/srv/api" {
		t.Errorf("dirs lost on round trip: %+v", dirs)
	}

	// An empty map writes no key at all: a fleet with no ghosts writes the
	// same file it always did.
	if err := saveState(groups, nil, nil); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	b, err := os.ReadFile(groupsPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "dirs") {
		t.Errorf("empty dirs map was written: %s", b)
	}
	// ...and that file — an old file, by construction — loads clean.
	_, collapsed, dirs := loadState()
	if dirs == nil || len(dirs) != 0 || collapsed == nil {
		t.Errorf("old file did not load into empty maps: dirs=%+v collapsed=%+v", dirs, collapsed)
	}
}

// TestSummonGroupStartsOnlyTheDead: S is the fleet verb — one press for the
// whole workspace. It must not touch what is already running.
func TestSummonGroupStartsOnlyTheDead(t *testing.T) {
	withoutZellij(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var calls []string
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "list-sessions":
			return "api\t0\t/tmp\n", nil
		case "list-windows":
			return "api\t1\tzsh\t1\t0\t0\t0\n", nil
		}
		return "", nil
	}
	t.Cleanup(func() { tmux.Runner = orig })

	m := railModel{
		vp: &fakeViewport{}, collapsed: map[string]bool{},
		groups: []Group{{Name: "work", Members: []string{"tmux:api", "tmux:web", "tmux:dots"}}},
		dirs:   map[string]string{"tmux:web": "/tmp"},
	}
	m.refresh()
	m.cursor = 0
	if !m.visible()[0].isGroup {
		t.Fatalf("cursor setup wrong: %+v", m.visible()[0])
	}
	calls = nil
	m.Update(key("S"))

	var started []string
	for _, c := range calls {
		if strings.HasPrefix(c, "new-session") {
			started = append(started, c)
		}
	}
	if len(started) != 2 {
		t.Fatalf("S started %d sessions, want the 2 ghosts: %v", len(started), started)
	}
	if !strings.Contains(strings.Join(started, "|"), "new-session -d -s web -c /tmp") {
		t.Errorf("S ignored the recorded dir: %v", started)
	}
	for _, c := range started {
		if strings.Contains(c, "-s api ") {
			t.Errorf("S restarted a live session: %v", started)
		}
	}
}

// TestSummonGroupIsANoOpOffAGroupRow: S is about a fleet. On a single row ↵
// already says it, and a key that silently did something else would be worse
// than one that does nothing.
func TestSummonGroupIsANoOpOffAGroupRow(t *testing.T) {
	withoutZellij(t)
	var calls []string
	recordTmux(t, &calls, nil)
	m := &railModel{vp: &fakeViewport{}, collapsed: map[string]bool{}}
	m.summonGroup(railRow{ghost: true, sess: "api", group: "work"})
	for _, c := range calls {
		if strings.HasPrefix(c, "new-session") {
			t.Errorf("S on a non-group row created a session: %v", calls)
		}
	}
}

// TestGhostHintLineSpeaksTheRealVerbs: ↵ on a ghost CREATES something, so the
// last line says where — and what x would take away — before you commit.
func TestGhostHintLineSpeaksTheRealVerbs(t *testing.T) {
	tmuxGhost := railRow{
		ghost: true, flat: true, label: "api", sess: "api",
		dir: "/home/george/Projects/api",
	}
	m := railModel{rows: []railRow{tmuxGhost}, collapsed: map[string]bool{}}
	hint := m.hintLine()
	// The dir is what gets shortened (from the left, keeping the tail that
	// identifies it) — never the verbs.
	for _, want := range []string{"start in", "…ts/api", "x forget"} {
		if !strings.Contains(hint, want) {
			t.Errorf("tmux ghost hint %q missing %q", hint, want)
		}
	}
	if w := len([]rune(strings.TrimSpace(hint))); w > railWidth {
		t.Errorf("hint overflows the rail: %d cols, %q", w, hint)
	}

	zj := railRow{ghost: true, flat: true, label: "myz", sess: "myz", backend: "zellij"}
	m2 := railModel{rows: []railRow{zj}, collapsed: map[string]bool{}}
	hint2 := m2.hintLine()
	for _, want := range []string{"resurrect", "x delete"} {
		if !strings.Contains(hint2, want) {
			t.Errorf("zellij ghost hint %q missing %q", hint2, want)
		}
	}

	// A live row keeps the empty last line: the frame's bar owns the keymap.
	m3 := railModel{rows: []railRow{sessionRow("api")}, collapsed: map[string]bool{}}
	if got := m3.hintLine(); got != "" {
		t.Errorf("live row hint = %q, want empty", got)
	}
}

// TestCollapsedGroupReportsLiveAndDeadApart: "2 ○1" is a different fleet from
// "3", and a folder that rounds them together lies about what S would do.
func TestCollapsedGroupReportsLiveAndDeadApart(t *testing.T) {
	row := railRow{isGroup: true, collapsed: true, label: "work", sess: "work", count: 2, ghostCount: 1}
	if got := renderRow(row, false, 0, ""); !strings.Contains(got, "2 ○1") {
		t.Errorf("collapsed group render = %q, want a `2 ○1` suffix", got)
	}
	ghostsOnly := railRow{isGroup: true, collapsed: true, label: "agents", sess: "agents", ghostCount: 2}
	if got := renderRow(ghostsOnly, false, 0, ""); !strings.Contains(got, "○2") {
		t.Errorf("ghost-only collapsed group render = %q, want `○2`", got)
	}
}
