package tmux

import "strings"

// Session is one row of `tmux list-sessions`.
type Session struct {
	Name      string
	Attached  bool
	ClientTTY string // #{client_tty} of an attached client, "" if none (D8 seam)
}

// Window is one row of `tmux list-windows -a`, plus the current commands of
// its panes and the ghostmux "done" mark (D5).
type Window struct {
	Session  string
	Index    string
	Name     string
	Active   bool
	Bell     bool
	Activity bool
	Done     bool     // #{@ghostmux_done}
	PaneCmds []string // #{pane_current_command} per pane, in pane order
}

// Sessions lists every tmux session.
func Sessions() []Session {
	ttys := clientTTYs()
	var sessions []Session
	for _, line := range Lines("list-sessions", "-F", "#{session_name}\t#{session_attached}") {
		f := strings.Split(line, "\t")
		if len(f) < 2 {
			continue
		}
		sessions = append(sessions, Session{
			Name:      f[0],
			Attached:  f[1] != "0",
			ClientTTY: ttys[f[0]],
		})
	}
	return sessions
}

// clientTTYs maps session name to the tty of an attached client. One
// list-clients call is the cheapest correct source for D8's termtile seam.
func clientTTYs() map[string]string {
	ttys := map[string]string{}
	for _, line := range Lines("list-clients", "-F", "#{client_session}\t#{client_tty}") {
		f := strings.Split(line, "\t")
		if len(f) < 2 {
			continue
		}
		if _, ok := ttys[f[0]]; !ok {
			ttys[f[0]] = f[1]
		}
	}
	return ttys
}

// Windows lists every window of every session (one -a call), each carrying
// its panes' current commands (a second -a call, keyed by session:index).
func Windows() []Window {
	panes := paneCommands()
	var windows []Window
	for _, line := range Lines("list-windows", "-a", "-F",
		"#{session_name}\t#{window_index}\t#{window_name}\t#{window_active}\t#{window_bell_flag}\t#{window_activity_flag}\t#{@ghostmux_done}") {
		f := strings.Split(line, "\t")
		// @ghostmux_done is the last field and is empty when unset; Lines'
		// whitespace-trim can drop that trailing empty field (and its tab) on
		// the final row, so accept a 6-field row with done defaulting to false.
		if len(f) < 6 {
			continue
		}
		done := false
		if len(f) >= 7 {
			done = f[6] == "1"
		}
		windows = append(windows, Window{
			Session:  f[0],
			Index:    f[1],
			Name:     f[2],
			Active:   f[3] != "0",
			Bell:     f[4] != "0",
			Activity: f[5] != "0",
			Done:     done,
			PaneCmds: panes[f[0]+":"+f[1]],
		})
	}
	return windows
}

// paneCommands maps "session:window_index" to its panes' current commands.
func paneCommands() map[string][]string {
	panes := map[string][]string{}
	for _, line := range Lines("list-panes", "-a", "-F",
		"#{session_name}\t#{window_index}\t#{pane_current_command}") {
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			continue
		}
		key := f[0] + ":" + f[1]
		panes[key] = append(panes[key], f[2])
	}
	return panes
}

// SetDone sets or clears the @ghostmux_done user option on a window (D5).
func SetDone(sess, index string, on bool) {
	v := "0"
	if on {
		v = "1"
	}
	Run("set-option", "-w", "-t", sess+":"+index, "@ghostmux_done", v)
}

// PaneDead reports whether a pane (by %id) is dead.
func PaneDead(paneID string) bool {
	return strings.TrimSpace(Output("display-message", "-p", "-t", paneID, "#{pane_dead}")) == "1"
}
