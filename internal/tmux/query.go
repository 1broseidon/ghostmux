package tmux

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Session is one row of `tmux list-sessions`.
type Session struct {
	Name        string
	SessionID   string // #{session_id}: stable for this session instance
	Attached    bool
	Clients     int    // number of attached clients (Attached == Clients > 0)
	ClientTTY   string // #{client_tty} of an attached client, "" if none (D8 seam)
	Path        string // #{session_path}: where the session was started
	CurrentPath string // active window's #{pane_current_path}: live cwd when proven
	ViewOwner   string // #{@ghostmux_view_owner}: exact viewport ownership marker
}

// Window is one row of `tmux list-windows -a`, plus the current commands of
// its panes and the ghostmux "done" mark (D5).
type Window struct {
	Session    string
	SessionID  string // instance provenance copied directly from list-windows
	WindowID   string // #{window_id}: stable across links, renames, and indices
	Index      string
	Name       string
	Active     bool
	Bell       bool
	Activity   bool     // native flag, augmented per panel by the rail's ledger
	ActivityAt int64    // #{window_activity}: stable tmux activity timestamp
	Done       bool     // #{@ghostmux_done}
	PanePath   string   // #{pane_current_path} of this window's active pane
	PaneCmds   []string // #{pane_current_command} per pane, in pane order
}

// Snapshot is one all-or-nothing read of the tmux fleet. Query only publishes
// it after every required command, row parser, and relationship check succeeds.
type Snapshot struct {
	Sessions []Session
	Windows  []Window
}

const (
	sessionFormat = "#{session_name}\t#{session_id}\t#{session_attached}\t#{session_path}\t#{@ghostmux_view_owner}"
	clientFormat  = "#{client_session}\t#{client_tty}"
	windowFormat  = "#{session_name}\t#{session_id}\t#{window_id}\t#{window_index}\t#{window_name}\t#{window_active}\t#{window_bell_flag}\t#{window_activity_flag}\t#{window_activity}\t#{@ghostmux_done}\t#{pane_current_path}"
	paneFormat    = "#{session_name}\t#{window_index}\t#{pane_current_command}"
)

type clientRow struct {
	session string
	tty     string
}

type paneRow struct {
	session string
	window  string
	command string
}

type snapshotInconsistencyError struct{ detail string }

func (e snapshotInconsistencyError) Error() string {
	return "tmux snapshot inconsistent: " + e.detail
}

// Query reads sessions, clients, windows, and panes as one candidate. A
// backend with no server is an authoritative empty snapshot when that state is
// reported by the initial list-sessions call. Command and row-parse failures
// reject immediately. A relationship inconsistency can be ordinary tmux churn,
// so Query retries the complete four-command candidate once before failing.
func Query() (Snapshot, error) {
	for attempt := 0; attempt < 2; attempt++ {
		snapshot, err := queryCandidate()
		if err == nil {
			return snapshot, nil
		}
		var inconsistent snapshotInconsistencyError
		if !errors.As(err, &inconsistent) || attempt == 1 {
			return Snapshot{}, err
		}
	}
	return Snapshot{}, nil // unreachable
}

func queryCandidate() (Snapshot, error) {
	sessionOut, err := Runner("list-sessions", "-F", sessionFormat)
	if err != nil {
		if sessionOut == "" && isNoServerError(err) {
			return Snapshot{}, nil
		}
		return Snapshot{}, queryError("list-sessions", err)
	}
	sessions, err := parseSessions(sessionOut)
	if err != nil {
		return Snapshot{}, err
	}

	clientOut, err := Runner("list-clients", "-F", clientFormat)
	if err != nil {
		return Snapshot{}, queryError("list-clients", err)
	}
	clients, err := parseClients(clientOut)
	if err != nil {
		return Snapshot{}, err
	}

	windowOut, err := Runner("list-windows", "-a", "-F", windowFormat)
	if err != nil {
		return Snapshot{}, queryError("list-windows", err)
	}
	windows, err := parseWindows(windowOut)
	if err != nil {
		return Snapshot{}, err
	}

	paneOut, err := Runner("list-panes", "-a", "-F", paneFormat)
	if err != nil {
		return Snapshot{}, queryError("list-panes", err)
	}
	panes, err := parsePanes(paneOut)
	if err != nil {
		return Snapshot{}, err
	}

	if err := linkAndValidateSnapshot(sessions, clients, windows, panes); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Sessions: sessions, Windows: windows}, nil
}

