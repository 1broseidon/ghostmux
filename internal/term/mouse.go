package term

import (
	tea "github.com/charmbracelet/bubbletea"
	uv "github.com/charmbracelet/ultraviolet"
)

// SendMouse routes a bubbletea v1 mouse message into the child, translated
// by (xoff, yoff) — the widget's top-left in host-window coordinates. The
// emulator is mode-aware: if the child never enabled mouse reporting, the
// event encodes to nothing, exactly like a real terminal.
func (w *Widget) SendMouse(m tea.MouseMsg, xoff, yoff int) {
	mouse := uv.Mouse{
		X:      m.X - xoff,
		Y:      m.Y - yoff,
		Button: mouseButtons[m.Button],
		Mod:    mouseMods(tea.MouseEvent(m)),
	}
	if mouse.X < 0 || mouse.Y < 0 {
		return
	}
	switch {
	case m.Action == tea.MouseActionMotion:
		w.emu.SendMouse(uv.MouseMotionEvent(mouse))
	case tea.MouseEvent(m).IsWheel():
		w.emu.SendMouse(uv.MouseWheelEvent(mouse))
	case m.Action == tea.MouseActionPress:
		w.emu.SendMouse(uv.MouseClickEvent(mouse))
	case m.Action == tea.MouseActionRelease:
		w.emu.SendMouse(uv.MouseReleaseEvent(mouse))
	}
}

// mouseButtons maps bubbletea v1 buttons to uv buttons (both follow X11
// numbering; the zero values align too, so a missing entry is MouseNone).
var mouseButtons = map[tea.MouseButton]uv.MouseButton{
	tea.MouseButtonNone:       uv.MouseNone,
	tea.MouseButtonLeft:       uv.MouseLeft,
	tea.MouseButtonMiddle:     uv.MouseMiddle,
	tea.MouseButtonRight:      uv.MouseRight,
	tea.MouseButtonWheelUp:    uv.MouseWheelUp,
	tea.MouseButtonWheelDown:  uv.MouseWheelDown,
	tea.MouseButtonWheelLeft:  uv.MouseWheelLeft,
	tea.MouseButtonWheelRight: uv.MouseWheelRight,
	tea.MouseButtonBackward:   uv.MouseBackward,
	tea.MouseButtonForward:    uv.MouseForward,
}

func mouseMods(m tea.MouseEvent) uv.KeyMod {
	var mod uv.KeyMod
	if m.Shift {
		mod |= uv.ModShift
	}
	if m.Alt {
		mod |= uv.ModAlt
	}
	if m.Ctrl {
		mod |= uv.ModCtrl
	}
	return mod
}
