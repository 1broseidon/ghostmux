package rail

import "github.com/1broseidon/ghostmux/internal/tmux"

// fakeViewport is the rail's test double for Viewport. The rail's brain is
// what these tests exercise; how a selection is rendered is the frame's
// problem, so the double only records what it was asked to do.
type fakeViewport struct {
	lock     ViewState
	grouped  bool
	detached bool
	focused  bool
	killed   []string
	points   []string
}

func (v *fakeViewport) Point(sess, win string, grouped bool) {
	v.lock = ViewState{Sess: sess, Win: win}
	v.grouped, v.detached = grouped, false
	v.points = append(v.points, sess+":"+win)
	if win != "" {
		tmux.SetDone(sess, win, false)
	}
}
func (v *fakeViewport) PointAux(backend, sess string) {
	v.lock = ViewState{Sess: sess, Backend: backend}
	v.detached = false
	v.points = append(v.points, backend+":"+sess)
}
func (v *fakeViewport) Idle()           { v.lock = ViewState{} }
func (v *fakeViewport) Detach()         { v.Idle(); v.detached = true }
func (v *fakeViewport) Heal() bool      { return false }
func (v *fakeViewport) Lock() ViewState { return v.lock }
func (v *fakeViewport) AttachTarget() string {
	if v.grouped {
		return GroupedName(v.lock.Sess)
	}
	return v.lock.Sess
}
func (v *fakeViewport) FocusViewport() { v.focused = true }
func (v *fakeViewport) SyncActiveWindow(windows []tmux.Window) {
	if v.lock.Sess == "" || v.lock.Backend != "" {
		return
	}
	for _, w := range windows {
		if w.Session == v.AttachTarget() && w.Active {
			v.lock.Win = w.Index
		}
	}
}
func (v *fakeViewport) OnKill(sess, backend string) {
	v.killed = append(v.killed, backend+":"+sess)
	if v.lock.Sess == sess && v.lock.Backend == backend {
		if v.grouped && backend == "" {
			tmux.Run("kill-session", "-t", "="+GroupedName(sess))
		}
		v.Idle()
	}
}
