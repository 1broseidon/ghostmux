package rail

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

// fakeRunner dispatches canned tmux output by subcommand (args[0]), so
// railRows (and the tmux queries it drives) can be exercised without a real
// tmux binary.
func fakeRunner(t *testing.T, outputs map[string]string) func(args ...string) (string, error) {
	t.Helper()
	return func(args ...string) (string, error) {
		if len(args) == 0 {
			t.Fatalf("fakeRunner: called with no args")
		}
		out, ok := outputs[args[0]]
		if !ok {
			// Subcommand not stubbed: behave like "no data" (mirrors a real
			// tmux server that simply has none, e.g. no clients attached).
			return "", nil
		}
		return out, nil
	}
}

func withFakeRunner(t *testing.T, outputs map[string]string) {
	t.Helper()
	orig := tmux.Runner
	tmux.Runner = fakeRunner(t, outputs)
	t.Cleanup(func() { tmux.Runner = orig })
}

func TestRailRows(t *testing.T) {
	cases := []struct {
		name    string
		hub     string
		view    ViewState
		outputs map[string]string
		want    []railRow
	}{
		{
			name: "sessions and windows parsed",
			outputs: map[string]string{
				"list-sessions": "alpha\t0\nbeta\t1\n",
				"list-windows": "alpha\t1\tvim\t1\t0\t0\t0\n" +
					"alpha\t2\tshell\t0\t0\t0\t0\n" +
					"beta\t1\tzsh\t1\t0\t0\t0\n",
			},
			want: []railRow{
				{depth: 0, label: "alpha", sess: "alpha", attached: false},
				{depth: 1, isWin: true, label: "1:vim", sess: "alpha", window: "1", active: true},
				{depth: 1, isWin: true, label: "2:shell", sess: "alpha", window: "2", active: false},
				// single-window session: one flat row, window marks inherited
				{depth: 0, flat: true, label: "beta", sess: "beta", window: "1", attached: true, active: true},
			},
		},
		{
			name: "hub session excluded by name",
			hub:  "hub",
			outputs: map[string]string{
				"list-sessions": "hub\t1\nwork\t0\n",
				"list-windows": "hub\t1\trail\t1\t0\t0\t0\n" +
					"work\t1\tzsh\t1\t0\t0\t0\n",
			},
			want: []railRow{
				{depth: 0, flat: true, label: "work", sess: "work", window: "1", active: true},
			},
		},
		{
			name: "attached flag reflects session_attached",
			outputs: map[string]string{
				"list-sessions": "solo\t2\n",
				"list-windows":  "solo\t1\tzsh\t1\t0\t0\t0\n",
			},
			want: []railRow{
				{depth: 0, flat: true, label: "solo", sess: "solo", window: "1", attached: true, active: true},
			},
		},
		{
			name: "window marks from bell, activity and done flags aggregate to the session",
			outputs: map[string]string{
				"list-sessions": "s\t0\n",
				"list-windows": "s\t1\tone\t1\t1\t0\t0\n" + // bell only
					"s\t2\ttwo\t0\t0\t1\t0\n" + // activity only
					"s\t3\tthree\t0\t0\t0\t1\n" + // done only
					"s\t4\tfour\t0\t0\t0\t0\n", // neither
			},
			// Session aggregates the single highest-priority mark (bell here).
			want: []railRow{
				{depth: 0, label: "s", sess: "s", bell: true},
				{depth: 1, isWin: true, label: "1:one", sess: "s", window: "1", bell: true, active: true},
				{depth: 1, isWin: true, label: "2:two", sess: "s", window: "2", act: true},
				{depth: 1, isWin: true, label: "3:three", sess: "s", window: "3", done: true},
				{depth: 1, isWin: true, label: "4:four", sess: "s", window: "4"},
			},
		},
		{
			name: "inView mark on locked window and its session",
			view: ViewState{Sess: "s", Win: "2"},
			outputs: map[string]string{
				"list-sessions": "s\t0\n",
				"list-windows": "s\t1\tone\t1\t0\t0\t0\n" +
					"s\t2\ttwo\t0\t0\t0\t0\n",
			},
			want: []railRow{
				{depth: 0, label: "s", sess: "s", inView: true},
				{depth: 1, isWin: true, label: "1:one", sess: "s", window: "1", active: true},
				{depth: 1, isWin: true, label: "2:two", sess: "s", window: "2", inView: true},
			},
		},
		{
			name: "whole-session lock marks the active window inView",
			view: ViewState{Sess: "s", Win: ""},
			outputs: map[string]string{
				"list-sessions": "s\t0\n",
				"list-windows": "s\t1\tone\t1\t0\t0\t0\n" +
					"s\t2\ttwo\t0\t0\t0\t0\n",
			},
			want: []railRow{
				{depth: 0, label: "s", sess: "s", inView: true},
				{depth: 1, isWin: true, label: "1:one", sess: "s", window: "1", active: true, inView: true},
				{depth: 1, isWin: true, label: "2:two", sess: "s", window: "2"},
			},
		},
		{
			name:    "no sessions",
			outputs: map[string]string{},
			want:    nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withFakeRunner(t, tc.outputs)
			got := railRows(tc.hub, tc.view)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("railRows(%q) =\n  %#v\nwant\n  %#v", tc.hub, got, tc.want)
			}
		})
	}
}

