package app

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/1broseidon/ghostmux/internal/rail"
	"github.com/1broseidon/ghostmux/internal/term"
	"github.com/1broseidon/ghostmux/internal/tmux"
)

// ptyViewport is the solo frame's rail.Viewport: instead of respawning a
// sibling tmux pane, it runs the very same attach argv as a child on an
// embedded terminal. The inner mux still owns the session — ghostmux is only
// the frame — so `d`, kill, heal and the grouped gm-view-* dance all mean
// exactly what they mean in classic mode.
type ptyViewport struct {
	w *term.Widget

	lockSess    string // session currently rendered, "" = idle
	lockWin     string // window index within lockSess
	lockBackend string // "" = tmux; e.g. "zellij"
	detached    bool   // user pressed d — heal stays idle until the next Point
	grouped     bool   // attached via a gm-view-* grouped session (tmux only)

	focusReq bool // FocusViewport was called; the frame consumes this
}

func newPtyViewport(cols, rows int, notify func(tea.Msg)) *ptyViewport {
	return &ptyViewport{w: term.New(cols, rows, notify)}
}

// Lock implements rail.Viewport.
func (v *ptyViewport) Lock() rail.ViewState {
	return rail.ViewState{Sess: v.lockSess, Win: v.lockWin, Backend: v.lockBackend}
}

// AttachTarget implements rail.Viewport.
func (v *ptyViewport) AttachTarget() string {
	if v.grouped {
		return rail.GroupedName(v.lockSess)
	}
	return v.lockSess
}

// Point implements rail.Viewport: run a tmux client for sess as the pty child.
func (v *ptyViewport) Point(sess, win string, grouped bool) {
	if sess == "" {
		return
	}
	if err := v.w.Start(rail.AttachArgv(sess, win, grouped), nil); err != nil {
		return // the last real frame stays; heal retries. Never fake a frame.
	}
	v.lockSess, v.lockWin, v.detached, v.grouped = sess, win, false, grouped
	v.lockBackend = ""
	if win != "" {
		tmux.SetDone(sess, win, false)
	}
}

// PointAux implements rail.Viewport: run a non-tmux backend's client instead.
func (v *ptyViewport) PointAux(backend, sess string) {
	if sess == "" {
		return
	}
	argv := rail.AuxAttachArgv(backend, sess)
	if argv == nil {
		return
	}
	if err := v.w.Start(argv, nil); err != nil {
		return
	}
	v.lockSess, v.lockWin, v.lockBackend = sess, "", backend
	v.detached, v.grouped = false, false
}

// Idle implements rail.Viewport: kill the child and drop the lock. The frame
// renders the idle placeholder in-process — no `rail idle` subprocess here.
func (v *ptyViewport) Idle() {
	v.w.Stop()
	v.lockSess, v.lockWin, v.lockBackend = "", "", ""
	v.grouped = false
}

// Detach implements rail.Viewport.
func (v *ptyViewport) Detach() {
	v.Idle()
	v.detached = true
}

// Heal implements rail.Viewport. Unlike classic — which respawns a dead pane
// unconditionally — solo first proves the locked session still exists. A
// session killed from outside would otherwise make the viewport re-attach to
// nothing, forever, once per tick.
func (v *ptyViewport) Heal() bool {
	if v.w.Running() {
		return false
	}
	if v.detached || v.lockSess == "" {
		return false // idle on purpose: not a death to report
	}
	if !v.lockExists() {
		v.Idle()
		return true
	}
	if v.lockBackend != "" {
		v.PointAux(v.lockBackend, v.lockSess)
	} else {
		v.Point(v.lockSess, v.lockWin, v.grouped)
	}
	return true
}

// lockExists reports whether the locked session is still alive in its backend.
func (v *ptyViewport) lockExists() bool {
	if v.lockBackend != "" {
		return rail.AuxSessionExists(v.lockBackend, v.lockSess)
	}
	return tmux.Run("has-session", "-t", "="+v.lockSess) == nil
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
	if v.lockSess == "" || v.lockBackend != "" {
		return
	}
	target := v.AttachTarget()
	for _, w := range windows {
		if w.Session == target && w.Active {
			v.lockWin = w.Index
			break
		}
	}
}

// OnKill implements rail.Viewport: drop the grouped shadow (which would keep
// the killed session's windows alive in its group) and idle if the kill took
// the lock.
func (v *ptyViewport) OnKill(sess, backend string) {
	if backend != "" {
		if v.lockBackend == backend && v.lockSess == sess {
			v.Idle()
		}
		return
	}
	if v.lockSess == sess {
		if v.grouped {
			tmux.Run("kill-session", "-t", "="+rail.GroupedName(sess))
		}
		v.Idle()
	}
}
