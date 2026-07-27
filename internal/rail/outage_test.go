package rail

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

type stagedBackends struct {
	stage        int
	setDoneCalls int
}

func (s *stagedBackends) tmuxRun(args ...string) (string, error) {
	if args[0] == "set-option" {
		if len(args) > 4 && args[4] == "@ghostmux_done" {
			s.setDoneCalls++
		}
		return "", nil
	}
	if s.stage == 1 && args[0] == "list-clients" {
		return "", errors.New("tmux transport down")
	}
	path, command := "/old", "node"
	if s.stage >= 2 {
		path, command = "/new", "zsh"
	}
	switch args[0] {
	case "list-sessions":
		return "alpha\t$1\t0\t" + path + "\t\n", nil
	case "list-clients":
		return "", nil
	case "list-windows":
		return "alpha\t$1\t@1\t1\tshell\t1\t1\t0\t100\t\t\n", nil
	case "list-panes":
		return "alpha\t1\t" + command + "\n", nil
	default:
		return "", nil
	}
}

func TestTmuxCacheRetainsStaleSnapshotWithoutSideEffects(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	origRunner, origTmuxPresent := tmux.Runner, tmuxPresent
	staged := &stagedBackends{stage: 1}
	tmux.Runner, tmuxPresent = staged.tmuxRun, func() bool { return true }
	t.Cleanup(func() { tmux.Runner, tmuxPresent = origRunner, origTmuxPresent })

	vp := &fakeViewport{}
	m := railModel{
		vp: vp, done: newDoneTracker(), collapsed: map[string]bool{},
		groups: []Group{{Name: "work", Members: []string{"tmux:alpha", "tmux:gone", "zellij:zeta"}}},
		dirs:   map[string]string{},
	}
	staged.stage = 0
	m.refresh()
	if m.tmuxValidity() != rowFresh || m.dirs["tmux:alpha"] != "/old" {
		t.Fatalf("initial cache/dir = tmux:%v dirs:%v", m.tmuxValidity(), m.dirs)
	}
	// A member key written by the retired multi-backend prototype must stay in
	// the state file but never render a row.
	for _, row := range m.rows {
		if row.sess == "zeta" {
			t.Fatalf("foreign member key rendered: %+v", row)
		}
	}
	if groupOf(m.groups, "zellij:zeta") == "" {
		t.Fatal("foreign member key dropped from saved groups")
	}
	initialSyncs := vp.syncCalls

	staged.stage = 1
	m.refresh()
	if m.tmuxValidity() != rowStale {
		t.Fatalf("query failure validity = %v", m.tmuxValidity())
	}
	alpha := findRow(t, m.rows, "alpha")
	if alpha.validity != rowStale || alpha.gutter() != "?" {
		t.Fatalf("cached tmux row not uncertain: %+v gutter=%q", alpha, alpha.gutter())
	}
	gone := findRow(t, m.rows, "gone")
	if gone.ghost || gone.validity != rowStale || gone.gutter() != "?" {
		t.Fatalf("stale absence became a ghost: %+v", gone)
	}
	if m.dirs["tmux:alpha"] != "/old" || vp.syncCalls != initialSyncs || staged.setDoneCalls != 0 {
		t.Fatalf("stale tmux caused side effects: dirs=%v sync=%d done=%d", m.dirs, vp.syncCalls, staged.setDoneCalls)
	}
	if !strings.Contains(m.backendStatus(), "tmux unavailable; showing last snapshot") {
		t.Fatalf("stale status = %q", m.backendStatus())
	}

	staged.stage = 2
	m.refresh()
	if m.tmuxValidity() != rowFresh || m.backendStatus() != "" {
		t.Fatalf("recovery = %v status=%q", m.tmuxValidity(), m.backendStatus())
	}
	if m.dirs["tmux:alpha"] != "/new" {
		t.Fatalf("fresh recovery did not capture dir: %v", m.dirs)
	}
	if staged.setDoneCalls != 0 {
		t.Fatalf("recovery inferred a done transition across outage: %d calls", staged.setDoneCalls)
	}
}