// QuerySnapshot is a descriptive alias retained for callers and tests that
// adopted the boundary under that name while it was introduced.
func QuerySnapshot() (Snapshot, error) { return Query() }

// Sessions is the compatibility wrapper for callers that cannot yet surface a
// query error. Rail refresh and destructive validation use Query or
// ProbeSession directly and never depend on this error-discarding boundary.
func Sessions() []Session {
	snapshot, err := Query()
	if err != nil {
		return nil
	}
	return snapshot.Sessions
}

// Windows is the compatibility wrapper matching Sessions.
func Windows() []Window {
	snapshot, err := Query()
	if err != nil {
		return nil
	}
	return snapshot.Windows
}

// ProbeSessionInstance validates one exact name and returns its current stable
// tmux session ID. Query's candidate checks make replacement/churn during the
// multi-command read fail closed rather than attributing another instance's
// dependent rows to this name.
func ProbeSessionInstance(name string) (present bool, sessionID string, err error) {
	if name == "" {
		return false, "", fmt.Errorf("probe tmux session: empty name")
	}
	snapshot, err := Query()
	if err != nil {
		return false, "", err
	}
	for _, session := range snapshot.Sessions {
		if session.Name == name {
			return true, session.SessionID, nil
		}
	}
	return false, "", nil
}

// ProbeSession is the presence-only compatibility wrapper.
func ProbeSession(name string) (present bool, err error) {
	present, _, err = ProbeSessionInstance(name)
	return present, err
}

const instanceMismatchMarker = "__ghostmux_tmux_instance_mismatch__"

// KillSessionIfInstance atomically rechecks the armed name in the stable
// session-ID context and kills only the matching instance. The false branch
// emits a private marker so callers can cancel without changing saved state.
// This closes the remaining probe-to-kill rename/replacement window.
func KillSessionIfInstance(name, sessionID string) (killed bool, err error) {
	if name == "" || !validSessionID(sessionID) {
		return false, fmt.Errorf("kill tmux session instance: invalid identity %q (%q)", name, sessionID)
	}
	predicate := fmt.Sprintf(
		"#{&&:#{==:#{session_name},%s},#{==:#{session_id},%s}}",
		name, sessionID,
	)
	kill := fmt.Sprintf("kill-session -t '%s'", sessionID)
	mismatch := "display-message -p '" + instanceMismatchMarker + "'"
	out, err := Runner("if-shell", "-F", "-t", sessionID, predicate, kill, mismatch)
	if err != nil {
		return false, err
	}
	switch strings.TrimSuffix(strings.TrimSuffix(out, "\n"), "\r") {
	case "":
		return true, nil
	case instanceMismatchMarker:
		return false, nil
	default:
		return false, fmt.Errorf("kill tmux session instance: unexpected response %q", out)
	}
}

func parseSessions(out string) ([]Session, error) {
	lines := outputLines(out)
	sessions := make([]Session, 0, len(lines))
	for i, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != 5 || fields[0] == "" || !validSessionID(fields[1]) {
			return nil, malformedRow("list-sessions", i, line)
		}
		clients, err := strconv.Atoi(fields[2])
		if err != nil || clients < 0 {
			return nil, malformedRow("list-sessions", i, line)
		}
		sessions = append(sessions, Session{
			Name: fields[0], SessionID: fields[1], Attached: clients > 0, Clients: clients,
			Path: fields[3], ViewOwner: fields[4],
		})
	}
	return sessions, nil
}

