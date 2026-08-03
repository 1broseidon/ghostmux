package app

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/1broseidon/ghostmux/internal/rail"
	"github.com/1broseidon/ghostmux/internal/term"
	"github.com/1broseidon/ghostmux/internal/theme"
	"github.com/1broseidon/ghostmux/internal/tmux"
)

// Wall border colors resolve through the theme seam like every other color
// (SPEC-OWNED-CHROME law 4), then through theme.Tmux once so tmux.CreateWall
// receives an opaque, already-spelled style value and never hand-converts.
var (
	wallBorderDim    = theme.Tmux(theme.C("#504945", "8"))
	wallBorderAccent = theme.Tmux(theme.C("#fe8019", "9"))
)

// retireKind selects which ownership-checked kill a retained capability needs
// retried: a grouped shadow (ViewPrefix) and the wall composite (WallPrefix)
// are tagged with different name prefixes and therefore different predicates.
type retireKind uint8

const (
	retireGroupedView retireKind = iota
	retireWallSession
)

func killFor(kind retireKind) func(tmux.ViewRef) error {
	if kind == retireWallSession {
		return tmux.KillWallIfOwned
	}
	return tmux.KillViewIfOwned
}

func kindNoun(kind retireKind) string {
	if kind == retireWallSession {
		return "wall"
	}
	return "view"
}

type pendingViewRetirement struct {
	ref  tmux.ViewRef
	err  error
	kind retireKind
}

// ptyViewport is the solo frame's rail.Viewport: instead of respawning a
// sibling tmux pane, it runs an attach client on an embedded terminal. Grouped
// tmux attaches use a uniquely named, explicitly owned shadow for this panel
// and this attach only.
type ptyViewport struct {
	w *term.Widget

	lockSess string // original session currently rendered, "" = idle
	lockWin  string // window index within lockSess
	detached bool   // user pressed d — heal stays idle until the next Point
	grouped  bool   // attached through view

	view               tmux.ViewRef // current owned grouped shadow, zero if direct/idle
	pendingRetirements []pendingViewRetirement
	panelNonce         string
	sequence           uint64

	// Wall state. wallGroup is the group label backing rail.ViewState.Wall,
	// "" when not walled — checked before lockSess anywhere the two paths
	// fork, since a walled viewport never has a normal session lock. wall is
	// the owned composite session; wallShadows/wallOrigins mirror its panes
	// 1:1, origin (the real member) alongside the owned shadow attached in
	// that pane, so Heal can tell a dead member from a dead wall.
	wallGroup   string
	wall        tmux.ViewRef
	wallShadows []tmux.ViewRef
	wallOrigins []string

	// Child-operation seams keep viewport lifecycle tests off the real PTY.
	// Production binds all three directly to w.
	startChild   func([]string, []string) error
	stopChild    func()
	childRunning func() bool

	focusReq bool // FocusViewport was called; the frame consumes this
}

func newPtyViewport(cols, rows int, notify func(tea.Msg)) *ptyViewport {
	w := term.New(cols, rows, notify)
	v := &ptyViewport{w: w, panelNonce: tmux.NewViewNonce()}
	v.startChild = w.Start
	v.stopChild = w.Stop
	v.childRunning = w.Running
	return v
}

// Lock implements rail.Viewport. It always names the original target, never
// the transient owned shadow — except while walled, where there is no single
// original target and Sess names the group instead (the group header is the
// wall's row).
func (v *ptyViewport) Lock() rail.ViewState {
	if v.wallGroup != "" {
		return rail.ViewState{Sess: v.wallGroup, Wall: true}
	}
	return rail.ViewState{Sess: v.lockSess, Win: v.lockWin}
}

// AttachTarget implements rail.Viewport.
func (v *ptyViewport) AttachTarget() string {
	if v.wallGroup != "" {
		return v.wall.Name
	}
	if v.grouped {
		return v.view.Name
	}
	return v.lockSess
}

