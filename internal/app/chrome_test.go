package app

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/1broseidon/ghostmux/internal/rail"
	"github.com/1broseidon/ghostmux/internal/tmux"
)

// newChromeSolo builds a frame with tmux stubbed and its own state dir. Every
// test here gets a private XDG_STATE_HOME: this repo has already had one real
// state file clobbered by a test that wrote to the user's own groups.json.
func newChromeSolo(t *testing.T) soloModel {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) { return "", nil }
	t.Cleanup(func() { tmux.Runner = orig })
	width := rail.Width()
	t.Cleanup(func() { rail.SetWidth(width) })

	m := newSolo(newTestViewport())
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	return next.(soloModel)
}

func key(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func send(m soloModel, msg tea.Msg) soloModel {
	next, _ := m.Update(msg)
	return next.(soloModel)
}

// TestComposeKeepsBaseWidthAndOutsideContent is the overlay's whole contract:
// a spliced line must be exactly as wide as the line it replaced, or the
// divider zig-zags and the viewport grid shears the moment `?` is pressed.
func TestComposeKeepsBaseWidthAndOutsideContent(t *testing.T) {
	base := strings.Join([]string{
		strings.Repeat("a", 40),
		"\x1b[38;2;235;219;178m" + strings.Repeat("b", 40) + "\x1b[0m",
		strings.Repeat("c", 40),
		strings.Repeat("d", 40),
	}, "\n")
	box := []string{"\x1b[48;2;29;32;33mBOX-ONE\x1b[0m", "\x1b[48;2;29;32;33mBOX-TWO\x1b[0m"}

	got := compose(base, box, 10, 1)
	lines := strings.Split(got, "\n")
	baseLines := strings.Split(base, "\n")
	if len(lines) != len(baseLines) {
		t.Fatalf("compose changed the line count: %d, want %d", len(lines), len(baseLines))
	}
	for i, ln := range lines {
		if w := ansi.StringWidth(ln); w != 40 {
			t.Errorf("line %d width = %d, want 40 (%q)", i, w, ln)
		}
	}
	// Rows the box never touched are untouched, byte for byte.
	if lines[0] != baseLines[0] || lines[3] != baseLines[3] {
		t.Errorf("compose modified rows outside the box")
	}
	// On a spliced row, the plain text outside the box columns survives.
	plain := ansi.Strip(lines[2])
	if plain[:10] != strings.Repeat("c", 10) || plain[17:] != strings.Repeat("c", 23) {
		t.Errorf("content outside the box changed: %q", plain)
	}
	if plain[10:17] != "BOX-TWO" {
		t.Errorf("box content not spliced in: %q", plain)
	}
	if !strings.HasSuffix(lines[1], "\x1b[0m") {
		t.Errorf("spliced line does not end in a reset: %q", lines[1])
	}
}

// TestComposeIgnoresRowsOffTheFrame: a box taller than the terminal must draw
// what fits, never panic and never add lines the frame did not have.
func TestComposeIgnoresRowsOffTheFrame(t *testing.T) {
	base := "aaaa\nbbbb"
	got := compose(base, []string{"XX", "YY", "ZZ", "WW"}, 1, 1)
	if n := len(strings.Split(got, "\n")); n != 2 {
		t.Errorf("compose produced %d lines, want 2", n)
	}
}

// TestHelpBoxFitsEveryEntryAt56 is the regression for the finding that put
// help in an overlay at all: in the 30-column rail, 9 of 17 rows truncated.
// It runs against the real keymap, not a fixture, so adding a longer
// description fails here instead of quietly shipping an ellipsis.
func TestHelpBoxFitsEveryEntryAt56(t *testing.T) {
	box := helpBox(200) // frame wide enough that the 56-col cap is what applies
	if len(box) == 0 {
		t.Fatal("helpBox rendered nothing")
	}
	if w := ansi.StringWidth(box[0]); w != overlayMaxWidth {
		t.Fatalf("help box width = %d, want %d", w, overlayMaxWidth)
	}
	plain := ansi.Strip(strings.Join(box, "\n"))
	for _, e := range rail.HelpEntries() {
		if !strings.Contains(plain, e.Desc) {
			t.Errorf("help entry %q truncated or missing at width %d", e.Desc, overlayMaxWidth)
		}
		if !strings.Contains(plain, e.Key) {
			t.Errorf("help key %q missing", e.Key)
		}
	}
	if strings.Contains(plain, "…") {
		t.Errorf("help box truncated something at width %d:\n%s", overlayMaxWidth, plain)
	}
	for _, want := range []string{"ghostmux · keys", "bell", "ghost", "any key closes"} {
		if !strings.Contains(plain, want) {
			t.Errorf("help box missing %q", want)
		}
	}
	// Every line the same width, or the box's right edge would ripple. (The
	// per-line reset is compose's job and is asserted there — lipgloss emits
	// no escapes at all under a test's color profile.)
	for i, ln := range box {
		if w := ansi.StringWidth(ln); w != overlayMaxWidth {
			t.Errorf("box line %d width = %d, want %d", i, w, overlayMaxWidth)
		}
	}
}

// TestHelpOverlayKeepsFrameGeometry: the overlay is composited over a finished
// frame, so the frame's size must survive it exactly.
func TestHelpOverlayKeepsFrameGeometry(t *testing.T) {
	m := newChromeSolo(t)
	before := strings.Split(m.View(), "\n")
	m = send(m, key("?"))
	if !m.overlayHelp {
		t.Fatal("? did not open the overlay")
	}
	after := strings.Split(m.View(), "\n")
	if len(after) != len(before) {
		t.Fatalf("overlay changed the line count: %d, want %d", len(after), len(before))
	}
	for i, ln := range after {
		if w := ansi.StringWidth(ln); w != 120 {
			t.Errorf("overlay line %d width = %d, want 120", i, w)
		}
	}
	if !strings.Contains(ansi.Strip(m.View()), "start group's dead sessions") {
		t.Errorf("overlay is not showing the keymap")
	}
}

// TestAnyKeyClosesOverlayIncludingTheToggle: the toggle closes the overlay and
// does NOT also flip focus. A key that did two things at once would be one the
// user could not press to mean either.
func TestAnyKeyClosesOverlayIncludingTheToggle(t *testing.T) {
	for _, k := range []tea.KeyMsg{
		key("x"),
		{Type: tea.KeyEscape},
		{Type: tea.KeyCtrlBackslash, Alt: true},
	} {
		m := newChromeSolo(t)
		m = send(m, key("?"))
		if !m.overlayHelp {
			t.Fatal("? did not open the overlay")
		}
		m = send(m, k)
		if m.overlayHelp {
			t.Errorf("%q did not close the overlay", k.String())
		}
		if m.focus != focusRail {
			t.Errorf("%q closed the overlay AND flipped focus", k.String())
		}
	}
}

// TestMousePressClosesOverlay: the overlay covers the rail, so a click has
// nothing behind it to mean except "dismiss this".
func TestMousePressClosesOverlay(t *testing.T) {
	m := newChromeSolo(t)
	m = send(m, key("?"))
	m = send(m, tea.MouseMsg{X: 5, Y: 5, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft})
	if m.overlayHelp {
		t.Errorf("a mouse press did not close the overlay")
	}
}

// TestOverlayKeysAreNotStolenMidPrompt is the law the frame's interception
// rests on: while the rail is typing, `?` and `,` are characters. A reserved
// key that silently eats one is the worst failure a keymap can have.
func TestOverlayKeysAreNotStolenMidPrompt(t *testing.T) {
	m := newChromeSolo(t)
	m = send(m, key("/")) // rail enters filter mode
	if !m.rail.InPrompt() {
		t.Fatal("rail did not enter a prompt on /")
	}
	for _, k := range []string{"?", ","} {
		m = send(m, key(k))
		if m.overlayHelp || m.settings != nil {
			t.Fatalf("%q was stolen from the filter prompt", k)
		}
	}
	if got := ansi.Strip(m.rail.View()); !strings.Contains(got, "?,") {
		t.Errorf("filter input did not receive the keys: %q", got)
	}
}

// TestOverlayKeysBelongToTheChildWhenViewportFocused: every reserved key is
// one stolen from the program in the viewport, so `?` is not one of them.
func TestOverlayKeysBelongToTheChildWhenViewportFocused(t *testing.T) {
	m := newChromeSolo(t)
	m = m.setFocus(focusViewport)
	for _, k := range []string{"?", ","} {
		m = send(m, key(k))
		if m.overlayHelp || m.settings != nil {
			t.Errorf("%q opened frame chrome while the viewport had focus", k)
		}
		if m.focus != focusViewport {
			t.Errorf("%q moved focus away from the child", k)
		}
	}
}

// TestSettingsOpensAndEscRestoresTheExactFrame: settings is a mode, so the
// frame it replaces has to come back untouched — the viewport child kept
// running the whole time and its frame comes from the emulator's buffer.
func TestSettingsOpensAndEscRestoresTheExactFrame(t *testing.T) {
	m := newChromeSolo(t)
	before := m.View()

	m = send(m, key(","))
	if m.settings == nil {
		t.Fatal(", did not open settings")
	}
	settingsView := ansi.Strip(m.View())
	for _, want := range []string{"settings", "Keys", "Backends", "About", "section", "back"} {
		if !strings.Contains(settingsView, want) {
			t.Errorf("settings view missing %q", want)
		}
	}
	if m.settings != nil && strings.Contains(settingsView, "no sessions yet") {
		t.Errorf("settings still rendered the rail body")
	}

	m = send(m, key("esc"))
	if m.settings != nil {
		t.Fatal("esc did not close settings")
	}
	if got := m.View(); got != before {
		t.Errorf("esc did not restore the exact prior frame")
	}
}

// TestSettingsClosePathsAgree: esc, q and `,` all go through closeSettings, so
// there is no way to leave the mode half-open.
func TestSettingsClosePathsAgree(t *testing.T) {
	for _, k := range []tea.KeyMsg{key("esc"), key("q"), key(",")} {
		m := newChromeSolo(t)
		m = send(m, key(","))
		m = send(m, k)
		if m.settings != nil {
			t.Errorf("%q did not close settings", k.String())
		}
	}
}

// TestToggleIsInertInSettings: there is no viewport on screen to hand the
// keyboard to, so the toggle must not move focus behind the mode's back.
func TestToggleIsInertInSettings(t *testing.T) {
	m := newChromeSolo(t)
	m = send(m, key(","))
	m = send(m, tea.KeyMsg{Type: tea.KeyCtrlBackslash, Alt: true})
	if m.focus != focusRail {
		t.Errorf("the toggle flipped focus while settings was open")
	}
	if m.settings == nil {
		t.Errorf("the toggle closed settings")
	}
}

// TestSettingsKeepsTheFleetLive: ticks and refreshes still reach the rail
// while settings is open, so the bar's attention counts stay true.
func TestSettingsKeepsTheFleetLive(t *testing.T) {
	m := newChromeSolo(t)
	m = send(m, key(","))
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(soloModel)
	if m.settings == nil {
		t.Errorf("a non-key message closed settings")
	}
	if m.w != 100 {
		t.Errorf("non-key messages stopped being handled in settings")
	}
}

// TestTogglePrecedenceEnvBeatsFileBeatsDefault: env is last because it is the
// layer a user reaches for when the panel is already broken — a stored setting
// must never be able to override the escape hatch.
func TestTogglePrecedenceEnvBeatsFileBeatsDefault(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("GHOSTMUX_TOGGLE", "")

	if got := toggleKeys(); len(got) != len(defaultToggles) || got[0] != defaultToggles[0] {
		t.Errorf("with nothing set, toggleKeys() = %v, want the defaults", got)
	}
	if err := rail.SaveSettings(rail.Settings{Toggle: []string{"ctrl+j"}}); err != nil {
		t.Fatal(err)
	}
	if got := toggleKeys(); len(got) != 1 || got[0] != "ctrl+j" {
		t.Errorf("state file did not beat the default: %v", got)
	}
	if toggleEnvLocked() {
		t.Errorf("a state-file binding must not read as env-locked")
	}
	t.Setenv("GHOSTMUX_TOGGLE", "f9")
	if got := toggleKeys(); len(got) != 1 || got[0] != "f9" {
		t.Errorf("env did not beat the state file: %v", got)
	}
	if !toggleEnvLocked() {
		t.Errorf("GHOSTMUX_TOGGLE set but the field does not report as locked")
	}
}

// TestKeysFieldIsReadOnlyUnderEnv: a field the user cannot change says why,
// rather than accepting a rebind it would then discard.
func TestKeysFieldIsReadOnlyUnderEnv(t *testing.T) {
	t.Setenv("GHOSTMUX_TOGGLE", "f9")
	m := newChromeSolo(t)
	m = send(m, key(","))
	m = send(m, key("enter")) // ↵ on the Keys section
	if m.settings.capture {
		t.Errorf("capture started while GHOSTMUX_TOGGLE decides the binding")
	}
	if !strings.Contains(m.settings.msg, "GHOSTMUX_TOGGLE") {
		t.Errorf("read-only field did not say why: %q", m.settings.msg)
	}
	if !strings.Contains(ansi.Strip(m.View()), "read-only") {
		t.Errorf("the pane does not show the field as read-only")
	}
}

// TestCaptureRebindsAppliesAndPersists: saved and applied are one path, so a
// new toggle works immediately and survives the next launch.
func TestCaptureRebindsAppliesAndPersists(t *testing.T) {
	t.Setenv("GHOSTMUX_TOGGLE", "")
	m := newChromeSolo(t)
	m = send(m, key(","))
	m = send(m, key("enter"))
	if !m.settings.capture {
		t.Fatal("↵ on Keys did not start capture")
	}
	m = send(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.settings.capture {
		t.Errorf("capture did not end on the next key")
	}
	if !m.toggles["ctrl+j"] || m.toggleLabel != "ctrl+j" {
		t.Errorf("the new toggle is not live: %v / %q", m.toggles, m.toggleLabel)
	}
	if got := rail.LoadSettings().Toggle; len(got) != 1 || got[0] != "ctrl+j" {
		t.Errorf("the new toggle was not persisted: %v", got)
	}
	m = send(m, key("esc"))
	if m2 := send(m, tea.KeyMsg{Type: tea.KeyCtrlJ}); m2.focus != focusViewport {
		t.Errorf("the rebound toggle does not move focus")
	}
}

// TestCaptureEscCancels: esc during capture leaves the binding alone.
func TestCaptureEscCancels(t *testing.T) {
	t.Setenv("GHOSTMUX_TOGGLE", "")
	m := newChromeSolo(t)
	m = send(m, key(","))
	m = send(m, key("enter"))
	m = send(m, key("esc"))
	if len(rail.LoadSettings().Toggle) != 0 {
		t.Errorf("esc during capture still wrote a binding")
	}
	if m.toggleLabel != defaultToggles[0] {
		t.Errorf("esc during capture changed the live binding: %q", m.toggleLabel)
	}
}

// TestWidthEditClampsSavesAndResizesEverything: a width that resized the rail
// but not the child's pty would leave every program wrapping at the wrong
// column, so the edit re-runs the whole resize path.
func TestWidthEditClampsSavesAndResizesEverything(t *testing.T) {
	m := newChromeSolo(t)
	m = send(m, key(","))
	m = send(m, key("j")) // Rail
	m = send(m, key("enter"))
	if !m.settings.editing {
		t.Fatal("↵ on Rail did not open the width editor")
	}
	m.settings.input.SetValue("999")
	m = send(m, key("enter"))

	if rail.Width() != 60 {
		t.Errorf("width = %d, want the 60 clamp", rail.Width())
	}
	if got := rail.LoadSettings().RailWidth; got != 60 {
		t.Errorf("clamped width not persisted: %d", got)
	}
	vw, _ := m.viewportSize()
	if vw != 120-60-dividerCol {
		t.Errorf("viewport was not resized with the rail: %d", vw)
	}
	if !strings.Contains(m.settings.msg, "clamped") {
		t.Errorf("a clamped edit did not say so: %q", m.settings.msg)
	}

	m.settings.editing = true
	m.settings.input = settingsInput("not a number")
	m = send(m, key("enter"))
	if rail.Width() != 60 {
		t.Errorf("a bad value changed the width")
	}
	if !m.settings.msgErr {
		t.Errorf("a bad value was not reported as an error")
	}
}

// TestAgentsEditIsAToggle: one key, add-or-remove, because the pane states the
// rule and an add/remove pair would be two keys for a three-name list.
func TestAgentsEditIsAToggle(t *testing.T) {
	m := newChromeSolo(t)
	t.Cleanup(func() { rail.RemoveAgentCmd("mybot") })
	m = send(m, key(","))
	m = send(m, key("j"))
	m = send(m, key("j")) // Agents

	m = send(m, key("enter"))
	m.settings.input.SetValue("MyBot") // lowercased on the way in
	m = send(m, key("enter"))
	if got := rail.ExtraAgentCmds(); len(got) != 1 || got[0] != "mybot" {
		t.Fatalf("agent not added: %v", got)
	}
	if got := rail.LoadSettings().Agents; len(got) != 1 || got[0] != "mybot" {
		t.Errorf("agent not persisted: %v", got)
	}

	m.settings.editing = true
	m.settings.input = settingsInput("mybot")
	m = send(m, key("enter"))
	if got := rail.ExtraAgentCmds(); len(got) != 0 {
		t.Errorf("entering an existing extra did not remove it: %v", got)
	}
	if got := rail.LoadSettings().Agents; len(got) != 0 {
		t.Errorf("removal not persisted: %v", got)
	}

	// A built-in is never removable: ghostmux can see it either way, and
	// pretending otherwise would be a setting that does nothing.
	m.settings.editing = true
	m.settings.input = settingsInput("claude")
	m = send(m, key("enter"))
	if !m.settings.msgErr {
		t.Errorf("removing a built-in was not refused")
	}
}

// TestBackendsAndStateShowOnlyProvableFacts: the evidence law, in the two
// sections most tempted to guess.
func TestBackendsAndStateShowOnlyProvableFacts(t *testing.T) {
	m := newChromeSolo(t)
	m = send(m, key(","))
	for range 3 {
		m = send(m, key("j")) // Backends
	}
	if m.settings.backends == nil {
		t.Fatal("backends were not probed on section entry")
	}
	probed := m.settings.backends
	m = send(m, key("k"))
	m = send(m, key("j"))
	if &probed[0] != &m.settings.backends[0] {
		t.Errorf("backends were re-probed on re-entry, not cached")
	}
	for _, f := range m.settings.backends {
		if !f.installed && f.version != "" {
			t.Errorf("%s is not installed but reported a version %q", f.name, f.version)
		}
	}

	m = send(m, key("j")) // State
	if m.settings.state == nil {
		t.Fatal("state file was not read on section entry")
	}
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "ghostmux/groups.json") {
		t.Errorf("State does not name the file: %s", plain)
	}
	if m.settings.state.Exists {
		t.Errorf("a fresh state dir reported an existing file")
	}
	if !strings.Contains(plain, "not created yet") {
		t.Errorf("a missing file was not reported as missing: %s", plain)
	}
}

// TestAboutVersionIsNeverInvented: a build with no stamp says "dev build"
// rather than a number nobody can check.
func TestAboutVersionIsNeverInvented(t *testing.T) {
	got := buildVersion()
	if got == "" {
		t.Fatal("buildVersion() is empty")
	}
	m := newChromeSolo(t)
	m = send(m, key(","))
	for range 5 {
		m = send(m, key("j")) // About
	}
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, got) {
		t.Errorf("About does not show the build version %q", got)
	}
	for _, law := range []string{"evidence, never inference", "ship only what the multiplexer"} {
		if !strings.Contains(plain, law) {
			t.Errorf("About does not state the law %q", law)
		}
	}
}

// TestSettingsBarShowsItsOwnKeys: the bar follows the mode, and never
// advertises a key that does nothing here.
func TestSettingsBarShowsItsOwnKeys(t *testing.T) {
	m := newChromeSolo(t)
	m = send(m, key(","))
	bar := ansi.Strip(m.statusLine(120))
	for _, want := range []string{"section", "edit", "back"} {
		if !strings.Contains(bar, want) {
			t.Errorf("settings bar missing %q: %q", want, bar)
		}
	}
	if strings.Contains(bar, "kill") {
		t.Errorf("settings bar still advertises rail keys: %q", bar)
	}
	if w := ansi.StringWidth(m.statusLine(120)); w != 120 {
		t.Errorf("settings bar width = %d, want 120", w)
	}
}
