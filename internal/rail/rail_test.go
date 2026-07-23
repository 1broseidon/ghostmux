package rail

import (
	"reflect"
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
				{depth: 1, label: "1:vim", sess: "alpha", window: "1", active: true},
				{depth: 1, label: "2:shell", sess: "alpha", window: "2", active: false},
				{depth: 0, label: "beta", sess: "beta", attached: true},
				{depth: 1, label: "1:zsh", sess: "beta", window: "1", active: true},
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
				{depth: 0, label: "work", sess: "work", attached: false},
				{depth: 1, label: "1:zsh", sess: "work", window: "1", active: true},
			},
		},
		{
			name: "attached flag reflects session_attached",
			outputs: map[string]string{
				"list-sessions": "solo\t2\n",
				"list-windows":  "solo\t1\tzsh\t1\t0\t0\t0\n",
			},
			want: []railRow{
				{depth: 0, label: "solo", sess: "solo", attached: true},
				{depth: 1, label: "1:zsh", sess: "solo", window: "1", active: true},
			},
		},
		{
			name: "gutter marks from bell and activity flags",
			outputs: map[string]string{
				"list-sessions": "s\t0\n",
				"list-windows": "s\t1\tone\t1\t1\t0\t0\n" + // bell only
					"s\t2\ttwo\t0\t0\t1\t0\n" + // activity only
					"s\t3\tthree\t0\t1\t1\t0\n" + // bell + activity
					"s\t4\tfour\t0\t0\t0\t0\n", // neither
			},
			want: []railRow{
				{depth: 0, label: "s", sess: "s"},
				{depth: 1, label: "1:one", sess: "s", window: "1", gutter: "●", active: true},
				{depth: 1, label: "2:two", sess: "s", window: "2", gutter: "~"},
				{depth: 1, label: "3:three", sess: "s", window: "3", gutter: "●~"},
				{depth: 1, label: "4:four", sess: "s", window: "4"},
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
			got := railRows(tc.hub)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("railRows(%q) =\n  %#v\nwant\n  %#v", tc.hub, got, tc.want)
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
			row:  railRow{depth: 1, label: "1:vim", active: true},
			want: "  *   1:vim",
		},
		{
			name: "window row with bell gutter",
			row:  railRow{depth: 1, label: "1:vim", gutter: "●"},
			want: "   ●  1:vim",
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

	railRows("")

	if len(seen) == 0 {
		t.Fatal("railRows made no tmux.Runner calls")
	}
}