// Point implements rail.Viewport: stop and safely retire the previous child
// and owned shadow, then run a new tmux client for sess.
func (v *ptyViewport) Point(sess, win string, grouped bool) {
	if sess == "" {
		return
	}
	v.stopCurrent()

	var (
		ref  tmux.ViewRef
		argv []string
		err  error
	)
	if grouped {
		v.sequence++
		identity := tmux.ViewIdentity(v.panelNonce, v.sequence)
		ref, err = tmux.CreateView(sess, win, identity)
		if err != nil {
			// A post-tag error returns a cleanup capability; a pre-tag error
			// returns zero and retireView deliberately does nothing.
			_ = v.retireView(ref)
			return // old logical lock remains available for heal
		}
		argv = tmux.AttachViewArgv(ref)
	} else {
		argv = tmux.AttachSessionArgv(sess, win)
	}
	if len(argv) == 0 {
		_ = v.retireView(ref)
		return
	}
	if err := v.startChild(argv, nil); err != nil {
		// ref is a tagged capability. The retained conditional cleanup path is
		// still the only path allowed to kill it.
		_ = v.retireView(ref)
		return // the last real frame stays; heal retries
	}

	v.view = ref
	v.lockSess, v.lockWin, v.detached, v.grouped = sess, win, false, grouped
	if win != "" {
		tmux.SetDone(sess, win, false)
	}
}

// PointWall implements rail.Viewport: retire whatever the panel currently
// shows, shadow every member exactly as a grouped Point does (never the
// original — a direct attach would join the user's other clients and fight
// them for window focus), tile them into one owned composite sized to the
// viewport, then attach that composite as the child. A failure at any stage
// retires whatever it already tagged and leaves the old logical lock alone —
// same recovery shape as a failed Point: the last real frame stays, and heal
// can still repoint it.
func (v *ptyViewport) PointWall(group string, members []string) {
	if group == "" || len(members) == 0 {
		return
	}
	v.stopCurrent()

	shadows := make([]tmux.ViewRef, 0, len(members))
	origins := make([]string, 0, len(members))
	for _, member := range members {
		v.sequence++
		identity := tmux.ViewIdentity(v.panelNonce, v.sequence)
		ref, err := tmux.CreateView(member, "", identity)
		if err != nil {
			_ = v.retireRef(ref, retireGroupedView)
			continue // a member that failed to shadow is simply not walled
		}
		shadows = append(shadows, ref)
		origins = append(origins, member)
	}
	if len(shadows) == 0 {
		return
	}

	v.sequence++
	wallIdentity := tmux.ViewIdentity(v.panelNonce, v.sequence)
	cols, rows := v.w.Size()
	wall, err := tmux.CreateWall(wallIdentity, shadows, origins, wallBorderDim, wallBorderAccent, cols, rows)
	if err != nil {
		_ = v.retireRef(wall, retireWallSession)
		for _, ref := range shadows {
			_ = v.retireRef(ref, retireGroupedView)
		}
		return
	}
	argv := tmux.AttachWallArgv(wall)
	if len(argv) == 0 {
		_ = v.retireRef(wall, retireWallSession)
		for _, ref := range shadows {
			_ = v.retireRef(ref, retireGroupedView)
		}
		return
	}
	if err := v.startChild(argv, nil); err != nil {
		_ = v.retireRef(wall, retireWallSession)
		for _, ref := range shadows {
			_ = v.retireRef(ref, retireGroupedView)
		}
		return
	}

	v.wall, v.wallShadows, v.wallOrigins, v.wallGroup = wall, shadows, origins, group
	v.lockSess, v.lockWin, v.detached, v.grouped = "", "", false, false
}

// stopCurrent stops the PTY child first, retries older retirements, then
// retires the exact current view and — mutually exclusive with it — the
// current wall. A failed current capability is moved to the pending
// collection before its field is cleared, so a replacement cannot overwrite
// the only safe way to clean the old session.
func (v *ptyViewport) stopCurrent() error {
	v.stopChild()
	pendingErr := v.retryPendingRetirements()
	currentErr := v.retireCurrentView()
	wallErr := v.retireCurrentWall()
	return joinViewportErrors(pendingErr, currentErr, wallErr)
}

func (v *ptyViewport) retireCurrentView() error {
	ref := v.view
	if ref == (tmux.ViewRef{}) {
		return nil
	}
	err := v.retireView(ref)
	// On error retireView retained ref in pendingRetirements first. On success
	// the ownership-checked tmux command completed (including mismatch/no-op).
	v.view = tmux.ViewRef{}
	return err
}

// retireCurrentWall retires every member shadow, then the composite itself,
// even when an earlier one in the set fails — a stuck member must not block
// draining the rest, and the wall session's own retirement is what a caller
// checks pendingRetirements against afterward.
func (v *ptyViewport) retireCurrentWall() error {
	shadows := v.wallShadows
	v.wallShadows, v.wallOrigins = nil, nil
	errs := make([]error, 0, len(shadows)+1)
	for _, ref := range shadows {
		errs = append(errs, v.retireRef(ref, retireGroupedView))
	}
	ref := v.wall
	v.wall = tmux.ViewRef{}
	errs = append(errs, v.retireRef(ref, retireWallSession))
	return joinViewportErrors(errs...)
}

