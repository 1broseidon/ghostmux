package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

// fakeWallTmux is a self-contained tmux double for PointWall: it tracks
// sessions by name (member origins plus whatever new-session calls create —
// member shadows and the wall itself), tags ownership on set-option, and
// answers list-sessions for ProbeSession the way heal needs. Attach argv is
// never executed through Runner (bindFakeChild intercepts startChild
// directly), so this double only has to model the commands PointWall/Heal
// actually run: new-session, set-option, split-window, select-layout, the
// ownership-checked kill's if-shell, and list-*.
type fakeWallTmux struct {
	calls       []string
	nextID      int
	sessions    map[string]bool   // name -> alive
	ids         map[string]string // name -> session id
	owned       map[string]bool   // id -> tagged owned
	cleanupFail map[string]int    // id -> remaining forced cleanup failures
	backendErr  error
}

func newFakeWallTmux(origins ...string) *fakeWallTmux {
	f := &fakeWallTmux{
		sessions: map[string]bool{}, ids: map[string]string{},
		owned: map[string]bool{}, cleanupFail: map[string]int{},
	}
	for _, o := range origins {
		f.nextID++
		id := "$" + itoa(f.nextID)
		f.sessions[o] = true
		f.ids[o] = id
	}
	return f
}

func argAfter(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func (f *fakeWallTmux) idFor(name string) string { return f.ids[name] }

func (f *fakeWallTmux) run(args ...string) (string, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	switch args[0] {
	case "new-session":
		name := argAfter(args, "-s")
		f.nextID++
		id := "$" + itoa(f.nextID)
		f.sessions[name] = true
		f.ids[name] = id
		return id + "\n", nil
	case "set-option":
		if len(args) >= 5 && args[3] == "@ghostmux_view_owner" {
			f.owned[args[2]] = true
		}
		return "", nil
	case "if-shell":
		id := args[3]
		if f.cleanupFail[id] > 0 {
			f.cleanupFail[id]--
			return "", errors.New("cleanup unavailable")
		}
		// Only a kill's true-branch may retire a session; an attach if-shell
		// is never sent through Runner (see the type comment).
		if len(args) >= 6 && strings.HasPrefix(args[5], "kill-session") && f.owned[id] {
			for name, sid := range f.ids {
				if sid == id {
					f.sessions[name] = false
				}
			}
		}
		return "", nil
	case "list-sessions":
		if f.backendErr != nil {
			return "", f.backendErr
		}
		var b strings.Builder
		for name, alive := range f.sessions {
			if !alive {
				continue
			}
			b.WriteString(name + "\t" + f.ids[name] + "\t0\t/tmp\t\n")
		}
		return b.String(), nil
	case "list-clients":
		return "", nil
	case "list-windows":
		// Query rejects any session with no window rows, so ProbeSession needs
		// one synthetic window per alive session to see it as present at all.
		var b strings.Builder
		for name, alive := range f.sessions {
			if !alive {
				continue
			}
			id := f.ids[name]
			b.WriteString(name + "\t" + id + "\t@" + id[1:] + "\t1\tshell\t1\t0\t0\t0\t\t\n")
		}
		return b.String(), nil
	case "list-panes":
		var b strings.Builder
		for name, alive := range f.sessions {
			if !alive {
				continue
			}
			b.WriteString(name + "\t1\tzsh\t%1\t0\t0\t0\t1\n")
		}
		return b.String(), nil
	}
	return "", nil
}

func useFakeWallTmux(t *testing.T, origins ...string) *fakeWallTmux {
	t.Helper()
	f := newFakeWallTmux(origins...)
	orig := tmux.Runner
	tmux.Runner = f.run
	t.Cleanup(func() { tmux.Runner = orig })
	return f
}

// killCallsFor counts ownership-checked kill attempts issued for id.
func killCallsFor(calls []string, id string) int {
	n := 0
	prefix := "if-shell -F -t " + id + " "
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// TestPointWallCreatesShadowsThenTheTiledCompositeThenAttaches pins the
// creation order the spec's mechanics section names: per-member shadows
// first (never a direct attach to the origin), then the owned composite
// sized to the viewport with one pane per shadow, then the child attaches to
// the composite — never with -A, which would join the wall to another
// client's window focus instead of composing its own.
func TestPointWallCreatesShadowsThenTheTiledCompositeThenAttaches(t *testing.T) {
	fake := useFakeWallTmux(t, "alpha", "beta")
	v := newTestViewport(t)
	child := bindFakeChild(v)
	v.panelNonce = "panel"

	v.PointWall("work", []string{"alpha", "beta"})

	if v.wallGroup != "work" {
		t.Fatalf("wallGroup = %q, want work", v.wallGroup)
	}
	if len(v.wallShadows) != 2 || len(v.wallOrigins) != 2 {
		t.Fatalf("wall shadows/origins = %+v/%v, want 2 each", v.wallShadows, v.wallOrigins)
	}
	if v.wallOrigins[0] != "alpha" || v.wallOrigins[1] != "beta" {
		t.Fatalf("wall origins = %v, want [alpha beta] in member order", v.wallOrigins)
	}
	if !strings.HasPrefix(v.wall.Name, tmux.WallPrefix) {
		t.Fatalf("wall session name = %q, want %s prefix", v.wall.Name, tmux.WallPrefix)
	}
	for _, shadow := range v.wallShadows {
		if !strings.HasPrefix(shadow.Name, tmux.ViewPrefix) {
			t.Fatalf("member shadow name = %q, want %s prefix", shadow.Name, tmux.ViewPrefix)
		}
	}

	newSessionIdx, splitIdx, layoutIdx := -1, -1, -1
	for i, c := range fake.calls {
		if strings.HasPrefix(c, "new-session") && strings.Contains(c, "-s "+v.wall.Name) && newSessionIdx < 0 {
			newSessionIdx = i
		}
		if strings.HasPrefix(c, "split-window") && splitIdx < 0 {
			splitIdx = i
		}
		if strings.HasPrefix(c, "select-layout") && strings.Contains(c, "tiled") {
			layoutIdx = i
		}
	}
	if newSessionIdx < 0 || splitIdx < 0 || layoutIdx < 0 || !(newSessionIdx < splitIdx && splitIdx < layoutIdx) {
		t.Fatalf("wall creation order wrong: new=%d split=%d layout=%d calls=%v", newSessionIdx, splitIdx, layoutIdx, fake.calls)
	}
	// Both member new-session calls (the shadows) precede the wall's own.
	for _, shadow := range v.wallShadows {
		found := -1
		for i, c := range fake.calls {
			if strings.HasPrefix(c, "new-session") && strings.Contains(c, "-s "+shadow.Name) {
				found = i
				break
			}
		}
		if found < 0 || found > newSessionIdx {
			t.Fatalf("shadow %s not created before the wall session: calls=%v", shadow.Name, fake.calls)
		}
	}

	if len(child.starts) != 1 {
		t.Fatalf("child started %d times, want 1: %v", len(child.starts), child.starts)
	}
	argv := strings.Join(child.starts[0], " ")
	if !strings.Contains(argv, "attach-session") || !strings.Contains(argv, v.wall.SessionID) {
		t.Fatalf("child did not attach the wall session: %v", child.starts[0])
	}
	if strings.Contains(argv, " -A ") {
		t.Fatalf("wall attach used -A: %v", child.starts[0])
	}

	lock := v.Lock()
	if !lock.Wall || lock.Sess != "work" {
		t.Fatalf("Lock() = %+v, want a walled lock on work", lock)
	}
	if v.AttachTarget() != v.wall.Name {
		t.Fatalf("AttachTarget() = %q, want the wall session %q", v.AttachTarget(), v.wall.Name)
	}

	// SPEC-OWNED-CHROME: origins (never shadow gm-view-* names) and the two
	// theme-resolved colors this package threads down must reach CreateWall,
	// landing as chrome commands against the wall's exact session ID.
	wallID := v.wall.SessionID
	wantChrome := []string{
		"set-option -t " + wallID + " status off",
		"set-option -t " + wallID + " -w pane-border-style fg=" + wallBorderDim,
		"set-option -t " + wallID + " -w pane-active-border-style fg=" + wallBorderAccent,
		"select-pane -t " + wallID + " -T alpha",
		"select-pane -t " + wallID + " -T beta",
	}
	for _, want := range wantChrome {
		found := false
		for _, c := range fake.calls {
			if c == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("wall chrome missing %q in %v", want, fake.calls)
		}
	}
	for _, c := range fake.calls {
		if strings.Contains(c, "gm-view") && strings.HasPrefix(c, "select-pane") {
			t.Fatalf("pane title leaked a shadow name instead of an origin: %q", c)
		}
	}
}

// TestPointWallTeardownRetiresWallAndEveryShadowExactlyOnce: v again (or d,
// or panel close) must kill the composite and every member shadow, each
// exactly once — no leftover gm-wall or gm-view session, and no repeated
// kill once retirement already succeeded.
func TestPointWallTeardownRetiresWallAndEveryShadowExactlyOnce(t *testing.T) {
	fake := useFakeWallTmux(t, "alpha", "beta", "gamma")
	v := newTestViewport(t)
	bindFakeChild(v)
	v.panelNonce = "panel"
	v.PointWall("work", []string{"alpha", "beta", "gamma"})

	wallID := v.wall.SessionID
	shadowIDs := make([]string, len(v.wallShadows))
	for i, s := range v.wallShadows {
		shadowIDs[i] = s.SessionID
	}
	fake.calls = nil

	v.Idle()

	if killCallsFor(fake.calls, wallID) != 1 {
		t.Fatalf("wall %s retired %d times, want 1: %v", wallID, killCallsFor(fake.calls, wallID), fake.calls)
	}
	for _, id := range shadowIDs {
		if killCallsFor(fake.calls, id) != 1 {
			t.Fatalf("shadow %s retired %d times, want 1: %v", id, killCallsFor(fake.calls, id), fake.calls)
		}
		if fake.sessions[fake.nameFor(id)] {
			t.Fatalf("shadow %s survived teardown", id)
		}
	}
	if fake.sessions[v.wall.Name] {
		t.Fatalf("wall session survived teardown")
	}
	if v.wallGroup != "" || v.wall != (tmux.ViewRef{}) || len(v.wallShadows) != 0 {
		t.Fatalf("wall state not cleared after teardown: group=%q wall=%+v shadows=%+v", v.wallGroup, v.wall, v.wallShadows)
	}
	if lock := v.Lock(); lock.Wall || lock.Sess != "" {
		t.Fatalf("Lock() after teardown = %+v, want idle", lock)
	}
}

// nameFor is the reverse of fakeWallTmux.ids, for teardown assertions.
func (f *fakeWallTmux) nameFor(id string) string {
	for name, sid := range f.ids {
		if sid == id {
			return name
		}
	}
	return ""
}

// TestHealReattachesDeadChildWhileWallSessionSurvives: a dead child with the
// wall session still present re-attaches, per the spec's heal mechanics.
func TestHealReattachesDeadChildWhileWallSessionSurvives(t *testing.T) {
	useFakeWallTmux(t, "alpha", "beta")
	v := newTestViewport(t)
	child := bindFakeChild(v)
	v.panelNonce = "panel"
	v.PointWall("work", []string{"alpha", "beta"})
	child.running = false // e.g. the emulator's process exited

	dead, err := v.Heal()
	if err != nil || !dead {
		t.Fatalf("Heal() = (%v, %v), want (true, nil)", dead, err)
	}
	if len(child.starts) != 2 {
		t.Fatalf("child restarted %d times, want 2 (initial + reattach): %v", len(child.starts), child.starts)
	}
	if v.wallGroup != "work" {
		t.Fatalf("wallGroup lost across reattach: %q", v.wallGroup)
	}
}

// TestHealIdlesWhenWallSessionIsAbsent: a crash (or external kill) leaving
// the wall session gone must idle the panel, not spin re-attaching to
// nothing.
func TestHealIdlesWhenWallSessionIsAbsent(t *testing.T) {
	fake := useFakeWallTmux(t, "alpha", "beta")
	v := newTestViewport(t)
	child := bindFakeChild(v)
	v.panelNonce = "panel"
	v.PointWall("work", []string{"alpha", "beta"})

	fake.sessions[v.wall.Name] = false // externally killed
	child.running = false

	dead, err := v.Heal()
	if err != nil || !dead {
		t.Fatalf("Heal() = (%v, %v), want (true, nil)", dead, err)
	}
	if v.wallGroup != "" || v.Lock().Wall {
		t.Fatalf("panel did not idle: wallGroup=%q lock=%+v", v.wallGroup, v.Lock())
	}
	if len(v.wallShadows) != 0 {
		t.Fatalf("member shadows not retired alongside the absent wall: %+v", v.wallShadows)
	}
}

// TestHealRetiresMemberShadowWhoseOriginDiedAndKeepsTheWall: the wall
// survives with its remaining panes; only the dead member's shadow is
// retired.
func TestHealRetiresMemberShadowWhoseOriginDiedAndKeepsTheWall(t *testing.T) {
	fake := useFakeWallTmux(t, "alpha", "beta")
	v := newTestViewport(t)
	bindFakeChild(v)
	v.panelNonce = "panel"
	v.PointWall("work", []string{"alpha", "beta"})
	deadShadow := v.wallShadows[0].SessionID

	fake.sessions["alpha"] = false // the member's origin died
	fake.calls = nil

	dead, err := v.Heal()
	if err != nil || dead {
		t.Fatalf("Heal() = (%v, %v), want (false, nil): the wall itself is still alive", dead, err)
	}
	if killCallsFor(fake.calls, deadShadow) != 1 {
		t.Fatalf("dead member's shadow was not retired: %v", fake.calls)
	}
	if len(v.wallShadows) != 1 || v.wallOrigins[0] != "beta" {
		t.Fatalf("surviving member not kept: shadows=%+v origins=%v", v.wallShadows, v.wallOrigins)
	}
	if v.wallGroup != "work" {
		t.Fatalf("wall torn down despite a surviving member: wallGroup=%q", v.wallGroup)
	}
}

// TestPendingRetirementOnFailedShadowKillIsRetainedAndRetried mirrors the
// single-view pending-retirement contract for a wall member: a cleanup
// failure is retained (not silently dropped, not silently retried forever)
// and a later Heal finishes it.
func TestPendingRetirementOnFailedShadowKillIsRetainedAndRetried(t *testing.T) {
	fake := useFakeWallTmux(t, "alpha", "beta")
	v := newTestViewport(t)
	bindFakeChild(v)
	v.panelNonce = "panel"
	v.PointWall("work", []string{"alpha", "beta"})
	stuckID := v.wallShadows[0].SessionID
	fake.cleanupFail[stuckID] = 2

	v.Idle()
	if len(v.pendingRetirements) != 1 || v.pendingRetirements[0].ref.SessionID != stuckID {
		t.Fatalf("failed shadow cleanup was not retained: %+v", v.pendingRetirements)
	}
	if !fake.sessions[fake.nameFor(stuckID)] {
		t.Fatalf("shadow killed despite a forced cleanup failure")
	}

	dead, err := v.Heal()
	if dead || err == nil || !strings.Contains(err.Error(), "retire owned tmux view") || len(v.pendingRetirements) != 1 {
		t.Fatalf("persistent cleanup failure was not surfaced: dead=%v err=%v pending=%+v", dead, err, v.pendingRetirements)
	}
	dead, err = v.Heal()
	if dead || err != nil || len(v.pendingRetirements) != 0 || fake.sessions[fake.nameFor(stuckID)] {
		t.Fatalf("later heal did not finish the retained retirement: dead=%v err=%v pending=%+v", dead, err, v.pendingRetirements)
	}
}

// TestPointAfterWallClearsWallState is the "tmux unavailable: probe tmux
// session: empty name" regression: pointing at a session while walled must
// clear the wall's logical state with its session, or every subsequent heal
// probes an empty wall name forever and the lock keeps claiming a wall.
func TestPointAfterWallClearsWallState(t *testing.T) {
	fake := useFakeViewTmux(t)
	v := newTestViewport(t)
	bindFakeChild(v)

	v.PointWall("agents", []string{"ada", "ifrit"})
	if lock := v.Lock(); !lock.Wall {
		t.Fatalf("wall did not come up: %+v", lock)
	}

	v.Point("metro", "", false)
	if lock := v.Lock(); lock.Wall || lock.Sess != "metro" {
		t.Fatalf("point after wall left wall state: %+v", lock)
	}
	fake.calls = nil
	if dead, err := v.Heal(); err != nil || dead {
		t.Fatalf("heal after point-from-wall = (%v, %v), want quiet (false, nil)", dead, err)
	}
	for _, c := range fake.calls {
		if strings.Contains(c, "probe") && strings.Contains(c, `""`) {
			t.Fatalf("heal probed an empty name: %v", fake.calls)
		}
	}
}