func TestTmuxCacheSurvivesExecutableDisappearanceAndRecovers(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	origRunner, origPresent := tmux.Runner, tmuxPresent
	installed := true
	path := "/first"
	tmuxPresent = func() bool { return installed }
	tmux.Runner = func(args ...string) (string, error) {
		switch args[0] {
		case "list-sessions":
			return "alpha\t$1\t0\t" + path + "\t\n", nil
		case "list-windows":
			return "alpha\t$1\t@1\t1\tzsh\t1\t0\t0\t100\t\t\n", nil
		case "list-panes":
			return "alpha\t1\tzsh\n", nil
		default:
			return "", nil
		}
	}
	t.Cleanup(func() { tmux.Runner, tmuxPresent = origRunner, origPresent })

	m := railModel{vp: &fakeViewport{}, done: newDoneTracker(), collapsed: map[string]bool{}}
	m.refresh()
	if m.tmuxValidity() != rowFresh || findRow(t, m.rows, "alpha").instanceID != "$1" {
		t.Fatalf("initial tmux snapshot not fresh: cache=%+v rows=%+v", m.tmuxCache, m.rows)
	}

	installed = false
	m.refresh()
	row := findRow(t, m.rows, "alpha")
	if row.validity != rowStale || row.gutter() != "?" ||
		!strings.Contains(m.backendStatus(), "tmux unavailable; showing last snapshot") {
		t.Fatalf("executable disappearance dropped/validated cache: row=%+v status=%q", row, m.backendStatus())
	}

	installed, path = true, "/recovered"
	m.refresh()
	if m.tmuxValidity() != rowFresh || m.backendStatus() != "" || m.tmuxCache.snapshot.Sessions[0].Path != "/recovered" {
		t.Fatalf("returned executable did not recover: cache=%+v status=%q", m.tmuxCache, m.backendStatus())
	}

	initial := railModel{vp: &fakeViewport{}, done: newDoneTracker(), collapsed: map[string]bool{}}
	installed = false
	initial.refresh()
	if initial.tmuxCache.enabled || initial.tmuxCache.lastErr != nil || initial.backendStatus() != "" {
		t.Fatalf("initial not-installed became an outage: %+v status=%q", initial.tmuxCache, initial.backendStatus())
	}
}

func findRow(t *testing.T, rows []railRow, name string) railRow {
	t.Helper()
	for _, row := range rows {
		if !row.isGroup && !row.isWin && row.sess == name {
			return row
		}
	}
	t.Fatalf("row %s not found in %+v", name, rows)
	return railRow{}
}

func TestUnvalidatedRowsAndUnavailableEmptyStateRecoverToGhosts(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	origRunner, origTmuxPresent := tmux.Runner, tmuxPresent
	failing := true
	tmuxPresent = func() bool { return true }
	tmux.Runner = func(args ...string) (string, error) {
		if failing && args[0] == "list-sessions" {
			return "", errors.New("tmux down")
		}
		return "", nil
	}
	t.Cleanup(func() { tmux.Runner, tmuxPresent = origRunner, origTmuxPresent })

	empty := railModel{vp: &fakeViewport{}, done: newDoneTracker(), collapsed: map[string]bool{}}
	empty.refresh()
	plain := ansi.Strip(empty.View())
	if strings.Contains(plain, "no sessions yet") || !strings.Contains(plain, "backend unavailable") {
		t.Fatalf("unavailable empty state lied about emptiness: %q", plain)
	}

	m := railModel{
		vp: &fakeViewport{}, done: newDoneTracker(), collapsed: map[string]bool{},
		groups: []Group{{Name: "work", Members: []string{"tmux:api"}}},
	}
	m.refresh()
	row := findRow(t, m.rows, "api")
	if row.ghost || row.validity != rowUnvalidated || row.gutter() != "?" || !strings.Contains(ansi.Strip(renderRow(row, false, 0, "")), "?") {
		t.Fatalf("unvalidated declaration rendering = %+v gutter=%q", row, row.gutter())
	}

	failing = false
	m.refresh()
	row = findRow(t, m.rows, "api")
	if !row.ghost || row.validity != rowFresh || row.gutter() != "○" {
		t.Fatalf("authoritative absence did not become a ghost: %+v", row)
	}
	if m.backendStatus() != "" {
		t.Fatalf("successful refresh retained status: %q", m.backendStatus())
	}
}

func TestUncertainRowsDisableEnterXAndGroupStart(t *testing.T) {
	origRunner, origTmuxPresent := tmux.Runner, tmuxPresent
	var created []string
	tmuxPresent = func() bool { return true }
	tmux.Runner = func(args ...string) (string, error) {
		if args[0] == "new-session" {
			created = append(created, "tmux:"+args[4])
		}
		if args[0] == "list-sessions" {
			return "", errors.New("down")
		}
		return "", nil
	}
	t.Cleanup(func() { tmux.Runner, tmuxPresent = origRunner, origTmuxPresent })

	vp := &fakeViewport{}
	uncertain := railRow{flat: true, sess: "api", label: "api", validity: rowStale}
	m := railModel{vp: vp, rows: []railRow{uncertain}, collapsed: map[string]bool{}}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(railModel)
	if len(vp.points) != 0 || !strings.Contains(m.errMsg, "action disabled") {
		t.Fatalf("Enter on uncertain row attached: points=%v err=%q", vp.points, m.errMsg)
	}
	next, _ = m.Update(key("x"))
	m = next.(railModel)
	if m.mode == modeKillConfirm || !strings.Contains(m.errMsg, "action disabled") {
		t.Fatalf("x on uncertain row armed confirmation: mode=%v err=%q", m.mode, m.errMsg)
	}

	m.rows = []railRow{
		{isGroup: true, label: "work", sess: "work", uncertainCount: 1},
		{depth: 1, group: "work", flat: true, sess: "api", label: "api", validity: rowStale},
	}
	m.cursor = 0
	next, _ = m.Update(key("S"))
	m = next.(railModel)
	if len(created) != 0 || !strings.Contains(m.errMsg, "skipped 1 uncertain") {
		t.Fatalf("S created against uncertainty: created=%v err=%q", created, m.errMsg)
	}
}