// retireView is the single-grouped-view convenience wrapper retained for
// Point's call sites.
func (v *ptyViewport) retireView(ref tmux.ViewRef) error {
	return v.retireRef(ref, retireGroupedView)
}

func (v *ptyViewport) retireRef(ref tmux.ViewRef, kind retireKind) error {
	if ref == (tmux.ViewRef{}) {
		return nil
	}
	if err := killFor(kind)(ref); err != nil {
		wrapped := fmt.Errorf("retire owned tmux %s %s (%s): %w", kindNoun(kind), ref.Name, ref.SessionID, err)
		v.rememberRetirement(ref, wrapped, kind)
		return wrapped
	}
	v.forgetRetirement(ref)
	return nil
}

func (v *ptyViewport) rememberRetirement(ref tmux.ViewRef, err error, kind retireKind) {
	for i := range v.pendingRetirements {
		if v.pendingRetirements[i].ref == ref {
			v.pendingRetirements[i].err = err
			v.pendingRetirements[i].kind = kind
			return
		}
	}
	v.pendingRetirements = append(v.pendingRetirements, pendingViewRetirement{ref: ref, err: err, kind: kind})
}

func (v *ptyViewport) forgetRetirement(ref tmux.ViewRef) {
	for i := range v.pendingRetirements {
		if v.pendingRetirements[i].ref != ref {
			continue
		}
		copy(v.pendingRetirements[i:], v.pendingRetirements[i+1:])
		v.pendingRetirements = v.pendingRetirements[:len(v.pendingRetirements)-1]
		return
	}
}

// retryPendingRetirements attempts every retained capability even when an
// earlier one fails. Only a successful ownership-checked tmux command removes
// a capability; a false ownership predicate is a successful safe no-op.
func (v *ptyViewport) retryPendingRetirements() error {
	pending := v.pendingRetirements
	remaining := make([]pendingViewRetirement, 0, len(pending))
	for _, retirement := range pending {
		if err := killFor(retirement.kind)(retirement.ref); err != nil {
			retirement.err = fmt.Errorf(
				"retire owned tmux %s %s (%s): %w",
				kindNoun(retirement.kind), retirement.ref.Name, retirement.ref.SessionID, err,
			)
			remaining = append(remaining, retirement)
		}
	}
	v.pendingRetirements = remaining
	return v.pendingRetirementError()
}

func (v *ptyViewport) pendingRetirementError() error {
	errs := make([]error, 0, len(v.pendingRetirements))
	for _, retirement := range v.pendingRetirements {
		errs = append(errs, retirement.err)
	}
	return joinViewportErrors(errs...)
}

// joinViewportErrors keeps the rail's existing one-line viewport error path
// intact even when several retained capabilities fail in the same tick.
func joinViewportErrors(errs ...error) error {
	messages := make([]string, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			messages = append(messages, err.Error())
		}
	}
	switch len(messages) {
	case 0:
		return nil
	case 1:
		for _, err := range errs {
			if err != nil {
				return err
			}
		}
	}
	return errors.New(strings.Join(messages, "; "))
}

// Idle implements rail.Viewport: stop the child, clean its owned view or
// wall, and drop the logical target lock.
func (v *ptyViewport) Idle() {
	v.stopCurrent()
	v.lockSess, v.lockWin = "", ""
	v.grouped = false
	v.wallGroup = ""
}

// Detach implements rail.Viewport.
func (v *ptyViewport) Detach() {
	v.Idle()
	v.detached = true
}

// ChildExited handles term.ExitMsg. It deliberately retains the logical lock,
// owned shadow capability, and emulator frame until Heal can distinguish
// authoritative absence from a backend outage. A stale exit message remains
// inert when a newer child is already running.
func (v *ptyViewport) ChildExited() {
	_ = v.retryPendingRetirements()
	if v.childRunning() {
		return
	}
}

