package rail

// Multi-backend prototype (v0.2.0): the rail lists sessions from other
// multiplexers beside tmux, honestly degraded to what each backend can
// prove. Zellij first: names and EXITED state are observable from
// `zellij list-sessions`; attached-ness and window trees are not — so
// zellij rows carry no attached dot, no tree, no gutter marks. Backends
// earn features by proving data, never by faked parity.

import (
	"fmt"
	"os/exec"
	"strings"
)

// auxSession is a session on a non-tmux backend.
type auxSession struct {
	backend string // "zellij"
	name    string
	exited  bool // zellij lists it as "(EXITED - attach to resurrect)"
}

// zellijList runs `zellij list-sessions --no-formatting`; injectable for
// tests, like tmux.Runner.
var zellijList = func() (string, error) {
	out, err := exec.Command("zellij", "list-sessions", "--no-formatting").Output()
	return string(out), err
}

// zellijPresent reports whether zellij exists on this box; probed once.
var zellijPresent = func() bool {
	_, err := exec.LookPath("zellij")
	return err == nil
}()

// auxSessions polls every non-tmux backend present on the box.
func auxSessions() []auxSession {
	if !zellijPresent {
		return nil
	}
	out, err := zellijList()
	if err != nil {
		return nil
	}
	var sessions []auxSession
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		// Format: "name [Created 2h ago]", with "(EXITED - attach to
		// resurrect)" appended for a session zellij has serialized but is not
		// running. That is zellij's own word for a ghost, so we relay it
		// instead of dropping the row: the name still exists, and attaching
		// really does bring it back. "No active zellij sessions found." is
		// prose, not a session.
		if line == "" || strings.HasPrefix(line, "No active") {
			continue
		}
		exited := strings.Contains(line, "EXITED")
		name, _, ok := strings.Cut(line, " ")
		if !ok {
			name = line
		}
		if name != "" {
			sessions = append(sessions, auxSession{backend: "zellij", name: name, exited: exited})
		}
	}
	return sessions
}

// auxRows renders backend sessions as flat rail rows: name + backend as the
// dim suffix, marks only for what the backend proves (zellij: the ▸ lock).
func auxRows(aux []auxSession, v ViewState) []railRow {
	var rows []railRow
	for _, s := range aux {
		rows = append(rows, railRow{
			depth: 0, flat: true, label: s.name, sess: s.name,
			backend: s.backend, cmd: s.backend, ghost: s.exited,
			// An exited session cannot be the one in the viewport, and a ghost
			// carries no marks anyway — so the lock only ever lights a live row.
			inView: !s.exited && v.Backend == s.backend && v.Sess == s.name,
		})
	}
	return rows
}

// backends returns the multiplexers available to create on, tmux first ("").
// The `n` prompt only offers a choice when there is genuinely one to make.
func backends() []string {
	out := []string{""}
	if !tmuxPresent() {
		out = out[:0]
	}
	if zellijPresent {
		out = append(out, "zellij")
	}
	if len(out) == 0 {
		out = []string{""} // nothing detected: tmux is still the honest default
	}
	return out
}

// tmuxPresent reports whether tmux is installed; injectable for tests.
var tmuxPresent = func() bool { _, err := exec.LookPath("tmux"); return err == nil }

// createAux creates a session on a non-tmux backend without attaching to it —
// the rail's viewport does the attaching. Injectable for tests.
var createAux = func(backend, name string) error {
	switch backend {
	case "zellij":
		return exec.Command("zellij", "attach", "--create-background", name).Run()
	}
	return fmt.Errorf("unknown backend %q", backend)
}

// AuxSessionExists reports whether a non-tmux backend still lists sess. It is
// the loop guard for a hosting frame's heal: a viewport whose child died
// because its session was killed must go idle, never re-attach forever.
func AuxSessionExists(backend, sess string) bool {
	for _, a := range auxSessions() {
		if a.backend == backend && a.name == sess {
			return true
		}
	}
	return false
}

// killAux kills a session on a non-tmux backend; injectable for tests.
var killAux = func(backend, name string) error {
	switch backend {
	case "zellij":
		return exec.Command("zellij", "kill-session", name).Run()
	}
	return nil
}

// deleteAux removes a backend's serialized session — the dead half of the
// fleet, not a running process. It is what `x` means on a zellij ghost: kill
// has nothing left to kill, so the honest verb is delete. Injectable for tests.
var deleteAux = func(backend, name string) error {
	switch backend {
	case "zellij":
		return exec.Command("zellij", "delete-session", "--force", name).Run()
	}
	return fmt.Errorf("unknown backend %q", backend)
}

// HasBackend reports whether a non-tmux backend is installed, so a frame never
// advertises a key that could only produce an error.
func HasBackend(name string) bool {
	switch name {
	case "zellij":
		return zellijPresent
	case "", "tmux":
		return tmuxPresent()
	}
	return false
}
