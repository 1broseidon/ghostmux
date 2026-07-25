package app

import (
	"os/exec"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/1broseidon/ghostmux/internal/rail"
)

// Settings is a MODE, not an overlay, and help is the other way around. The
// rule is the panes' contract: left selects, right shows what is selected.
// Sections left, their fields right — settings honors it exactly, so it gets
// to be the whole frame. A flat keymap cannot honor it, so it floats.
//
// Everything here obeys the same two laws the rail does. Backends, State and
// About report only what can be observed — an installed binary's own version
// string, the bytes in the state file, the build info the linker left — and a
// missing fact renders as missing, never as a guess. And a field the user
// cannot change says why: with GHOSTMUX_TOGGLE set, Keys is read-only and
// names the variable, rather than accepting a rebind it would then discard.

type section int

const (
	secKeys section = iota
	secRail
	secAgents
	secBackends
	secState
	secAbout
)

var sectionNames = []string{"Keys", "Rail", "Agents", "Backends", "State", "About"}

const (
	hexSetCursorBg = "#504945"
	hexSetName     = "#ebdbb2"
	hexSetLabel    = "#928374"
	hexSetValue    = "#8ec07c"
	hexSetHint     = "#665c54"
	hexSetErr      = "#fb4934"
	hexSetTitle    = "#fe8019"
)

var (
	stySetName  = lipgloss.NewStyle().Foreground(lipgloss.Color(hexSetName))
	stySetLabel = lipgloss.NewStyle().Foreground(lipgloss.Color(hexSetLabel))
	stySetValue = lipgloss.NewStyle().Foreground(lipgloss.Color(hexSetValue))
	stySetHint  = lipgloss.NewStyle().Foreground(lipgloss.Color(hexSetHint))
	stySetErr   = lipgloss.NewStyle().Foreground(lipgloss.Color(hexSetErr))
	stySetTitle = lipgloss.NewStyle().Foreground(lipgloss.Color(hexSetTitle)).Bold(true)
)

// backendFact is one multiplexer as the box can prove it: where it is and what
// it says its version is. Absent means absent — never a guess at a version.
type backendFact struct {
	name, path, version string
	installed           bool
}

type settingsModel struct {
	cursor  int
	capture bool            // Keys: the next keypress becomes the toggle
	editing bool            // Rail/Agents: an inline text edit is open
	input   textinput.Model // the inline editor
	msg     string          // result of the last edit
	msgErr  bool

	// Probed once on section entry, not per render: a settings pane that
	// forked two processes per frame would be its own performance bug.
	backends []backendFact
	state    *rail.StateInfo
}

// openSettings enters settings mode. It is the only constructor: `,` from a
// rail-focused, non-prompting frame, and nothing else.
func (m soloModel) openSettings() soloModel {
	s := &settingsModel{}
	s.enter()
	m.settings = s
	return m
}

// closeSettings is the ONE close path — esc, q, and `,` all come here, so
// there is no way to leave settings in a half-open state. The viewport is
// untouched on the way in and out: the child kept running the whole time, and
// its frame comes straight back from the emulator's buffer.
func (m soloModel) closeSettings() soloModel {
	m.settings = nil
	return m
}

// enter probes whatever the newly-selected section needs, once.
func (s *settingsModel) enter() {
	switch section(s.cursor) {
	case secBackends:
		if s.backends == nil {
			s.backends = probeBackends()
		}
	case secState:
		if s.state == nil {
			info := rail.StateFile()
			s.state = &info
		}
	}
}

// updateSettingsKey routes a keystroke inside settings. Toggle keys are inert
// here: there is no viewport to hand the keyboard to while the frame is
// showing something else.
func (m soloModel) updateSettingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.settings
	if s.capture {
		return m.captureToggle(msg)
	}
	if s.editing {
		return m.editKey(msg)
	}
	switch msg.String() {
	case "esc", "q", ",":
		return m.closeSettings(), nil
	case "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		if s.cursor < len(sectionNames)-1 {
			s.cursor++
			s.msg = ""
			s.enter()
		}
	case "k", "up":
		if s.cursor > 0 {
			s.cursor--
			s.msg = ""
			s.enter()
		}
	case "g":
		s.cursor = 0
		s.enter()
	case "G":
		s.cursor = len(sectionNames) - 1
		s.enter()
	case "enter":
		return m.startEdit()
	}
	return m, nil
}

