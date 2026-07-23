// Package tmux is the one seam between ghostmux and the tmux binary. Every
// other package talks to tmux only through here — never through a raw
// exec.Command("tmux", ...) of its own — so the whole tree can be pointed at
// a scratch server (GHOSTMUX_TMUX_ARGS) or a fake Runner in tests.
package tmux

import (
	"os"
	"os/exec"
	"strings"
)

// Runner executes a tmux command and returns stdout. Swapped in tests.
var Runner = func(args ...string) (string, error) {
	out, err := exec.Command("tmux", append(tmuxArgs(), args...)...).Output()
	return string(out), err
}

// tmuxArgs returns the whitespace-split GHOSTMUX_TMUX_ARGS prefix (e.g.
// "-L gm-test"), prepended to every invocation. It lets integration scripts
// point ghostmux at a scratch tmux server instead of the user's real one.
func tmuxArgs() []string {
	v := strings.TrimSpace(os.Getenv("GHOSTMUX_TMUX_ARGS"))
	if v == "" {
		return nil
	}
	return strings.Fields(v)
}

// Argv returns the full tmux argument vector including the GHOSTMUX_TMUX_ARGS
// prefix, for callers that must exec tmux directly (attach replaces the
// process, so it can't go through Runner).
func Argv(args ...string) []string {
	return append(tmuxArgs(), args...)
}

// ArgvString returns the GHOSTMUX_TMUX_ARGS prefix as a shell-ready string
// with a leading space (or "" when unset), for embedding in a nested tmux
// command run inside a pane (e.g. the viewport's `TMUX= tmux attach`).
func ArgvString() string {
	a := tmuxArgs()
	if len(a) == 0 {
		return ""
	}
	return " " + strings.Join(a, " ")
}

// Output runs args and returns stdout, or "" on error.
func Output(args ...string) string {
	out, err := Runner(args...)
	if err != nil {
		return ""
	}
	return out
}

// Lines runs args and returns stdout split into trimmed lines. Returns nil
// on error (no server, no such session, ...).
func Lines(args ...string) []string {
	out, err := Runner(args...)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(out), "\n")
}

// Run fires a tmux command and discards its output, keeping only the error.
func Run(args ...string) error {
	_, err := Runner(args...)
	return err
}
