package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/1broseidon/ghostmux/internal/rail"
	"github.com/1broseidon/ghostmux/internal/state"
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

	m := newSolo(newTestViewport(t))
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
	if !strings.Contains(ansi.Strip(m.View()), "start group's ghosts") {
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
	for _, want := range []string{"settings", "Fleet", "Panel", "System", "section", "back"} {
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
	store, err := state.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveSettings(store, rail.Settings{Toggle: []string{"ctrl+j"}}); err != nil {
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

// openPanelFields lands on Panel with the field cursor active (field 0 =
// toggle). Multi-field sections need two Enters: section, then field.
func openPanelFields(m soloModel) soloModel {
	m = send(m, key(","))
	m = send(m, key("j")) // Agents
	m = send(m, key("j")) // Panel
	m = send(m, key("enter"))
	return m
}

// TestKeysFieldIsReadOnlyUnderEnv: a field the user cannot change says why,
// rather than accepting a rebind it would then discard.
func TestKeysFieldIsReadOnlyUnderEnv(t *testing.T) {
	t.Setenv("GHOSTMUX_TOGGLE", "f9")
	m := openPanelFields(newChromeSolo(t))
	m = send(m, key("enter")) // ↵ on the toggle field
	if m.settings.capture {
		t.Errorf("capture started while GHOSTMUX_TOGGLE decides the binding")
	}
	if m.settings.msg != "GHOSTMUX_TOGGLE is set; unset it to change this setting" {
		t.Errorf("read-only field message = %q", m.settings.msg)
	}
	if !strings.Contains(ansi.Strip(m.View()), "GHOSTMUX_TOGGLE overrides the saved setting") {
		t.Errorf("the pane does not show environment precedence")
	}
}

// TestCaptureRebindsAppliesAndPersists: saved and applied are one path, so a
// new toggle works immediately and survives the next launch.
func TestCaptureRebindsAppliesAndPersists(t *testing.T) {
	t.Setenv("GHOSTMUX_TOGGLE", "")
	m := openPanelFields(newChromeSolo(t))
	m = send(m, key("enter"))
	if !m.settings.capture {
		t.Fatal("↵ on the toggle field did not start capture")
	}
	m = send(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.settings.capture {
		t.Errorf("capture did not end on the next key")
	}
	if m.settings.msg != "toggle key: ctrl+j" {
		t.Errorf("toggle success message = %q", m.settings.msg)
	}
	if !m.toggles["ctrl+j"] || m.toggleLabel != "ctrl+j" {
		t.Errorf("the new toggle is not live: %v / %q", m.toggles, m.toggleLabel)
	}
	if got := settingsFromStore(m.store).Toggle; len(got) != 1 || got[0] != "ctrl+j" {
		t.Errorf("the new toggle was not persisted: %v", got)
	}
	m = send(m, key("esc")) // leave fields
	m = send(m, key("esc")) // close settings
	if m2 := send(m, tea.KeyMsg{Type: tea.KeyCtrlJ}); m2.focus != focusViewport {
		t.Errorf("the rebound toggle does not move focus")
	}
}

// TestCaptureEscCancels: esc during capture leaves the binding alone.
func TestCaptureEscCancels(t *testing.T) {
	t.Setenv("GHOSTMUX_TOGGLE", "")
	m := openPanelFields(newChromeSolo(t))
	m = send(m, key("enter"))
	m = send(m, key("esc"))
	if len(settingsFromStore(m.store).Toggle) != 0 {
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
	m := openPanelFields(newChromeSolo(t))
	m = send(m, key("j")) // rail width
	m = send(m, key("enter"))
	if !m.settings.editing {
		t.Fatal("↵ on the width field did not open the editor")
	}
	m.settings.input.SetValue("999")
	m = send(m, key("enter"))

	if rail.Width() != 60 {
		t.Errorf("width = %d, want the 60 clamp", rail.Width())
	}
	if got := settingsFromStore(m.store).RailWidth; got != 60 {
		t.Errorf("clamped width not persisted: %d", got)
	}
	vw, _ := m.viewportSize()
	if vw != 120-60-dividerCol {
		t.Errorf("viewport was not resized with the rail: %d", vw)
	}
	if m.settings.msg != "rail width: 60 (clamped to 20-60)" {
		t.Errorf("clamped edit message = %q", m.settings.msg)
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
	m = send(m, key("j")) // Agents

	m = send(m, key("enter"))
	m.settings.input.SetValue("MyBot") // lowercased on the way in
	m = send(m, key("enter"))
	if got := rail.ExtraAgentCmds(); len(got) != 1 || got[0] != "mybot" {
		t.Fatalf("agent not added: %v", got)
	}
	if got := settingsFromStore(m.store).Agents; len(got) != 1 || got[0] != "mybot" {
		t.Errorf("agent not persisted: %v", got)
	}

	m.settings.editing = true
	m.settings.input = settingsInput("mybot")
	m = send(m, key("enter"))
	if got := rail.ExtraAgentCmds(); len(got) != 0 {
		t.Errorf("entering an existing extra did not remove it: %v", got)
	}
	if got := settingsFromStore(m.store).Agents; len(got) != 0 {
		t.Errorf("removal not persisted: %v", got)
	}

	// A built-in is never removable: ghostmux can see it either way, and
	// pretending otherwise would be a setting that does nothing.
	m.settings.editing = true
	m.settings.input = settingsInput("claude")
	m = send(m, key("enter"))
	if !m.settings.msgErr || m.settings.msg != "claude is built in and cannot be removed" {
		t.Errorf("built-in refusal = %q err=%v", m.settings.msg, m.settings.msgErr)
	}
}

// TestFleetDirSettingsCycleAndApply: Fleet owns the two dir policies; ↵ on
// each field flips it and the next create/capture uses the new mode.
func TestFleetDirSettingsCycleAndApply(t *testing.T) {
	m := newChromeSolo(t)
	t.Cleanup(func() {
		rail.SetGhostDir("")
		rail.SetCreateDir("")
	})
	m = send(m, key(","))
	if section(m.settings.cursor) != secFleet {
		t.Fatalf("cursor = %d, want Fleet", m.settings.cursor)
	}
	m = send(m, key("enter")) // enter fields
	m = send(m, key("enter")) // cycle ghost dir
	if settingsFromStore(m.store).GhostDir != rail.GhostDirLast {
		t.Fatalf("first cycle did not persist last: %+v", settingsFromStore(m.store))
	}
	if rail.GhostDir() != rail.GhostDirLast {
		t.Fatalf("first cycle did not apply live mode: %q", rail.GhostDir())
	}
	if m.settings.msg != "ghost dir: last working directory" {
		t.Errorf("cycle message = %q", m.settings.msg)
	}
	m = send(m, key("enter"))
	if settingsFromStore(m.store).GhostDir != "" {
		t.Fatalf("second cycle did not clear to default launch: %+v", settingsFromStore(m.store))
	}
	if rail.GhostDir() != rail.GhostDirLaunch {
		t.Fatalf("second cycle live mode = %q, want launch", rail.GhostDir())
	}

	m = send(m, key("j")) // new session dir
	m = send(m, key("enter"))
	if settingsFromStore(m.store).CreateDir != rail.CreateDirCurrent {
		t.Fatalf("create dir cycle did not persist current: %+v", settingsFromStore(m.store))
	}
	if rail.CreateDir() != rail.CreateDirCurrent {
		t.Fatalf("create dir live mode = %q, want current", rail.CreateDir())
	}
	if m.settings.msg != "new session dir: current session's cwd" {
		t.Errorf("create dir message = %q", m.settings.msg)
	}
}

// TestSystemShowsOnlyProvableFacts: backends, state, and about are one
// diagnostic surface — evidence only, never inference.
func TestSystemShowsOnlyProvableFacts(t *testing.T) {
	got := buildVersion()
	if got == "" {
		t.Fatal("buildVersion() is empty")
	}
	m := newChromeSolo(t)
	m = send(m, key(","))
	m = send(m, key("G")) // System
	if section(m.settings.cursor) != secSystem {
		t.Fatalf("cursor = %d, want System", m.settings.cursor)
	}
	if m.settings.backends == nil {
		t.Fatal("backends were not probed on section entry")
	}
	if m.settings.state == nil {
		t.Fatal("state file was not read on section entry")
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
	plain := ansi.Strip(m.View())
	detail := ansi.Strip(strings.Join(stateDetail(m.settings.state), "\n"))
	if !strings.Contains(detail, m.store.Path()) {
		t.Errorf("System does not name the state file: %s", detail)
	}
	if m.settings.state.Exists {
		t.Errorf("a fresh state dir reported an existing file")
	}
	if !strings.Contains(plain, "created when settings or groups are first saved") {
		t.Errorf("a missing file was not reported as missing: %s", plain)
	}
	if !strings.Contains(plain, got) {
		t.Errorf("System does not show the build version %q", got)
	}
	if !strings.Contains(plain, "The tmux fleet navigator") {
		t.Errorf("System does not show the technical description: %s", plain)
	}
	if strings.Contains(plain, "laws") || strings.Contains(plain, "mission control") {
		t.Errorf("System retained removed product-language copy: %s", plain)
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
	if w := ansi.StringWidth(strings.Split(m.statusLine(120), "\n")[0]); w != 120 {
		t.Errorf("settings bar width = %d, want 120", w)
	}
}

func corruptPrimaryAfterOpen(t *testing.T, m soloModel) []byte {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(m.store.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	invalid := []byte("{invalid state")
	if err := os.WriteFile(m.store.Path(), invalid, 0o644); err != nil {
		t.Fatal(err)
	}
	return invalid
}

func TestSettingsSaveBeforeApplyingLiveBehavior(t *testing.T) {
	t.Run("toggle", func(t *testing.T) {
		t.Setenv("GHOSTMUX_TOGGLE", "")
		m := newChromeSolo(t)
		oldLabel := m.toggleLabel
		invalid := corruptPrimaryAfterOpen(t, m)
		m = openPanelFields(m)
		m = send(m, key("enter"))
		m = send(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
		if m.toggleLabel != oldLabel || m.toggles["ctrl+j"] {
			t.Fatalf("failed save changed live toggle: %q %v", m.toggleLabel, m.toggles)
		}
		if !m.settings.msgErr || !strings.Contains(m.settings.msg, "state save failed") {
			t.Fatalf("toggle save failure not shown: %q", m.settings.msg)
		}
		if got, _ := os.ReadFile(m.store.Path()); string(got) != string(invalid) {
			t.Fatalf("invalid primary was overwritten: %q", got)
		}
	})

	t.Run("width", func(t *testing.T) {
		rail.SetWidth(30)
		m := newChromeSolo(t)
		oldWidth := rail.Width()
		invalid := corruptPrimaryAfterOpen(t, m)
		m = openPanelFields(m)
		m = send(m, key("j")) // width field
		m = send(m, key("enter"))
		m.settings.input.SetValue("50")
		m = send(m, key("enter"))
		if rail.Width() != oldWidth {
			t.Fatalf("failed save changed live width: %d -> %d", oldWidth, rail.Width())
		}
		if !m.settings.msgErr || !strings.Contains(m.settings.msg, "state save failed") {
			t.Fatalf("width save failure not shown: %q", m.settings.msg)
		}
		if got, _ := os.ReadFile(m.store.Path()); string(got) != string(invalid) {
			t.Fatalf("invalid primary was overwritten: %q", got)
		}
	})

	t.Run("agent", func(t *testing.T) {
		rail.SetExtraAgentCmds(nil)
		t.Cleanup(func() { rail.SetExtraAgentCmds(nil) })
		m := newChromeSolo(t)
		invalid := corruptPrimaryAfterOpen(t, m)
		m = send(m, key(","))
		m = send(m, key("j")) // Agents
		m = send(m, key("enter"))
		m.settings.input.SetValue("mybot")
		m = send(m, key("enter"))
		if len(rail.ExtraAgentCmds()) != 0 {
			t.Fatalf("failed save changed live agents: %v", rail.ExtraAgentCmds())
		}
		if !m.settings.msgErr || !strings.Contains(m.settings.msg, "state save failed") {
			t.Fatalf("agent save failure not shown: %q", m.settings.msg)
		}
		if got, _ := os.ReadFile(m.store.Path()); string(got) != string(invalid) {
			t.Fatalf("invalid primary was overwritten: %q", got)
		}
	})
}

func TestSettingsConflictAdoptsAllSettingsAndRetryPreservesExternalAgent(t *testing.T) {
	t.Setenv("GHOSTMUX_TOGGLE", "")
	rail.SetExtraAgentCmds(nil)
	t.Cleanup(func() { rail.SetExtraAgentCmds(nil) })
	m := newChromeSolo(t)
	external, err := state.Open(m.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	externalSettings := state.Settings{
		Toggle: []string{"ctrl+x"}, RailWidth: 44, Agents: []string{"externalbot"},
	}
	if err := external.Update(func(doc *state.Document) error {
		doc.Groups = []state.Group{{Name: "external"}}
		doc.Settings = &externalSettings
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	m = send(m, key(","))
	m = send(m, key("j")) // Agents
	staleInfo := m.store.Info()
	m.settings.state = &staleInfo
	m = send(m, key("enter"))
	m.settings.input.SetValue("localbot")
	m = send(m, key("enter"))
	if m.settings.msg != "state changed in another panel; change not saved" || !m.settings.msgErr {
		t.Fatalf("conflict message = %q err=%v", m.settings.msg, m.settings.msgErr)
	}
	if m.settings.state != nil {
		t.Fatal("conflict adoption did not invalidate cached State info")
	}
	adopted := settingsFromStore(m.store)
	if got := adopted.Agents; len(got) != 1 || got[0] != "externalbot" {
		t.Fatalf("failed local mutation changed adopted agents: %v", got)
	}
	if adopted.RailWidth != 44 || len(adopted.Toggle) != 1 || adopted.Toggle[0] != "ctrl+x" {
		t.Fatalf("Store did not adopt external settings: %+v", adopted)
	}
	if rail.Width() != 44 {
		t.Fatalf("external width was not activated: %d", rail.Width())
	}
	if got := rail.ExtraAgentCmds(); len(got) != 1 || got[0] != "externalbot" {
		t.Fatalf("external agents were not activated: %v", got)
	}
	if m.toggleLabel != "ctrl+x" || !m.toggles["ctrl+x"] || m.toggles[defaultToggles[0]] {
		t.Fatalf("external toggle was not activated: %q %v", m.toggleLabel, m.toggles)
	}
	if vw, _ := m.viewportSize(); vw != 120-44-dividerCol {
		t.Fatalf("external width did not resize layout/PTY path: viewport width %d", vw)
	}
	if !strings.Contains(ansi.Strip(m.rail.View()), "external") {
		t.Fatalf("rail did not adopt Store snapshot after conflict: %s", ansi.Strip(m.rail.View()))
	}

	m = send(m, key("enter"))
	if !m.settings.editing {
		t.Fatal("retry did not reopen the agent editor")
	}
	m.settings.input.SetValue("localbot")
	m = send(m, key("enter"))
	if m.settings.msg != "agent added: localbot" || m.settings.msgErr {
		t.Fatalf("retry message = %q err=%v", m.settings.msg, m.settings.msgErr)
	}
	got := settingsFromStore(m.store)
	if len(got.Agents) != 2 || got.Agents[0] != "externalbot" || got.Agents[1] != "localbot" {
		t.Fatalf("retry erased the external agent: %v", got.Agents)
	}
	if got.RailWidth != 44 || len(got.Toggle) != 1 || got.Toggle[0] != "ctrl+x" {
		t.Fatalf("retry erased other external settings: %+v", got)
	}
}

func TestSuccessfulSettingsWritesInvalidateCachedStateInfo(t *testing.T) {
	tests := []struct {
		name  string
		write func(soloModel) soloModel
	}{
		{name: "toggle", write: func(m soloModel) soloModel {
			next, _ := m.captureToggle(tea.KeyMsg{Type: tea.KeyCtrlJ})
			return next.(soloModel)
		}},
		{name: "width", write: func(m soloModel) soloModel {
			next, _ := m.applyWidth("41")
			return next.(soloModel)
		}},
		{name: "agent", write: func(m soloModel) soloModel {
			next, _ := m.applyAgent("cachebot")
			return next.(soloModel)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GHOSTMUX_TOGGLE", "")
			rail.SetExtraAgentCmds(nil)
			t.Cleanup(func() { rail.SetExtraAgentCmds(nil) })
			m := newChromeSolo(t)
			m = send(m, key(","))
			cached := m.store.Info()
			m.settings.state = &cached
			m = test.write(m)
			if m.settings.state != nil {
				t.Fatal("successful settings write retained cached State info")
			}
			m.settings.cursor = int(secSystem)
			m.settings.enter()
			if m.settings.state == nil || m.settings.state.Status != state.StatusValid {
				t.Fatalf("State info was not refreshed after write: %+v", m.settings.state)
			}
		})
	}
}

func TestToggleSourceUsesExplicitStoreSettingEvenWhenItEqualsDefault(t *testing.T) {
	t.Setenv("GHOSTMUX_TOGGLE", "")
	m := newChromeSolo(t)
	cfg := settingsFromStore(m.store)
	cfg.Toggle = append([]string(nil), defaultToggles...)
	if err := saveSettings(m.store, cfg); err != nil {
		t.Fatal(err)
	}
	m = m.openSettings()
	m.settings.cursor = int(secPanel)
	m.settings.enter()
	plain := ansi.Strip(strings.Join(m.panelDetail(), "\n"))
	if !strings.Contains(plain, "source") || !strings.Contains(plain, "saved setting") {
		t.Fatalf("default-valued explicit toggle reported the wrong source: %s", plain)
	}
}

func TestStartupStateErrorRemainsVisibleAndReadOnly(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	path := filepath.Join(dir, "ghostmux", "groups.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	invalid := []byte("{broken")
	if err := os.WriteFile(path, invalid, 0o644); err != nil {
		t.Fatal(err)
	}
	orig := tmux.Runner
	tmux.Runner = func(args ...string) (string, error) { return "", nil }
	t.Cleanup(func() { tmux.Runner = orig })
	store, err := state.OpenDefault()
	if err == nil {
		t.Fatal("corrupt startup primary loaded without error")
	}
	m := newSolo(newTestViewport(t), store)
	if !strings.Contains(ansi.Strip(m.rail.View()), "state read-only: corrupt") {
		t.Fatalf("startup error not visible in rail: %s", ansi.Strip(m.rail.View()))
	}
	m = send(m, tea.WindowSizeMsg{Width: 120, Height: 32})
	m = send(m, key(","))
	m = send(m, key("G")) // System
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, "status  corrupt") || !strings.Contains(plain, "state is read-only") {
		t.Fatalf("System detail does not report corruption: %s", plain)
	}
	m = send(m, key("g"))     // Fleet
	m = send(m, key("j"))     // Agents
	m = send(m, key("j"))     // Panel
	m = send(m, key("enter")) // enter fields
	m = send(m, key("enter")) // toggle field
	m = send(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if !m.settings.msgErr {
		t.Fatal("write attempt against startup corruption was not reported")
	}
	if got, _ := os.ReadFile(path); string(got) != string(invalid) {
		t.Fatalf("startup corruption was cleared by write attempt: %q", got)
	}
}

func TestSettingsTechnicalCopy(t *testing.T) {
	m := newChromeSolo(t)
	m = send(m, key(","))
	if got := ansi.Strip(strings.Join(m.fleetDetail(), "\n")); !strings.Contains(got, "ghost dir") || !strings.Contains(got, "new session dir") {
		t.Errorf("fleet copy missing: %s", got)
	}
	m.settings.cursor = int(secPanel)
	m.settings.inFields = true
	m.settings.field = 0
	m.settings.capture = true
	if got := ansi.Strip(strings.Join(m.panelDetail(), "\n")); !strings.Contains(got, "press new toggle key; esc cancels") {
		t.Errorf("capture copy missing: %s", got)
	}
	m.settings.capture = false
	if got := ansi.Strip(strings.Join(m.panelDetail(), "\n")); !strings.Contains(got, "width applies immediately") {
		t.Errorf("panel width copy missing: %s", got)
	}
	if got := ansi.Strip(strings.Join(m.agentsDetail(90), "\n")); !strings.Contains(got, "enter a command name to add or remove it") || !strings.Contains(got, "built-ins cannot be removed") {
		t.Errorf("agent copy missing: %s", got)
	}
	facts := []backendFact{{name: "tmux", installed: true, path: "/bin/tmux", version: "tmux 3"}}
	if got := ansi.Strip(strings.Join(backendsDetail(facts), "\n")); !strings.Contains(got, "versions reported by installed binaries") {
		t.Errorf("backend copy missing: %s", got)
	}
	validInfo := &state.Info{
		Path: "/tmp/groups.json", Status: state.StatusValid, Version: 1,
		BackupPath: "/tmp/groups.json.bak", BackupStatus: state.StatusLegacy,
	}
	if got := ansi.Strip(strings.Join(stateDetail(validInfo), "\n")); !strings.Contains(got, "saved file contents") || !strings.Contains(got, "backup is retained but not restored automatically") {
		t.Errorf("state copy missing: %s", got)
	}
	m.settings.backends = facts
	m.settings.state = validInfo
	if got := ansi.Strip(strings.Join(m.systemDetail(), "\n")); !strings.Contains(got, "The tmux fleet navigator") {
		t.Errorf("system about copy missing: %s", got)
	}
}

func TestRecoveryRequiredStateDetailIsActionable(t *testing.T) {
	info := &state.Info{
		Path:          "/tmp/groups.json",
		Status:        state.StatusRecoveryRequired,
		Error:         "restore the backup to the primary path or remove the backup deliberately",
		BackupPath:    "/tmp/groups.json.bak",
		BackupExists:  true,
		BackupStatus:  state.StatusValid,
		BackupVersion: 1,
	}
	plain := ansi.Strip(strings.Join(stateDetail(info), "\n"))
	for _, want := range []string{"status  recovery required", "state is read-only", "restore the backup", "remove the backup deliberately"} {
		if !strings.Contains(plain, want) {
			t.Errorf("recovery State detail missing %q: %s", want, plain)
		}
	}
}

func TestStateStatusTextDistinguishesStorageConditions(t *testing.T) {
	for _, test := range []struct {
		status string
		want   string
	}{
		{state.StatusMissing, "missing"},
		{state.StatusValid, "valid (schema version 1)"},
		{state.StatusLegacy, "legacy (unversioned)"},
		{state.StatusCorrupt, "corrupt"},
		{state.StatusUnreadable, "unreadable"},
		{state.StatusUnsupported, "unsupported schema version"},
		{state.StatusRecoveryRequired, "recovery required"},
	} {
		if got := stateStatusText(test.status, 1); got != test.want {
			t.Errorf("stateStatusText(%q) = %q, want %q", test.status, got, test.want)
		}
	}
}