func TestDestructiveValidationRacesAndErrorsDoNothing(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	origRunner, origTmuxPresent := tmux.Runner, tmuxPresent
	t.Cleanup(func() { tmux.Runner, tmuxPresent = origRunner, origTmuxPresent })
	tmuxPresent = func() bool { return true }

	t.Run("kill disappeared", func(t *testing.T) {
		var killed bool
		tmux.Runner = func(args ...string) (string, error) {
			if args[0] == "kill-session" {
				killed = true
			}
			if args[0] == "has-session" {
				return "", errors.New("no such session")
			}
			return "", nil // authoritative empty snapshot
		}
		m := confirmedModel(killLive, "api", nil)
		next, _ := m.Update(key("y"))
		got := next.(railModel)
		if killed || !strings.Contains(got.errMsg, "state changed") {
			t.Fatalf("kill race: killed=%v err=%q", killed, got.errMsg)
		}
	})

	t.Run("kill query error", func(t *testing.T) {
		var killed bool
		tmux.Runner = func(args ...string) (string, error) {
			if args[0] == "kill-session" {
				killed = true
			}
			if args[0] == "has-session" || args[0] == "list-sessions" {
				return "", errors.New("transport down")
			}
			return "", nil
		}
		m := confirmedModel(killLive, "api", nil)
		next, _ := m.Update(key("y"))
		got := next.(railModel)
		if killed || !strings.Contains(got.errMsg, "tmux unavailable") {
			t.Fatalf("kill outage: killed=%v err=%q", killed, got.errMsg)
		}
	})

	t.Run("forget became present", func(t *testing.T) {
		tmux.Runner = func(args ...string) (string, error) {
			switch args[0] {
			case "list-sessions":
				return "api\t$1\t0\t/tmp\t\n", nil
			case "list-windows":
				return "api\t$1\t@1\t1\tzsh\t1\t0\t0\t100\t\t\n", nil
			case "list-panes":
				return "api\t1\tzsh\n", nil
			default:
				return "", nil
			}
		}
		groups := []Group{{Name: "work", Members: []string{"tmux:api"}}}
		m := confirmedModel(killForget, "api", groups)
		next, _ := m.Update(key("y"))
		got := next.(railModel)
		if groupOf(got.groups, "tmux:api") == "" || !strings.Contains(got.errMsg, "state changed") {
			t.Fatalf("forget race mutated state: groups=%v err=%q", got.groups, got.errMsg)
		}
		if _, err := os.Stat(groupsPath()); !os.IsNotExist(err) {
			t.Fatalf("cancelled forget wrote state: %v", err)
		}
	})

	t.Run("forget query error", func(t *testing.T) {
		tmux.Runner = func(args ...string) (string, error) {
			if args[0] == "has-session" || args[0] == "list-sessions" {
				return "", errors.New("transport down")
			}
			return "", nil
		}
		groups := []Group{{Name: "work", Members: []string{"tmux:api"}}}
		m := confirmedModel(killForget, "api", groups)
		next, _ := m.Update(key("y"))
		got := next.(railModel)
		if groupOf(got.groups, "tmux:api") == "" || !strings.Contains(got.errMsg, "tmux unavailable") {
			t.Fatalf("forget outage mutated state: groups=%v err=%q", got.groups, got.errMsg)
		}
	})
}

