package app

import (
	"errors"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/1broseidon/ghostmux/internal/rail"
	"github.com/1broseidon/ghostmux/internal/term"
	"github.com/1broseidon/ghostmux/internal/tmux"
)

type pendingViewRetirement struct {
	ref tmux.ViewRef
	err error
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
	remote   bool   // PointRemote child (ssh); no local probe can heal it

	view               tmux.ViewRef // current owned grouped shadow, zero if direct/idle
	pendingRetirements []pendingViewRetirement
	panelNonce         string
	sequence           uint64

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
// the transient owned shadow.
func (v *ptyViewport) Lock() rail.ViewState {
	return rail.ViewState{Sess: v.lockSess, Win: v.lockWin}
}

// AttachTarget implements rail.Viewport.
func (v *ptyViewport) AttachTarget() string {
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
	v.remote = false
	if win != "" {
		tmux.SetDone(sess, win, false)
	}
}

// PointRemote implements rail.Viewport (PROTOTYPE): run ssh to a declared
// remote workspace as the viewport child. The lock records the reach name so
// the rail and bar can say what is showing; no local probe can validate it.
func (v *ptyViewport) PointRemote(name, host, session string) {
	if name == "" || host == "" || session == "" {
		return
	}
	v.stopCurrent()
	argv := []string{"ssh", "-t", host, "--", "tmux", "new-session", "-A", "-s", session}
	if err := v.startChild(argv, nil); err != nil {
		return
	}
	v.lockSess, v.lockWin = name, ""
	v.detached, v.grouped, v.remote = false, false, true
}

// stopCurrent stops the PTY child first, retries older retirements, then
// retires the exact current view. A failed current capability is moved to the
// pending collection before v.view is cleared, so a replacement cannot
// overwrite the only safe way to clean the old session.
func (v *ptyViewport) stopCurrent() error {
	v.stopChild()
	pendingErr := v.retryPendingRetirements()
	currentErr := v.retireCurrentView()
	return joinViewportErrors(pendingErr, currentErr)
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

func (v *ptyViewport) retireView(ref tmux.ViewRef) error {
	if ref == (tmux.ViewRef{}) {
		return nil
	}
	if err := tmux.KillViewIfOwned(ref); err != nil {
		wrapped := fmt.Errorf("retire owned tmux view %s (%s): %w", ref.Name, ref.SessionID, err)
		v.rememberRetirement(ref, wrapped)
		return wrapped
	}
	v.forgetRetirement(ref)
	return nil
}

func (v *ptyViewport) rememberRetirement(ref tmux.ViewRef, err error) {
	for i := range v.pendingRetirements {
		if v.pendingRetirements[i].ref == ref {
			v.pendingRetirements[i].err = err
			return
		}
	}
	v.pendingRetirements = append(v.pendingRetirements, pendingViewRetirement{ref: ref, err: err})
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
		if err := tmux.KillViewIfOwned(retirement.ref); err != nil {
			retirement.err = fmt.Errorf(
				"retire owned tmux view %s (%s): %w",
				retirement.ref.Name, retirement.ref.SessionID, err,
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

// Idle implements rail.Viewport: stop the child, clean its owned view, and
// drop the logical target lock.
func (v *ptyViewport) Idle() {
	v.stopCurrent()
	v.lockSess, v.lockWin = "", ""
	v.grouped, v.remote = false, false
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
	if v.detached || v.lockSess == "" {
		return false, v.pendingRetirementError() // idle on purpose: not a death to report
	}
	if v.remote {
		// A remote child cannot be validated by a local probe, and blindly
		// re-running ssh could loop on a dead link or an auth prompt. A
		// finished ssh — detach or drop — idles; ↵ reconnects deliberately.
		if v.childRunning() {
			return false, v.pendingRetirementError()
		}
		v.lockSess, v.lockWin = "", ""
		v.remote = false
		return true, v.pendingRetirementError()
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
	if v.lockSess == "" || v.remote {
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
	v.grouped, v.remote = false, false
	v.detached = true
	v.w.Close()
	return v.pendingRetirementError()
}
