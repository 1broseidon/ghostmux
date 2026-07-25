package rail

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestSettingsRoundTripAndCoexistWithGroups: settings and groups share one
// file with two owners, so each save has to leave the other half alone.
func TestSettingsRoundTripAndCoexistWithGroups(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	want := Settings{Toggle: []string{"ctrl+j"}, RailWidth: 42, Agents: []string{"mybot"}}
	if err := SaveSettings(want); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	got := LoadSettings()
	if len(got.Toggle) != 1 || got.Toggle[0] != "ctrl+j" || got.RailWidth != 42 ||
		len(got.Agents) != 1 || got.Agents[0] != "mybot" {
		t.Fatalf("round trip lost data: %+v", got)
	}

	// A rail save must not erase settings…
	if err := saveState([]Group{{Name: "work", Members: []string{"tmux:api"}}}, map[string]bool{"grp:work": true}, nil); err != nil {
		t.Fatal(err)
	}
	if LoadSettings().RailWidth != 42 {
		t.Errorf("a groups save erased the settings half of the file")
	}
	// …and a settings save must not erase groups.
	if err := SaveSettings(Settings{RailWidth: 24}); err != nil {
		t.Fatal(err)
	}
	groups, collapsed, _ := loadState()
	if len(groups) != 1 || groups[0].Name != "work" || !collapsed["grp:work"] {
		t.Errorf("a settings save erased groups/folds: %+v %v", groups, collapsed)
	}
}