func TestTmuxKillConfirmationIsPinnedToArmedSessionID(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	origRunner, origTmuxPresent := tmux.Runner, tmuxPresent
	tmuxPresent = func() bool { return true }
	t.Cleanup(func() { tmux.Runner, tmuxPresent = origRunner, origTmuxPresent })

	queryFor := func(id string, killed *bool, killTarget *string) func(...string) (string, error) {
		return func(args ...string) (string, error) {
			switch args[0] {
			case "list-sessions":
				return "api\t" + id + "\t0\t/tmp\t\n", nil
			case "list-windows":
				return "api\t" + id + "\t@1\t1\tzsh\t1\t0\t0\t100\t\t\n", nil
			case "list-panes":
				return "api\t1\tzsh\n", nil
			case "if-shell":
				*killed = true
				*killTarget = args[3] // stable -t session ID
			}
			return "", nil
		}
	}

	t.Run("same name replacement cancels without state mutation", func(t *testing.T) {
		var killed bool
		var killTarget string
		tmux.Runner = queryFor("$2", &killed, &killTarget) // armed row was old $1
		groups := []Group{{Name: "work", Members: []string{"tmux:api"}}}
		m := railModel{
			vp: &fakeViewport{}, rows: []railRow{{flat: true, sess: "api", label: "api", instanceID: "$1"}},
			groups: cloneGroups(groups), collapsed: map[string]bool{}, dirs: map[string]string{}, done: newDoneTracker(),
		}
		next, _ := m.Update(key("x"))
		armed := next.(railModel)
		if armed.killInstance != "$1" {
			t.Fatalf("confirmation did not capture row instance: %+v", armed)
		}
		next, _ = armed.Update(key("y"))
		got := next.(railModel)
		if killed || killTarget != "" || groupOf(got.groups, "tmux:api") == "" ||
			!strings.Contains(got.errMsg, "state changed") {
			t.Fatalf("replacement was killed/mutated: killed=%v target=%q groups=%v err=%q", killed, killTarget, got.groups, got.errMsg)
		}
		if _, err := os.Stat(groupsPath()); !os.IsNotExist(err) {
			t.Fatalf("cancelled replacement kill wrote saved state: %v", err)
		}
	})

	t.Run("matching instance is killed by stable id", func(t *testing.T) {
		var killed bool
		var target string
		tmux.Runner = queryFor("$7", &killed, &target)
		m := railModel{
			vp: &fakeViewport{}, rows: []railRow{{flat: true, sess: "api", label: "api", instanceID: "$7"}},
			collapsed: map[string]bool{}, dirs: map[string]string{}, done: newDoneTracker(),
		}
		next, _ := m.Update(key("x"))
		next, _ = next.(railModel).Update(key("y"))
		got := next.(railModel)
		if !killed || target != "$7" || strings.Contains(got.errMsg, "state changed") {
			t.Fatalf("matching instance kill = killed:%v target:%q err:%q", killed, target, got.errMsg)
		}
	})
}

func confirmedModel(kind killKind, name string, groups []Group) railModel {
	return railModel{
		vp: &fakeViewport{}, mode: modeKillConfirm,
		killKind: kind, killTarget: name,
		groups: cloneGroups(groups), collapsed: map[string]bool{}, dirs: map[string]string{},
		done: newDoneTracker(),
	}
}

func TestRailOnceEmptyFailureAndSuccess(t *testing.T) {
	origRunner, origTmuxPresent := tmux.Runner, tmuxPresent
	t.Cleanup(func() { tmux.Runner, tmuxPresent = origRunner, origTmuxPresent })
	tmuxPresent = func() bool { return true }

	t.Run("valid empty", func(t *testing.T) {
		tmux.Runner = func(args ...string) (string, error) { return "", nil }
		out, err := captureStdout(func() error { return cmdOnce(nil) })
		if err != nil || out != "" {
			t.Fatalf("rail once empty = %q, %v", out, err)
		}
	})

	t.Run("query failed", func(t *testing.T) {
		tmux.Runner = func(args ...string) (string, error) { return "", errors.New("tmux down") }
		out, err := captureStdout(func() error { return cmdOnce(nil) })
		if err == nil || out != "" || !strings.Contains(err.Error(), "tmux unavailable") {
			t.Fatalf("rail once failed = %q, %v", out, err)
		}
	})

	t.Run("success with marks schema", func(t *testing.T) {
		tmux.Runner = func(args ...string) (string, error) {
			switch args[0] {
			case "list-sessions":
				return "alpha\t$1\t0\t/tmp\t\n", nil
			case "list-windows":
				return "alpha\t$1\t@1\t1\tzsh\t1\t0\t0\t100\t\t\n", nil
			case "list-panes":
				return "alpha\t1\tzsh\n", nil
			default:
				return "", nil
			}
		}
		out, err := captureStdout(func() error { return cmdOnce([]string{"--marks"}) })
		if err != nil || out != "alpha|1|\n" {
			t.Fatalf("rail once marks = %q, %v", out, err)
		}
	})
}

func captureStdout(fn func() error) (string, error) {
	read, write, err := os.Pipe()
	if err != nil {
		return "", err
	}
	old := os.Stdout
	os.Stdout = write
	callErr := fn()
	_ = write.Close()
	os.Stdout = old
	bytes, readErr := io.ReadAll(read)
	_ = read.Close()
	if readErr != nil {
		return "", readErr
	}
	return string(bytes), callErr
}