func parseClients(out string) ([]clientRow, error) {
	lines := outputLines(out)
	clients := make([]clientRow, 0, len(lines))
	for i, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != 2 || fields[0] == "" {
			return nil, malformedRow("list-clients", i, line)
		}
		clients = append(clients, clientRow{session: fields[0], tty: fields[1]})
	}
	return clients, nil
}

func parseWindows(out string) ([]Window, error) {
	lines := outputLines(out)
	windows := make([]Window, 0, len(lines))
	for i, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != 11 || fields[0] == "" || !validSessionID(fields[1]) ||
			!validWindowID(fields[2]) || fields[3] == "" || fields[4] == "" {
			return nil, malformedRow("list-windows", i, line)
		}
		if index, err := strconv.Atoi(fields[3]); err != nil || index < 0 {
			return nil, malformedRow("list-windows", i, line)
		}
		active, ok := parseFlag(fields[5], false)
		if !ok {
			return nil, malformedRow("list-windows", i, line)
		}
		bell, ok := parseFlag(fields[6], false)
		if !ok {
			return nil, malformedRow("list-windows", i, line)
		}
		activity, ok := parseFlag(fields[7], false)
		if !ok {
			return nil, malformedRow("list-windows", i, line)
		}
		activityAt, err := strconv.ParseInt(fields[8], 10, 64)
		if err != nil || activityAt < 0 {
			return nil, malformedRow("list-windows", i, line)
		}
		done, ok := parseFlag(fields[9], true)
		if !ok {
			return nil, malformedRow("list-windows", i, line)
		}
		windows = append(windows, Window{
			Session: fields[0], SessionID: fields[1], WindowID: fields[2],
			Index: fields[3], Name: fields[4], Active: active, Bell: bell,
			Activity: activity, ActivityAt: activityAt, Done: done,
			PanePath: fields[10],
		})
	}
	return windows, nil
}