// Heal implements rail.Viewport. For a grouped attach, the original target is
// checked even while the child runs: otherwise its shadow would keep shared
// windows alive after the original session was externally killed.
func (v *ptyViewport) Heal() (bool, error) {
	_ = v.retryPendingRetirements()
	if v.detached {
		return false, v.pendingRetirementError() // idle on purpose: not a death to report
	}
	if v.wallGroup != "" {
		return v.healWall()
	}
	if v.lockSess == "" {
		return false, v.pendingRetirementError() // idle on purpose: not a death to report
	}

	// A grouped shadow can keep shared windows alive after the original dies,
	// so validate it even while its child runs. A dead direct child also needs
	// validation before either idling or reattaching.
	if v.grouped || !v.childRunning() {
		exists, err := v.lockExists()
		if err != nil {
			backendErr := fmt.Errorf("tmux unavailable: %w", err)
			return false, joinViewportErrors(v.pendingRetirementError(), backendErr)
		}
		if !exists {
			v.stopChild()
			_ = v.retireCurrentView()
			v.lockSess, v.lockWin = "", ""
			v.grouped = false
			return true, v.pendingRetirementError()
		}
	}
	if v.childRunning() {
		return false, v.pendingRetirementError()
	}
	v.Point(v.lockSess, v.lockWin, v.grouped)
	return true, v.pendingRetirementError()
}

// lockExists reports whether the original locked session is still alive. It
// never asks whether an owned grouped shadow survived.
func (v *ptyViewport) lockExists() (bool, error) {
	return tmux.ProbeSession(v.lockSess)
}

// healWall validates the composite itself first: an absent wall session
// idles, retiring every member shadow along with it (KillViewIfOwned is a
// safe no-op on a shadow tmux already tore down with its group). A present
// wall is then checked pane-by-pane — members whose origin died are retired
// while the composite survives with whatever panes remain, tmux having
// already closed the dead ones on its own once their attach client lost its
// target. Only once the wall itself is confirmed alive does a dead child get
// re-attached.
func (v *ptyViewport) healWall() (bool, error) {
	exists, err := tmux.ProbeSession(v.wall.Name)
	if err != nil {
		backendErr := fmt.Errorf("tmux unavailable: %w", err)
		return false, joinViewportErrors(v.pendingRetirementError(), backendErr)
	}
	if !exists {
		v.stopChild()
		_ = v.retireCurrentWall()
		v.wallGroup = ""
		return true, v.pendingRetirementError()
	}
	v.healWallMembers()
	if v.childRunning() {
		return false, v.pendingRetirementError()
	}
	argv := tmux.AttachWallArgv(v.wall)
	if len(argv) == 0 {
		return false, v.pendingRetirementError()
	}
	if err := v.startChild(argv, nil); err != nil {
		return false, v.pendingRetirementError()
	}
	return true, v.pendingRetirementError()
}

// healWallMembers retires the shadow of any member whose origin session is
// authoritatively gone. A query outage leaves a member in place rather than
// guessing it dead — the same fail-safe the rest of heal uses.
func (v *ptyViewport) healWallMembers() {
	live := v.wallShadows[:0:0]
	liveOrigins := v.wallOrigins[:0:0]
	for i, origin := range v.wallOrigins {
		present, err := tmux.ProbeSession(origin)
		if err != nil || present {
			live = append(live, v.wallShadows[i])
			liveOrigins = append(liveOrigins, origin)
			continue
		}
		_ = v.retireRef(v.wallShadows[i], retireGroupedView)
	}
	v.wallShadows, v.wallOrigins = live, liveOrigins
}

// FocusViewport implements rail.Viewport: solo has no panes to select, so it
// records the request and the frame moves input focus after the rail update.
func (v *ptyViewport) FocusViewport() { v.focusReq = true }

// takeFocusRequest consumes a pending focus request.
func (v *ptyViewport) takeFocusRequest() bool {
	req := v.focusReq
	v.focusReq = false
	return req
}

// SyncActiveWindow implements rail.Viewport: follow ctrl+b navigation the user
// performs inside the embedded client, exactly as classic does.
func (v *ptyViewport) SyncActiveWindow(windows []tmux.Window) {
	if v.lockSess == "" {
		return
	}
	target := v.AttachTarget()
	if target == "" {
		return
	}
	for _, w := range windows {
		if w.Session == target && w.Active {
			v.lockWin = w.Index
			break
		}
	}
}

// OnKill implements rail.Viewport: retire the owned shadow (which would keep
// grouped windows alive) and idle if the kill took the logical target lock.
func (v *ptyViewport) OnKill(sess string) {
	if v.lockSess == sess {
		v.Idle()
	}
}

// Close owns the panel-exit path: stop the child, retry every pending/current
// exact capability best-effort, then join and close the terminal widget. It is
// safe to call repeatedly; a later call retries capabilities retained by an
// earlier cleanup error.
func (v *ptyViewport) Close() error {
	_ = v.stopCurrent()
	v.lockSess, v.lockWin = "", ""
	v.grouped = false
	v.wallGroup = ""
	v.detached = true
	v.w.Close()
	return v.pendingRetirementError()
}
