package term

import (
	tea "github.com/charmbracelet/bubbletea"
	uv "github.com/charmbracelet/ultraviolet"
)

// SendKey routes a bubbletea v1 key message into the child. Paste goes
// through the emulator's bracketed-paste-aware Paste. Everything else is
// translated to a uv KeyPressEvent so the emulator's mode-aware encoder
// (DECCKM cursor keys, application keypad) picks the bytes. The combinations
// vt's encoder doesn't cover yet (modified arrows/nav keys — its own TODO)
// are hand-encoded as xterm CSI sequences and injected raw: the documented
// fallback, kept in one table below.
func (w *Widget) SendKey(k tea.KeyMsg) {
	if k.Paste {
		w.emu.Paste(string(k.Runes))
		return
	}
	if raw, ok := rawKeySeq[k.Type]; ok {
		w.emu.SendText(raw)
		return
	}
	if ev, ok := keyEvent(tea.Key(k)); ok {
		w.emu.SendKey(ev)
		return
	}
	// Multi-rune KeyRunes (e.g. an unbracketed burst): literal text.
	if k.Type == tea.KeyRunes && len(k.Runes) > 0 {
		w.emu.SendText(string(k.Runes))
	}
}

// rawKeySeq hand-encodes the modified navigation keys vt's SendKey drops
// (its switch matches unmodified codes only). xterm encoding: CSI 1;{1+m}X
// with shift=1, alt=2, ctrl=4. These are mode-independent in real xterm, so
// raw injection is honest.
var rawKeySeq = map[tea.KeyType]string{
	tea.KeyShiftTab: "\x1b[Z",

	tea.KeyShiftUp:    "\x1b[1;2A",
	tea.KeyShiftDown:  "\x1b[1;2B",
	tea.KeyShiftRight: "\x1b[1;2C",
	tea.KeyShiftLeft:  "\x1b[1;2D",

	tea.KeyCtrlUp:    "\x1b[1;5A",
	tea.KeyCtrlDown:  "\x1b[1;5B",
	tea.KeyCtrlRight: "\x1b[1;5C",
	tea.KeyCtrlLeft:  "\x1b[1;5D",

	tea.KeyCtrlShiftUp:    "\x1b[1;6A",
	tea.KeyCtrlShiftDown:  "\x1b[1;6B",
	tea.KeyCtrlShiftRight: "\x1b[1;6C",
	tea.KeyCtrlShiftLeft:  "\x1b[1;6D",

	tea.KeyShiftHome:     "\x1b[1;2H",
	tea.KeyShiftEnd:      "\x1b[1;2F",
	tea.KeyCtrlHome:      "\x1b[1;5H",
	tea.KeyCtrlEnd:       "\x1b[1;5F",
	tea.KeyCtrlShiftHome: "\x1b[1;6H",
	tea.KeyCtrlShiftEnd:  "\x1b[1;6F",

	tea.KeyCtrlPgUp:   "\x1b[5;5~",
	tea.KeyCtrlPgDown: "\x1b[6;5~",
}

// specialKeys maps bubbletea's negative KeyTypes to uv key codes the
// emulator's encoder understands.
var specialKeys = map[tea.KeyType]rune{
	tea.KeyUp:     uv.KeyUp,
	tea.KeyDown:   uv.KeyDown,
	tea.KeyRight:  uv.KeyRight,
	tea.KeyLeft:   uv.KeyLeft,
	tea.KeyHome:   uv.KeyHome,
	tea.KeyEnd:    uv.KeyEnd,
	tea.KeyPgUp:   uv.KeyPgUp,
	tea.KeyPgDown: uv.KeyPgDown,
	tea.KeyDelete: uv.KeyDelete,
	tea.KeyInsert: uv.KeyInsert,
	tea.KeySpace:  uv.KeySpace,
	tea.KeyF1:     uv.KeyF1, tea.KeyF2: uv.KeyF2, tea.KeyF3: uv.KeyF3,
	tea.KeyF4: uv.KeyF4, tea.KeyF5: uv.KeyF5, tea.KeyF6: uv.KeyF6,
	tea.KeyF7: uv.KeyF7, tea.KeyF8: uv.KeyF8, tea.KeyF9: uv.KeyF9,
	tea.KeyF10: uv.KeyF10, tea.KeyF11: uv.KeyF11, tea.KeyF12: uv.KeyF12,
	tea.KeyF13: uv.KeyF13, tea.KeyF14: uv.KeyF14, tea.KeyF15: uv.KeyF15,
	tea.KeyF16: uv.KeyF16, tea.KeyF17: uv.KeyF17, tea.KeyF18: uv.KeyF18,
	tea.KeyF19: uv.KeyF19, tea.KeyF20: uv.KeyF20,
}

// keyEvent translates one bubbletea v1 key to a uv KeyPressEvent. Text is
// deliberately left empty: vt's encoder switch compares whole structs, and
// only Code/Mod participate in its byte choice. Reports false for keys the
// caller should handle another way (multi-rune bursts) or drop.
func keyEvent(k tea.Key) (uv.KeyPressEvent, bool) {
	var mod uv.KeyMod
	if k.Alt {
		mod |= uv.ModAlt
	}
	t := k.Type

	if code, ok := specialKeys[t]; ok {
		return uv.KeyPressEvent{Code: code, Mod: mod}, true
	}

	switch t {
	case tea.KeyRunes:
		if len(k.Runes) != 1 {
			return uv.KeyPressEvent{}, false
		}
		return uv.KeyPressEvent{Code: k.Runes[0], Mod: mod}, true
	case tea.KeyEnter: // CR; also how ctrl+m arrives
		return uv.KeyPressEvent{Code: uv.KeyEnter, Mod: mod}, true
	case tea.KeyTab: // HT; also ctrl+i
		return uv.KeyPressEvent{Code: uv.KeyTab, Mod: mod}, true
	case tea.KeyBackspace: // DEL
		return uv.KeyPressEvent{Code: uv.KeyBackspace, Mod: mod}, true
	case tea.KeyEsc: // ESC; also ctrl+[
		return uv.KeyPressEvent{Code: uv.KeyEscape, Mod: mod}, true
	case tea.KeyNull: // ctrl+@ / ctrl+space → NUL
		return uv.KeyPressEvent{Code: uv.KeySpace, Mod: mod | uv.ModCtrl}, true
	}

	// Remaining positive types are control bytes: ctrl+a..z and the
	// bracket/caret/underscore chords. vt encodes them from letter+ModCtrl.
	if t > 0 && t < 32 {
		switch {
		case t >= 1 && t <= 26: // SOH..SUB → ctrl+a..z
			return uv.KeyPressEvent{Code: 'a' + rune(t) - 1, Mod: mod | uv.ModCtrl}, true
		case t == 28: // FS → ctrl+backslash
			return uv.KeyPressEvent{Code: '\\', Mod: mod | uv.ModCtrl}, true
		case t == 29: // GS → ctrl+]
			return uv.KeyPressEvent{Code: ']', Mod: mod | uv.ModCtrl}, true
		case t == 30: // RS → ctrl+^
			return uv.KeyPressEvent{Code: '^', Mod: mod | uv.ModCtrl}, true
		case t == 31: // US → ctrl+_
			return uv.KeyPressEvent{Code: '_', Mod: mod | uv.ModCtrl}, true
		}
	}
	return uv.KeyPressEvent{}, false
}
