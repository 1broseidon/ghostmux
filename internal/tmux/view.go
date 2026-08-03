package tmux

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// ViewPrefix is reserved for transient grouped sessions created by a
	// ghostmux viewport. The prefix alone does not establish ownership: a
	// session is an owned view only when its owner option matches its name.
	ViewPrefix = "gm-view-"

	// WallPrefix is reserved for the owned composite session a group wall
	// creates. It is tagged with the exact same option and version as
	// ViewPrefix shadows — only the prefix (and therefore the name) differs —
	// so ownership, the predicate, and cleanup are shared unchanged.
	WallPrefix = "gm-wall-"

	viewOwnerOption  = "@ghostmux_view_owner"
	viewOwnerVersion = "v1:"
)

var fallbackNonce atomic.Uint64

// ViewRef is the capability a viewport keeps for one grouped shadow. Commands
// use SessionID, which is stable across renames; Name and Owner are retained so
// cleanup can prove that the ID still denotes exactly the session we created.
type ViewRef struct {
	Name      string
	SessionID string
	Owner     string
}

// NewViewNonce returns a collision-resistant identity for one panel. It is
// combined with that panel's monotonically increasing attach sequence, so two
// panels and two attaches from one panel never intentionally reuse a name.
func NewViewNonce() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	// crypto/rand failure is exceptional, but viewport creation still needs a
	// non-repeating identity. Mix process/time with an in-process sequence.
	return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" +
		strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(fallbackNonce.Add(1), 36)
}

// ViewIdentity combines a panel nonce and per-attach sequence into the part of
// a shadow name following ViewPrefix.
func ViewIdentity(panelNonce string, sequence uint64) string {
	return panelNonce + "-" + strconv.FormatUint(sequence, 36)
}

// CreateView creates and configures one detached grouped shadow. Ownership is
// tagged before any other option is changed. A failure before that tag returns
// no capability and leaves the unowned session untouched. A later failure
// returns the tagged capability with the error so its caller can retain and
// retry ownership-checked cleanup. destroy-unattached is enabled by
// AttachViewArgv in the same tmux command queue as attach: enabling it while
// this detached session has no client would make tmux destroy it before the PTY
// can attach.
func CreateView(target, win, identity string) (ViewRef, error) {
	if target == "" {
		return ViewRef{}, fmt.Errorf("create view: empty target")
	}
	if !validViewIdentity(identity) {
		return ViewRef{}, fmt.Errorf("create view: invalid identity %q", identity)
	}

	name := ViewPrefix + identity
	owner := viewOwnerVersion + name
	out, err := Runner(
		"new-session", "-d", "-P", "-F", "#{session_id}",
		"-s", name, "-t", "="+target,
	)
	if err != nil {
		return ViewRef{}, fmt.Errorf("create view %q: %w", name, err)
	}

	sessionID := strings.TrimSpace(out)
	if !validSessionID(sessionID) {
		// Creation may have succeeded, but without the stable ID there is no
		// safe session to tag or clean. Deliberately leave it unowned.
		return ViewRef{}, fmt.Errorf("create view %q: invalid session id %q", name, sessionID)
	}
	ref := ViewRef{Name: name, SessionID: sessionID, Owner: owner}

	if err := Run("set-option", "-t", sessionID, viewOwnerOption, owner); err != nil {
		// The ownership write itself did not succeed, so automatic cleanup is
		// not allowed to infer ownership from the name.
		return ViewRef{}, fmt.Errorf("tag view %q: %w", name, err)
	}
	if err := Run("set-option", "-t", sessionID, "status-left", "["+target+"] "); err != nil {
		return ref, fmt.Errorf("configure view %q status: %w", name, err)
	}
	if win != "" {
		if err := Run("select-window", "-t", sessionID+":"+win); err != nil {
			return ref, fmt.Errorf("select view %q window %q: %w", name, win, err)
		}
	}
	return ref, nil
}

