// Package theme selects between ghostmux's engraved gruvbox palette and the
// viewer's own terminal palette. The Mac sessions proved the law twice: the
// frame may not assume the viewer's terminal theme. Gruvbox stays the default
// identity; GHOSTMUX_THEME=ansi swaps every color for an ANSI-16 index, so
// the whole panel renders in whatever palette the terminal was themed with —
// every screenshot is the operator's ghostmux, not ours.
//
// The mode is read from the environment in init, before any dependent
// package's color vars initialize — which is why it is env-only for now: a
// settings-driven switch would need every color to become a lookup.
package theme

import "os"

var ansi = os.Getenv("GHOSTMUX_THEME") == "ansi"

// C picks the color for one role: the gruvbox hex by default, the ANSI-16
// index (a string lipgloss accepts, "0".."15") when the terminal's own
// palette is in charge.
func C(hex, ansi16 string) string {
	if ansi {
		return ansi16
	}
	return hex
}

// ANSI reports whether the terminal-palette mode is active — for the one or
// two places that render differently rather than just recolor.
func ANSI() bool { return ansi }