// TestOldStateFileLoadsUnchanged: a file written before settings existed is
// the normal case for every current user, and it must load as-is.
func TestOldStateFileLoadsUnchanged(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	if err := os.MkdirAll(dir+"/ghostmux", 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"groups":[{"name":"work","members":["tmux:api"]}],"collapsed":["grp:work"],"dirs":{"tmux:api":"/srv"}}`
	if err := os.WriteFile(dir+"/ghostmux/groups.json", []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	groups, collapsed, dirs := loadState()
	if len(groups) != 1 || !collapsed["grp:work"] || dirs["tmux:api"] != "/srv" {
		t.Errorf("old-format file did not load: %+v %v %v", groups, collapsed, dirs)
	}
	if s := LoadSettings(); !s.empty() {
		t.Errorf("a file with no settings key invented some: %+v", s)
	}
}

// TestEmptySettingsAreNotSerialized: an untouched setting is not a decision,
// so it does not get written down.
func TestEmptySettingsAreNotSerialized(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	if err := SaveSettings(Settings{}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dir + "/ghostmux/groups.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "settings") {
		t.Errorf("empty settings were written to the file: %s", b)
	}

	// And clearing every field removes the key again.
	if err := SaveSettings(Settings{RailWidth: 30}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(Settings{}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(dir + "/ghostmux/groups.json")
	if strings.Contains(string(b), "settings") {
		t.Errorf("cleared settings left a key behind: %s", b)
	}
}

// TestSetWidthClamps: below 20 the names stop being readable, above 60 the
// rail stops being a rail.
func TestSetWidthClamps(t *testing.T) {
	orig := Width()
	t.Cleanup(func() { SetWidth(orig) })
	for _, c := range []struct{ in, want int }{{1, 20}, {19, 20}, {20, 20}, {45, 45}, {60, 60}, {61, 60}, {9999, 60}} {
		if got := SetWidth(c.in); got != c.want || Width() != c.want {
			t.Errorf("SetWidth(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestStateFileReportsTheFileNotTheFleet: the counts come from the bytes on
// disk, so a pane showing them is reporting the file it names.
func TestStateFileReportsTheFileNotTheFleet(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if info := StateFile(); info.Exists {
		t.Errorf("a fresh dir reported an existing file")
	}
	if err := saveState(
		[]Group{{Name: "work", Members: []string{"tmux:api", "tmux:web"}}},
		map[string]bool{"grp:work": true},
		map[string]string{"tmux:api": "/srv"},
	); err != nil {
		t.Fatal(err)
	}
	info := StateFile()
	if !info.Exists || info.Groups != 1 || info.Members != 2 || info.Dirs != 1 || info.Collapsed != 1 {
		t.Errorf("StateFile misread the file: %+v", info)
	}
	if info.ModTime.IsZero() || !strings.HasSuffix(info.Path, "ghostmux/groups.json") {
		t.Errorf("StateFile has no path/mtime evidence: %+v", info)
	}
}

// TestAddAgentCmdsAffectsDetection: an added name is detected exactly like a
// built-in, because detection is one map and there is no second list.
func TestAddAgentCmdsAffectsDetection(t *testing.T) {
	t.Cleanup(func() { RemoveAgentCmd("mybot") })
	if isAgentCmd("mybot") {
		t.Fatal("mybot is detected before it was added")
	}
	AddAgentCmds([]string{" MyBot ", "", "claude"})
	if !isAgentCmd("mybot") {
		t.Errorf("an added command is not detected (lowercase/trim)")
	}
	extras := ExtraAgentCmds()
	if len(extras) != 1 || extras[0] != "mybot" {
		t.Errorf("extras = %v, want just mybot (a built-in must not duplicate)", extras)
	}

	RemoveAgentCmd("claude")
	if !isAgentCmd("claude") {
		t.Errorf("a built-in was removed — ghostmux can still see it either way")
	}
	RemoveAgentCmd("mybot")
	if isAgentCmd("mybot") {
		t.Errorf("a user-added command was not removable")
	}
}

// TestInPromptCoversEveryPrompt is what the frame's `?`/`,` interception rests
// on: a mode missing here would silently eat a character the user typed.
func TestInPromptCoversEveryPrompt(t *testing.T) {
	withFakeRunner(t, map[string]string{
		"list-sessions": "alpha\t0\n",
		"list-windows":  "alpha\t1\tzsh\t1\t0\t0\t0\n",
	})
	m := New(&fakeViewport{})
	if m.InPrompt() {
		t.Fatal("a fresh rail reports itself mid-prompt")
	}
	for _, key := range []string{"/", "n", "a", "x"} {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		if !next.(Model).InPrompt() {
			t.Errorf("%q opened a prompt the frame cannot see", key)
		}
	}
}

// TestHelpEntriesAndToggleFooterAreTheRailsTruth: the frame draws help now, so
// what it draws has to come from the same table the keymap does.
func TestHelpEntriesAndToggleFooterAreTheRailsTruth(t *testing.T) {
	entries := HelpEntries()
	if len(entries) != len(keyHelpRows()) {
		t.Fatalf("HelpEntries dropped rows: %d vs %d", len(entries), len(keyHelpRows()))
	}
	var keys []string
	for _, e := range entries {
		keys = append(keys, e.Key)
		if e.Desc == "" {
			t.Errorf("entry %q has no description", e.Key)
		}
	}
	joined := strings.Join(keys, " ")
	for _, want := range []string{"?", ","} {
		if !strings.Contains(joined, want) {
			t.Errorf("the table does not document the frame key %q", want)
		}
	}

	origLabel, origAll := toggleLabel, toggleAll
	t.Cleanup(func() { toggleLabel, toggleAll = origLabel, origAll })
	SetToggleKeys("f9")
	if f := ToggleFooter(); f != "" {
		t.Errorf("one bound key produced a footer: %q", f)
	}
	SetToggleKeys("f9", "ctrl+]")
	if f := ToggleFooter(); !strings.Contains(f, "f9") || !strings.Contains(f, "ctrl+]") {
		t.Errorf("footer does not name every accepted key: %q", f)
	}
	if HelpEntries()[4].Key != "f9" {
		t.Errorf("the keymap row does not follow the real binding: %+v", HelpEntries()[4])
	}
}
