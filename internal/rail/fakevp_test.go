package rail

import "github.com/1broseidon/ghostmux/internal/tmux"

// fakeViewport is the rail's test double for Viewport. The rail's brain is
// what these tests exercise; how a selection is rendered is the frame's
// problem, so the double only records what it was asked to do.
type fakeViewport struct {
	lock         ViewState
	grouped      bool
	detached     bool
	focused      bool
	killed       []string
	points       []string
	healErr      error
	syncCalls    int
	pointBlocked bool

	walls       [][]string // one entry per PointWall call: group, then its members
	wallBlocked bool
}

func (v *fakeViewport) Point(sess, win string, grouped bool) {
	v.points = append(v.points, sess+":"+win)
	if v.pointBlocked {
		return
	}
	v.lock = ViewState{Sess: sess, Win: win}
	v.grouped, v.detached = grouped, false
	if win != "" {
		tmux.SetDone(sess, win, false)
	}
}

// PointWall records the call and, unless a test arms wallBlocked, publishes
// the ViewState.Wall lock the rail checks after asking to wall.
func (v *fakeViewport) PointWall(group string, members []string) {
	v.walls = append(v.walls, append([]string{group}, members...))
	if v.wallBlocked {
		return
	}
	v.lock = ViewState{Sess: group, Wall: true}
	v.grouped, v.detached = false, false
}
func (v *fakeViewport) Idle()               { v.lock = ViewState{} }
func (v *fakeViewport) Detach()             { v.Idle(); v.detached = true }
func (v *fakeViewport) Heal() (bool, error) { return false, v.healErr }
func (v *fakeViewport) Lock() ViewState     { return v.lock }
func (v *fakeViewport) AttachTarget() string {
	if v.grouped {
		return "" // the rail double does not create a real owned shadow
	}
	return v.lock.Sess
}
func (v *fakeViewport) FocusViewport() { v.focused = true }
func (v *fakeViewport) SyncActiveWindow(windows []tmux.Window) {
	v.syncCalls++
	if v.lock.Sess == "" {
		return
	}
	for _, w := range windows {
		if w.Session == v.AttachTarget() && w.Active {
			v.lock.Win = w.Index
		}
	}
}
func (v *fakeViewport) OnKill(sess string) {
	v.killed = append(v.killed, ":"+sess)
	if v.lock.Sess == sess {
		v.Idle()
	}
}
