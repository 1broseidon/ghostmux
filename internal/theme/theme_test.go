package theme

import "testing"

// TestPickFollowsMode: gruvbox hex by default, the terminal's own ANSI-16
// palette when the operator asks for it.
func TestPickFollowsMode(t *testing.T) {
	orig := ansi
	t.Cleanup(func() { ansi = orig })

	ansi = false
	if got := C("#fe8019", "9"); got != "#fe8019" {
		t.Fatalf("default mode = %q, want gruvbox hex", got)
	}
	ansi = true
	if got := C("#fe8019", "9"); got != "9" || !ANSI() {
		t.Fatalf("ansi mode = %q ANSI()=%v, want terminal palette index", got, ANSI())
	}
}

// TestTmuxConvertsSpellingOnly: hex passes through as-is, an ANSI-16 index
// gains tmux's "colour" prefix — the only conversion Tmux performs.
func TestTmuxConvertsSpellingOnly(t *testing.T) {
	if got := Tmux("#fe8019"); got != "#fe8019" {
		t.Fatalf("Tmux(hex) = %q, want unchanged hex", got)
	}
	if got := Tmux("9"); got != "colour9" {
		t.Fatalf("Tmux(ansi) = %q, want colour9", got)
	}
}