// startEdit opens whatever "change this" means for the selected section. The
// three read-only sections do nothing, quietly: there is nothing to say about
// a field that was never offered as editable.
func (m soloModel) startEdit() (tea.Model, tea.Cmd) {
	s := m.settings
	s.msg, s.msgErr = "", false
	switch section(s.cursor) {
	case secKeys:
		if toggleEnvLocked() {
			s.msg, s.msgErr = "GHOSTMUX_TOGGLE decides this binding — unset it to rebind here", true
			return m, nil
		}
		s.capture = true
	case secRail:
		s.editing = true
		s.input = settingsInput(strconv.Itoa(rail.Width()))
		return m, textinput.Blink
	case secAgents:
		s.editing = true
		s.input = settingsInput("")
		return m, textinput.Blink
	}
	return m, nil
}

// captureToggle takes the next keypress as the new toggle. One key replaces
// the list: a user rebinding is naming the key they can actually press, and
// keeping the old one would leave the pane unable to say what is bound.
func (m soloModel) captureToggle(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.settings
	s.capture = false
	key := msg.String()
	if key == "esc" {
		return m, nil
	}
	cfg := rail.LoadSettings()
	cfg.Toggle = []string{key}
	if err := rail.SaveSettings(cfg); err != nil {
		s.msg, s.msgErr = err.Error(), true
		return m, nil
	}
	m = m.applyToggleKeys(toggleKeys())
	s.msg = "toggle is now " + key
	return m, nil
}

// applyToggleKeys makes a new binding live everywhere it is known: the frame's
// own intercept set, the bar's label, and the rail's help table. Three places
// that must never disagree, so they are set from one slice in one function.
func (m soloModel) applyToggleKeys(keys []string) soloModel {
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[k] = true
	}
	m.toggles, m.toggleLabel = set, keys[0]
	rail.SetToggleKeys(keys...)
	return m
}

// editKey handles the inline text editor for Rail width and Agents.
func (m soloModel) editKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.settings
	switch msg.String() {
	case "esc":
		s.editing = false
		return m, nil
	case "enter":
		value := strings.TrimSpace(s.input.Value())
		s.editing = false
		switch section(s.cursor) {
		case secRail:
			return m.applyWidth(value)
		case secAgents:
			return m.applyAgent(value)
		}
		return m, nil
	}
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	return m, cmd
}

// applyWidth saves and applies a new rail width. Applying it means re-running
// the resize path with the current terminal size, so the rail, the divider,
// the viewport AND the child's pty all move now — a width that only took
// effect on the next launch would be a setting that lies.
func (m soloModel) applyWidth(value string) (tea.Model, tea.Cmd) {
	s := m.settings
	n, err := strconv.Atoi(value)
	if err != nil {
		s.msg, s.msgErr = "width must be a number", true
		return m, nil
	}
	got := rail.SetWidth(n)
	cfg := rail.LoadSettings()
	cfg.RailWidth = got
	if err := rail.SaveSettings(cfg); err != nil {
		s.msg, s.msgErr = err.Error(), true
		return m, nil
	}
	s.msg = "rail width " + strconv.Itoa(got)
	if got != n {
		s.msg += " (clamped to the 20–60 range)"
	}
	return m.resize(m.w, m.h)
}

// applyAgent adds a name, or removes it if this user added it before. One key
// with toggle semantics rather than two: the pane states the rule, and an
// add/remove pair would be two keys for a list that is usually three names
// long. Built-ins are never removable — ghostmux can see them either way.
func (m soloModel) applyAgent(name string) (tea.Model, tea.Cmd) {
	s := m.settings
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return m, nil
	}
	extras := map[string]bool{}
	for _, e := range rail.ExtraAgentCmds() {
		extras[e] = true
	}
	if extras[name] {
		delete(extras, name)
		rail.RemoveAgentCmd(name)
		s.msg = "removed " + name
	} else {
		for _, b := range rail.BuiltinAgentCmds() {
			if b == name {
				s.msg, s.msgErr = name+" is built in — it is always detected", true
				return m, nil
			}
		}
		extras[name] = true
		rail.AddAgentCmds([]string{name})
		s.msg = "added " + name
	}
	list := make([]string, 0, len(extras))
	for e := range extras {
		list = append(list, e)
	}
	sort.Strings(list)
	cfg := rail.LoadSettings()
	cfg.Agents = list
	if err := rail.SaveSettings(cfg); err != nil {
		s.msg, s.msgErr = err.Error(), true
	}
	return m, nil
}

