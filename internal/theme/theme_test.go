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
