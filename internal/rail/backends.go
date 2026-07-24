package rail

// Multi-backend prototype (v0.2.0): the rail lists sessions from other
// multiplexers beside tmux, honestly degraded to what each backend can
// prove. Zellij first: names and EXITED state are observable from
// `zellij list-sessions`; attached-ness and window trees are not — so
// zellij rows carry no attached dot, no tree, no gutter marks. Backends
// earn features by proving data, never by faked parity.

import (
	"os/exec"
	"strings"
)

// auxSession is a session on a non-tmux backend.
type auxSession struct {
	backend string // "zellij"
	name    string
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
		// Format: "name [Created 2h ago]" — EXITED sessions are dead
		// (resurrectable) and "No active zellij sessions found." is prose.
		if line == "" || strings.Contains(line, "EXITED") || strings.HasPrefix(line, "No active") {
			continue
		}
		name, _, ok := strings.Cut(line, " ")
		if !ok {
			name = line
		}
		if name != "" {
			sessions = append(sessions, auxSession{backend: "zellij", name: name})
		}
	}
	return sessions
}

// auxRows renders backend sessions as flat rail rows: name + backend as the
// dim suffix, marks only for what the backend proves (zellij: the ▸ lock).
func auxRows(aux []auxSession, v viewState) []railRow {
	var rows []railRow
	for _, s := range aux {
		rows = append(rows, railRow{
			depth: 0, flat: true, label: s.name, sess: s.name,
			backend: s.backend, cmd: s.backend,
			inView: v.lockBackend == s.backend && v.lockSess == s.name,
		})
	}
	return rows
}

// killAux kills a session on a non-tmux backend; injectable for tests.
var killAux = func(backend, name string) error {
	switch backend {
	case "zellij":
		return exec.Command("zellij", "kill-session", name).Run()
	}
	return nil
}