// settingsInput is the inline editor, styled like the rail's prompt so the two
// text fields in the program look like one idea.
func settingsInput(value string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 32
	ti.Width = 24
	ti.SetValue(value)
	ti.TextStyle = stySetName
	ti.Cursor.Style = stySetName
	ti.Focus()
	return ti
}

// --- rendering ---

// settingsView draws the mode with the panel's own geometry: the section list
// in the rail's columns, the same divider, the detail on the right. The bar is
// added by View(), as always.
func (m soloModel) settingsView(vw, bodyH int) string {
	left := block(m.sectionList(bodyH), rail.Width(), bodyH)
	right := block(m.sectionDetail(vw), vw, bodyH)
	div := lipgloss.NewStyle().Foreground(lipgloss.Color(hexDividerRail)).Render("│")
	var b strings.Builder
	for i := range bodyH {
		b.WriteString(left[i])
		b.WriteString(div)
		b.WriteString(right[i])
		b.WriteByte('\n')
	}
	return b.String()
}

// sectionList is the left pane: the same cursor bar the rail draws, because it
// is the same gesture selecting the same kind of thing.
func (m soloModel) sectionList(height int) string {
	var b strings.Builder
	b.WriteString(" " + stySetTitle.Render("▍") + stySetName.Render("settings") + "\n\n")
	for i, name := range sectionNames {
		row := " " + name
		if i == m.settings.cursor {
			row = lipgloss.NewStyle().
				Foreground(lipgloss.Color(hexSetName)).
				Background(lipgloss.Color(hexSetCursorBg)).
				Render(pad(" "+name, rail.Width()))
		} else {
			row = stySetLabel.Render(row)
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

// sectionDetail is the right pane: the selected section's fields.
func (m soloModel) sectionDetail(w int) string {
	s := m.settings
	var lines []string
	switch section(s.cursor) {
	case secKeys:
		lines = m.keysDetail()
	case secRail:
		lines = m.railDetail()
	case secAgents:
		lines = m.agentsDetail(w)
	case secBackends:
		lines = backendsDetail(s.backends)
	case secState:
		lines = stateDetail(s.state)
	case secAbout:
		lines = aboutDetail()
	}
	head := []string{"", " " + stySetTitle.Render(sectionNames[s.cursor]), ""}
	out := append(head, lines...)
	if s.msg != "" {
		sty := stySetHint
		if s.msgErr {
			sty = stySetErr
		}
		out = append(out, "", " "+sty.Render(truncateRunes(s.msg, max(w-2, 1))))
	}
	return strings.Join(out, "\n")
}

func (m soloModel) keysDetail() []string {
	keys := toggleKeys()
	source := "default"
	if len(rail.LoadSettings().Toggle) > 0 {
		source = "state file"
	}
	if toggleEnvLocked() {
		source = "env (GHOSTMUX_TOGGLE)"
	}
	out := []string{
		" " + stySetLabel.Render("rail ⇄ viewport  ") + stySetValue.Render(strings.Join(keys, "  ")),
		" " + stySetLabel.Render("source           ") + stySetName.Render(source),
		"",
	}
	switch {
	case m.settings.capture:
		out = append(out, " "+stySetTitle.Render("press the new toggle key")+stySetHint.Render(" · esc cancels"))
	case toggleEnvLocked():
		out = append(out,
			" "+stySetHint.Render("read-only: GHOSTMUX_TOGGLE is set in this"),
			" "+stySetHint.Render("environment and wins over the state file."))
	default:
		out = append(out, " "+stySetHint.Render("↵ rebind — the next key you press becomes it"))
	}
	return out
}

func (m soloModel) railDetail() []string {
	out := []string{
		" " + stySetLabel.Render("width  ") + stySetValue.Render(strconv.Itoa(rail.Width())+" cols"),
		" " + stySetLabel.Render("range  ") + stySetName.Render("20–60 (default 30)"),
		"",
	}
	if m.settings.editing {
		out = append(out, " "+stySetLabel.Render("width: ")+m.settings.input.View())
	} else {
		out = append(out, " "+stySetHint.Render("↵ edit — applied immediately, pty and all"))
	}
	return out
}

func (m soloModel) agentsDetail(w int) []string {
	out := []string{
		" " + stySetLabel.Render("built in ") + stySetHint.Render(wrapList(rail.BuiltinAgentCmds(), max(w-11, 8))),
	}
	extras := rail.ExtraAgentCmds()
	if len(extras) == 0 {
		out = append(out, " "+stySetLabel.Render("yours    ")+stySetHint.Render("(none)"))
	} else {
		out = append(out, " "+stySetLabel.Render("yours    ")+stySetValue.Render(wrapList(extras, max(w-11, 8))))
	}
	out = append(out, "")
	if m.settings.editing {
		out = append(out, " "+stySetLabel.Render("agent: ")+m.settings.input.View())
	} else {
		out = append(out,
			" "+stySetHint.Render("↵ type a command name: a new one is added,"),
			" "+stySetHint.Render("one of yours is removed. Built-ins stay."))
	}
	return out
}

func backendsDetail(facts []backendFact) []string {
	var out []string
	for _, f := range facts {
		if !f.installed {
			out = append(out, " "+stySetLabel.Render(padRight(f.name, 8))+stySetHint.Render("not installed"))
			continue
		}
		out = append(out,
			" "+stySetLabel.Render(padRight(f.name, 8))+stySetName.Render(f.path))
		if f.version != "" {
			out = append(out, " "+strings.Repeat(" ", 8)+stySetValue.Render(f.version))
		}
	}
	out = append(out, "", " "+stySetHint.Render("what each binary reports about itself"))
	return out
}

func stateDetail(info *rail.StateInfo) []string {
	if info == nil {
		return nil
	}
	if info.Path == "" {
		return []string{" " + stySetHint.Render("no state directory on this box")}
	}
	out := []string{" " + stySetLabel.Render("file  ") + stySetName.Render(info.Path)}
	if !info.Exists {
		return append(out, "", " "+stySetHint.Render("not created yet — it appears with your first group"))
	}
	counts := "groups " + strconv.Itoa(info.Groups) +
		" · members " + strconv.Itoa(info.Members) +
		" · dirs " + strconv.Itoa(info.Dirs) +
		" · collapsed " + strconv.Itoa(info.Collapsed)
	return append(out,
		" "+stySetLabel.Render("holds ")+stySetValue.Render(counts),
		" "+stySetLabel.Render("saved ")+stySetName.Render(info.ModTime.Format("2006-01-02 15:04:05")),
		"",
		" "+stySetHint.Render("read from the file, not from the live fleet"))
}

func aboutDetail() []string {
	return []string{
		" " + stySetName.Render("ghostmux"),
		" " + stySetHint.Render("attach-anywhere mission control for your"),
		" " + stySetHint.Render("multiplexers"),
		"",
		" " + stySetLabel.Render("version  ") + stySetValue.Render(buildVersion()),
		"",
		" " + stySetLabel.Render("laws"),
		" " + stySetHint.Render("  render evidence, never inference"),
		" " + stySetHint.Render("  ship only what the multiplexer alone"),
		" " + stySetHint.Render("  can't give you"),
	}
}

// buildVersion reports what the linker actually recorded. A build with no
// module version and no vcs stamp is a dev build, and says so — inventing a
// number here would be the one thing this program refuses to do.
func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev build"
	}
	version := info.Main.Version
	if version == "" || version == "(devel)" {
		version = ""
	}
	rev := ""
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 7 {
			rev = s.Value[:7]
		}
	}
	switch {
	case version != "" && rev != "":
		return version + " (" + rev + ")"
	case version != "":
		return version
	case rev != "":
		return "dev build (" + rev + ")"
	}
	return "dev build"
}