func TestRailRowGutterPriority(t *testing.T) {
	cases := []struct {
		name string
		row  railRow
		want string
	}{
		{"none", railRow{}, ""},
		{"bell", railRow{bell: true}, "●"},
		{"done", railRow{done: true}, "✓"},
		{"activity", railRow{act: true}, "~"},
		{"inView", railRow{inView: true}, "▸"},
		{"bell beats done", railRow{bell: true, done: true}, "●✓"},
		{"all four capped at two, priority order", railRow{bell: true, done: true, act: true, inView: true}, "●✓"},
		{"done and activity", railRow{done: true, act: true}, "✓~"},
		{"activity and inView", railRow{act: true, inView: true}, "~▸"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.row.gutter(); got != tc.want {
				t.Errorf("gutter() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRailRowPlain(t *testing.T) {
	cases := []struct {
		name string
		row  railRow
		want string
	}{
		{
			name: "session row",
			row:  railRow{depth: 0, label: "alpha"},
			want: "    alpha",
		},
		{
			name: "active window row",
			row:  railRow{depth: 1, isWin: true, label: "1:vim", active: true},
			want: "  *   1:vim",
		},
		{
			name: "window row with bell gutter",
			row:  railRow{depth: 1, isWin: true, label: "1:vim", bell: true},
			want: "   ●  1:vim",
		},
		{
			name: "window row with done gutter",
			row:  railRow{depth: 1, isWin: true, label: "1:vim", done: true},
			want: "   ✓  1:vim",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.row.plain(); got != tc.want {
				t.Errorf("plain() = %q, want %q", got, tc.want)
			}
		})
	}
}

// setDoneCalls captures every SetDone (set-option -w ... @ghostmux_done)
// invocation, in "sess:index=value" form, for the transition tests.
func setDoneCalls(seen *[]string) func(args ...string) (string, error) {
	return func(args ...string) (string, error) {
		if len(args) >= 6 && args[0] == "set-option" && args[4] == "@ghostmux_done" {
			*seen = append(*seen, args[3]+"="+args[5])
		}
		return "", nil
	}
}

func win(sess, index string, cmds ...string) tmux.Window {
	return tmux.Window{Session: sess, Index: index, PaneCmds: cmds}
}

func TestDoneTrackerTransitions(t *testing.T) {
	// A suppress predicate keyed by "sess:index"; membership means viewed or
	// attached (done should be withheld).
	suppressed := func(keys ...string) func(sess, window string) bool {
		set := map[string]bool{}
		for _, k := range keys {
			set[k] = true
		}
		return func(sess, window string) bool { return set[sess+":"+window] }
	}

	cases := []struct {
		name     string
		hub      string
		suppress func(string, string) bool
		prime    []tmux.Window // first observe (seeds last-seen commands)
		then     []tmux.Window // second observe (drives transitions)
		want     []string      // expected SetDone calls on the second observe
	}{
		{
			name:  "cmd to shell while unattended sets done",
			prime: []tmux.Window{win("work", "1", "node")},
			then:  []tmux.Window{win("work", "1", "zsh")},
			want:  []string{"work:1=1"},
		},
		{
			name:     "cmd to shell while viewed is not set",
			suppress: suppressed("work:1"),
			prime:    []tmux.Window{win("work", "1", "node")},
			then:     []tmux.Window{win("work", "1", "zsh")},
			want:     nil,
		},
		{
			name:     "cmd to shell while attached is not set",
			suppress: suppressed("work:1"),
			prime:    []tmux.Window{win("work", "1", "python")},
			then:     []tmux.Window{win("work", "1", "bash")},
			want:     nil,
		},
		{
			name:  "shell to cmd changes nothing",
			prime: []tmux.Window{win("work", "1", "zsh")},
			then:  []tmux.Window{win("work", "1", "vim")},
			want:  nil,
		},
		{
			name:  "shell to shell changes nothing",
			prime: []tmux.Window{win("work", "1", "bash")},
			then:  []tmux.Window{win("work", "1", "zsh")},
			want:  nil,
		},
		{
			name:  "first observe never transitions",
			prime: nil,
			then:  []tmux.Window{win("work", "1", "zsh")},
			want:  nil,
		},
		{
			name:  "hub session is never tracked",
			hub:   "hub",
			prime: []tmux.Window{win("hub", "1", "node")},
			then:  []tmux.Window{win("hub", "1", "zsh")},
			want:  nil,
		},
		{
			name:  "only the transitioning pane marks done",
			prime: []tmux.Window{win("work", "1", "node", "zsh")},
			then:  []tmux.Window{win("work", "1", "zsh", "zsh")},
			want:  []string{"work:1=1"},
		},
		{
			name:  "transitions across two windows",
			prime: []tmux.Window{win("a", "1", "make"), win("b", "2", "go")},
			then:  []tmux.Window{win("a", "1", "fish"), win("b", "2", "sh")},
			want:  []string{"a:1=1", "b:2=1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen []string
			orig := tmux.Runner
			tmux.Runner = setDoneCalls(&seen)
			t.Cleanup(func() { tmux.Runner = orig })

			suppress := tc.suppress
			if suppress == nil {
				suppress = func(string, string) bool { return false }
			}

			dt := newDoneTracker()
			dt.observe(tc.prime, tc.hub, suppress) // seed; ignore its calls
			seen = nil
			dt.observe(tc.then, tc.hub, suppress)

			sort.Strings(seen)
			sort.Strings(tc.want)
			if !reflect.DeepEqual(seen, tc.want) {
				t.Errorf("SetDone calls = %v, want %v", seen, tc.want)
			}
		})
	}
}

func TestDoneTrackerForgetsVanishedPanes(t *testing.T) {
	orig := tmux.Runner
	var seen []string
	tmux.Runner = setDoneCalls(&seen)
	t.Cleanup(func() { tmux.Runner = orig })
	nope := func(string, string) bool { return false }

	dt := newDoneTracker()
	dt.observe([]tmux.Window{win("work", "1", "node")}, "", nope) // seed node
	dt.observe(nil, "", nope)                                     // window gone: forget it
	seen = nil
	// The pane reappears already at a shell. Because the stale "node" was
	// forgotten, this must NOT read as a node→zsh transition.
	dt.observe([]tmux.Window{win("work", "1", "zsh")}, "", nope)

	if len(seen) != 0 {
		t.Errorf("SetDone called after pane churn: %v", seen)
	}
}

func TestClearViewedDone(t *testing.T) {
	// Viewing a window clears its @ghostmux_done: directly for a window row,
	// and for a session row via its active window.
	rows := []railRow{
		{depth: 0, label: "work", sess: "work"},
		{depth: 1, isWin: true, label: "1:one", sess: "work", window: "1", active: false},
		{depth: 1, isWin: true, label: "2:two", sess: "work", window: "2", active: true},
	}
	cases := []struct {
		name string
		row  railRow
		want []string // expected SetDone calls
	}{
		{"window row clears itself", rows[1], []string{"work:1=0"}},
		{"session row clears active window", rows[0], []string{"work:2=0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen []string
			orig := tmux.Runner
			tmux.Runner = setDoneCalls(&seen)
			t.Cleanup(func() { tmux.Runner = orig })

			m := railModel{rows: rows}
			m.clearViewedDone(tc.row)
			if !reflect.DeepEqual(seen, tc.want) {
				t.Errorf("SetDone calls = %v, want %v", seen, tc.want)
			}
		})
	}
}

func TestSuppressDone(t *testing.T) {
	m := railModel{
		attached: map[string]bool{"att": true},
		vp:       &fakeViewport{lock: ViewState{Sess: "view", Win: "2"}},
	}
	cases := []struct {
		sess, window string
		want         bool
	}{
		{"att", "1", true},    // attached elsewhere
		{"view", "2", true},   // exact window in the viewport
		{"view", "3", false},  // different window of a window-locked session
		{"other", "1", false}, // unrelated
	}
	for _, tc := range cases {
		if got := m.suppressDone(tc.sess, tc.window); got != tc.want {
			t.Errorf("suppressDone(%q,%q) = %v, want %v", tc.sess, tc.window, got, tc.want)
		}
	}
	// whole-session lock suppresses every window of that session
	m.vp = &fakeViewport{lock: ViewState{Sess: "whole"}}
	if !m.suppressDone("whole", "9") {
		t.Errorf("whole-session lock should suppress any window")
	}
}

func TestAnyBell(t *testing.T) {
	if anyBell([]railRow{{done: true}, {act: true}}) {
		t.Errorf("anyBell = true with no bell rows")
	}
	if !anyBell([]railRow{{done: true}, {bell: true}}) {
		t.Errorf("anyBell = false with a bell row")
	}
}

func TestGHOSTMUXTmuxArgsPrepended(t *testing.T) {
	// Belt-and-braces: exercised properly in internal/tmux, but confirm rail's
	// data layer goes through the injectable Runner rather than a direct
	// exec.Command, so GHOSTMUX_TMUX_ARGS (set by test/hub_test.sh) always
	// reaches it.
	var seen []string
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) {
		seen = append(seen, strings.Join(args, " "))
		return "", nil
	}
	t.Cleanup(func() { tmux.Runner = orig })

	railRows("", ViewState{})

	if len(seen) == 0 {
		t.Fatal("railRows made no tmux.Runner calls")
	}
}