func validWindowID(id string) bool {
	if len(id) < 2 || id[0] != '@' {
		return false
	}
	for _, r := range id[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func parsePanes(out string) ([]paneRow, error) {
	lines := outputLines(out)
	panes := make([]paneRow, 0, len(lines))
	for i, line := range lines {
		fields := strings.Split(line, "\t")
		if len(fields) != 3 || fields[0] == "" || fields[1] == "" {
			return nil, malformedRow("list-panes", i, line)
		}
		if index, err := strconv.Atoi(fields[1]); err != nil || index < 0 {
			return nil, malformedRow("list-panes", i, line)
		}
		panes = append(panes, paneRow{session: fields[0], window: fields[1], command: fields[2]})
	}
	return panes, nil
}

func linkAndValidateSnapshot(sessions []Session, clients []clientRow, windows []Window, panes []paneRow) error {
	byName := make(map[string]*Session, len(sessions))
	byID := make(map[string]*Session, len(sessions))
	for i := range sessions {
		session := &sessions[i]
		if _, exists := byName[session.Name]; exists {
			return inconsistentSnapshot("duplicate session name %q", session.Name)
		}
		if _, exists := byID[session.SessionID]; exists {
			return inconsistentSnapshot("duplicate session id %q", session.SessionID)
		}
		byName[session.Name] = session
		byID[session.SessionID] = session
	}

	clientCounts := make(map[string]int, len(sessions))
	for _, client := range clients {
		session := byName[client.session]
		if session == nil {
			return inconsistentSnapshot("client references unknown session %q", client.session)
		}
		clientCounts[session.SessionID]++
		if session.ClientTTY == "" {
			session.ClientTTY = client.tty
		}
	}
	for i := range sessions {
		if got := clientCounts[sessions[i].SessionID]; got != sessions[i].Clients {
			return inconsistentSnapshot(
				"session %q expected %d client rows, got %d",
				sessions[i].Name, sessions[i].Clients, got,
			)
		}
	}

	windowByKey := make(map[string]*Window, len(windows))
	windowCounts := make(map[string]int, len(sessions))
	for i := range windows {
		window := &windows[i]
		session := byName[window.Session]
		if session == nil {
			return inconsistentSnapshot("window references unknown session %q", window.Session)
		}
		if window.SessionID != session.SessionID {
			return inconsistentSnapshot(
				"window %s:%s has session id %q, want %q",
				window.Session, window.Index, window.SessionID, session.SessionID,
			)
		}
		key := window.Session + ":" + window.Index
		if _, exists := windowByKey[key]; exists {
			return inconsistentSnapshot("duplicate window key %q", key)
		}
		windowByKey[key] = window
		windowCounts[session.SessionID]++
		if window.Active && window.PanePath != "" {
			session.CurrentPath = window.PanePath
		}
	}
	for i := range sessions {
		if windowCounts[sessions[i].SessionID] == 0 {
			return inconsistentSnapshot("session %q has no window rows", sessions[i].Name)
		}
	}

	paneCounts := make(map[string]int, len(windows))
	for _, pane := range panes {
		key := pane.session + ":" + pane.window
		window := windowByKey[key]
		if window == nil {
			return inconsistentSnapshot("pane references unknown window %q", key)
		}
		window.PaneCmds = append(window.PaneCmds, pane.command)
		paneCounts[key]++
	}
	for i := range windows {
		key := windows[i].Session + ":" + windows[i].Index
		if paneCounts[key] == 0 {
			return inconsistentSnapshot("window %q has no pane rows", key)
		}
	}
	return nil
}

func inconsistentSnapshot(format string, args ...any) error {
	return snapshotInconsistencyError{detail: fmt.Sprintf(format, args...)}
}

// outputLines removes line terminators only. In particular it does not trim
// tabs or spaces, because a final empty format field is still part of a row.
func outputLines(out string) []string {
	if out == "" {
		return nil
	}
	// tmux terminates nonempty output with one newline. Remove that terminator
	// only; extra blank rows remain and are rejected by the strict parsers.
	out = strings.TrimSuffix(out, "\n")
	out = strings.TrimSuffix(out, "\r")
	lines := strings.Split(out, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return lines
}

func parseFlag(value string, emptyOK bool) (bool, bool) {
	switch value {
	case "0":
		return false, true
	case "1":
		return true, true
	case "":
		return false, emptyOK
	default:
		return false, false
	}
}

func malformedRow(command string, index int, line string) error {
	return fmt.Errorf("tmux %s: malformed row %d: %q", command, index+1, line)
}

func queryError(command string, err error) error {
	return fmt.Errorf("tmux %s: %w", command, err)
}

// isNoServerError recognizes the normal empty-server diagnostics emitted by
// supported tmux builds. It is intentionally narrow: arbitrary exit status 1
// errors remain outages.
func isNoServerError(err error) bool {
	text := strings.ToLower(strings.TrimSpace(commandErrorText(err)))
	if strings.Contains(text, "no server running on ") {
		return true
	}
	return strings.Contains(text, "error connecting to ") &&
		strings.Contains(text, "no such file or directory")
}

func commandErrorText(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
		text += "\n" + string(exitErr.Stderr)
	}
	return text
}

// SetDone sets or clears the @ghostmux_done user option on a window (D5).
func SetDone(sess, index string, on bool) {
	value := "0"
	if on {
		value = "1"
	}
	Run("set-option", "-w", "-t", sess+":"+index, "@ghostmux_done", value)
}

// PaneDead reports whether a pane (by %id) is dead.
func PaneDead(paneID string) bool {
	return strings.TrimSpace(Output("display-message", "-p", "-t", paneID, "#{pane_dead}")) == "1"
}