// probeBackends asks each multiplexer where it is and what version it is —
// once, on entry to the section. Nothing is inferred from the other's answer:
// a box with zellij and no tmux reports exactly that.
func probeBackends() []backendFact {
	var out []backendFact
	for _, b := range []struct{ name, flag string }{{"tmux", "-V"}, {"zellij", "--version"}} {
		f := backendFact{name: b.name}
		path, err := exec.LookPath(b.name)
		if err != nil {
			out = append(out, f)
			continue
		}
		f.installed, f.path = true, path
		if o, err := exec.Command(b.name, b.flag).Output(); err == nil {
			f.version = strings.TrimSpace(strings.SplitN(string(o), "\n", 2)[0])
		}
		out = append(out, f)
	}
	return out
}

// wrapList joins names, cutting the tail when the row runs out of room. It
// says "+N more" rather than truncating a name into a lie.
func wrapList(names []string, width int) string {
	var kept []string
	used := 0
	for i, n := range names {
		add := len([]rune(n)) + 1
		if used+add > width {
			return strings.Join(kept, " ") + " +" + strconv.Itoa(len(names)-i) + " more"
		}
		kept = append(kept, n)
		used += add
	}
	return strings.Join(kept, " ")
}

func padRight(s string, width int) string {
	if n := width - len([]rune(s)); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}