// AttachViewArgv returns the tmux client command for an owned shadow. The
// ownership predicate is evaluated in the stable session-ID context when tmux
// executes it, and gates the entire option-and-attach queue. A reused, renamed,
// or retagged ID therefore becomes a no-op: it is neither configured nor
// attached. Within the true branch, destroy-unattached and attach share one
// queue so the option cannot destroy the detached shadow before its first
// client arrives.
func AttachViewArgv(ref ViewRef) []string {
	if !validViewRef(ref) {
		return nil
	}
	attach := fmt.Sprintf(
		"set-option -t '%s' destroy-unattached on ; attach-session -t '%s'",
		ref.SessionID, ref.SessionID,
	)
	return append([]string{"tmux"}, Argv(
		"if-shell", "-F", "-t", ref.SessionID,
		ownedViewPredicate(ref), attach, "",
	)...)
}

// AttachSessionArgv returns the direct tmux client command used when the
// target has no outside client and therefore needs no grouped shadow.
func AttachSessionArgv(sess, win string) []string {
	if sess == "" {
		return nil
	}
	argv := append([]string{"tmux"}, Argv("attach-session", "-t", "="+sess)...)
	if win != "" {
		argv = append(argv, ";", "select-window", "-t", "="+sess+":"+win)
	}
	return argv
}

// CreateWall creates and configures the owned composite session a group wall
// tiles: one pane per member shadow, sized to the viewport. Ownership is
// tagged immediately after creation and before any pane is split — the same
// order CreateView uses — so a failure before the tag leaves nothing to
// clean, and a later failure returns the tagged capability so its caller can
// retain and retry ownership-checked cleanup. Panes run through wallPaneCommand,
// never a direct attach: a direct attach to a member would join the user's
// other clients and fight them for window focus.
func CreateWall(identity string, shadows []ViewRef, width, height int) (ViewRef, error) {
	if !validViewIdentity(identity) {
		return ViewRef{}, fmt.Errorf("create wall: invalid identity %q", identity)
	}
	if len(shadows) == 0 {
		return ViewRef{}, fmt.Errorf("create wall: no members")
	}

	name := WallPrefix + identity
	owner := viewOwnerVersion + name
	args := []string{"new-session", "-d", "-P", "-F", "#{session_id}", "-s", name}
	if width > 0 && height > 0 {
		args = append(args, "-x", strconv.Itoa(width), "-y", strconv.Itoa(height))
	}
	args = append(args, wallPaneCommand(shadows[0]))
	out, err := Runner(args...)
	if err != nil {
		return ViewRef{}, fmt.Errorf("create wall %q: %w", name, err)
	}

	sessionID := strings.TrimSpace(out)
	if !validSessionID(sessionID) {
		return ViewRef{}, fmt.Errorf("create wall %q: invalid session id %q", name, sessionID)
	}
	ref := ViewRef{Name: name, SessionID: sessionID, Owner: owner}

	if err := Run("set-option", "-t", sessionID, viewOwnerOption, owner); err != nil {
		return ViewRef{}, fmt.Errorf("tag wall %q: %w", name, err)
	}
	for _, shadow := range shadows[1:] {
		if err := Run("split-window", "-t", sessionID, wallPaneCommand(shadow)); err != nil {
			return ref, fmt.Errorf("split wall %q: %w", name, err)
		}
	}
	if err := Run("select-layout", "-t", sessionID, "tiled"); err != nil {
		return ref, fmt.Errorf("layout wall %q: %w", name, err)
	}
	return ref, nil
}

// wallPaneCommand is one wall pane's shell command: a nested attach to an
// owned member shadow via the same ownership-gated queue AttachViewArgv uses.
// TMUX is cleared for this command only — the pane is a fresh top-level tmux
// client composing into the wall, not evidence that it is already inside
// itself. tmux always runs a multi-word shell-command through $SHELL -c, so
// the fully quoted line is passed as a single trailing argument.
func wallPaneCommand(ref ViewRef) string {
	argv := AttachViewArgv(ref)
	if len(argv) == 0 {
		return ""
	}
	words := make([]string, len(argv))
	for i, a := range argv {
		words[i] = wallShellQuote(a)
	}
	return "TMUX= " + strings.Join(words, " ")
}

func wallShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// AttachWallArgv is AttachViewArgv's counterpart for the owned wall session:
// the same ownership-gated predicate and destroy-unattached/attach ordering,
// evaluated against WallPrefix instead of ViewPrefix.
func AttachWallArgv(ref ViewRef) []string {
	if !validWallRef(ref) {
		return nil
	}
	attach := fmt.Sprintf(
		"set-option -t '%s' destroy-unattached on ; attach-session -t '%s'",
		ref.SessionID, ref.SessionID,
	)
	return append([]string{"tmux"}, Argv(
		"if-shell", "-F", "-t", ref.SessionID,
		ownedViewPredicate(ref), attach, "",
	)...)
}

// KillWallIfOwned is KillViewIfOwned's counterpart for the owned wall
// session: the same atomic ownership-checked conditional kill.
func KillWallIfOwned(ref ViewRef) error {
	if !validWallRef(ref) {
		return nil
	}
	kill := fmt.Sprintf("kill-session -t '%s'", ref.SessionID)
	return Run("if-shell", "-F", "-t", ref.SessionID, ownedViewPredicate(ref), kill, "")
}

// IsOwnedWall reports whether a queried session is the owned wall composite,
// so the rail excludes it from the fleet exactly as it excludes gm-view-*
// shadows: a prefix match alone is insufficient, untagged gm-wall-* names are
// ordinary sessions.
func IsOwnedWall(session Session) bool {
	return strings.HasPrefix(session.Name, WallPrefix) &&
		len(session.Name) > len(WallPrefix) &&
		session.ViewOwner == viewOwnerVersion+session.Name
}

// IsOwnedView reports whether a queried session is ghostmux viewport plumbing.
// A prefix match is intentionally insufficient: untagged and malformed legacy
// gm-view-* sessions are ordinary visible sessions.
func IsOwnedView(session Session) bool {
	return strings.HasPrefix(session.Name, ViewPrefix) &&
		len(session.Name) > len(ViewPrefix) &&
		session.ViewOwner == viewOwnerVersion+session.Name
}

// KillViewIfOwned atomically checks the name and exact owner marker in the
// current session-ID context, then kills. One tmux conditional command avoids
// the check-then-kill race and makes repeated cleanup safe.
func KillViewIfOwned(ref ViewRef) error {
	if !validViewRef(ref) {
		return nil
	}
	kill := fmt.Sprintf("kill-session -t '%s'", ref.SessionID)
	return Run("if-shell", "-F", "-t", ref.SessionID, ownedViewPredicate(ref), kill, "")
}

// ownedViewPredicate is the single authorization predicate shared by the
// attach queue and retirement. It is evaluated by tmux in ref.SessionID's
// current context, never against a cached name lookup.
func ownedViewPredicate(ref ViewRef) string {
	return fmt.Sprintf(
		"#{&&:#{==:#{session_name},%s},#{==:#{%s},%s}}",
		ref.Name, viewOwnerOption, ref.Owner,
	)
}

func validViewIdentity(identity string) bool {
	if identity == "" {
		return false
	}
	for _, r := range identity {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

func validSessionID(id string) bool {
	if len(id) < 2 || id[0] != '$' {
		return false
	}
	for _, r := range id[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func validViewRef(ref ViewRef) bool {
	return validSessionID(ref.SessionID) &&
		strings.HasPrefix(ref.Name, ViewPrefix) &&
		len(ref.Name) > len(ViewPrefix) &&
		ref.Owner == viewOwnerVersion+ref.Name
}

// validWallRef mirrors validViewRef against WallPrefix instead of ViewPrefix.
func validWallRef(ref ViewRef) bool {
	return validSessionID(ref.SessionID) &&
		strings.HasPrefix(ref.Name, WallPrefix) &&
		len(ref.Name) > len(WallPrefix) &&
		ref.Owner == viewOwnerVersion+ref.Name
}
