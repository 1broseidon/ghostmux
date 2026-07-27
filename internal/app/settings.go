package app

import (
	"errors"
	"os/exec"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/1broseidon/ghostmux/internal/rail"
	"github.com/1broseidon/ghostmux/internal/state"
)

// Settings is a full-frame mode: sections are selected on the left and their
// fields are shown on the right. Four sections, each earning its place:
// Fleet is policy (what the fleet remembers, where new work lands), Agents is
// detection evidence, Panel is the frame's own chrome, and System is every
// read-only fact — probes, state file, build — in one diagnostic surface.
// GHOSTMUX_TOGGLE makes the saved toggle field read-only.

type section int

const (
	secFleet section = iota
	secAgents
	secPanel
	secSystem
)

var sectionNames = []string{"Fleet", "Agents", "Panel", "System"}

// sectionFields is how many editable fields a section owns. Multi-field
// sections take a field cursor on ↵; zero-field sections are read-only.
func sectionFields(sec section) int {
	switch sec {
	case secFleet, secPanel:
		return 2
	}
	return 0
}

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

// backendFact is tmux as the box can prove it: where it is and what it says
// its version is. Absent means absent — never a guess at a version.
type backendFact struct {
	name, path, version string
	installed           bool
}

type settingsModel struct {
	store    *state.Store
	cursor   int
	inFields bool            // a multi-field section is entered; j/k move field
	field    int             // field cursor within the entered section
	capture  bool            // Panel: the next keypress becomes the toggle
	editing  bool            // Panel/Agents: an inline text edit is open
	input    textinput.Model // the inline editor
	msg      string          // result of the last edit
	msgErr   bool

	// Probed once on section entry, not per render: a settings pane that
	// forked two processes per frame would be its own performance bug.
	backends []backendFact
	state    *state.Info
}

// openSettings enters settings mode. It is the only constructor: `,` from a
// rail-focused, non-prompting frame, and nothing else.
func (m soloModel) openSettings() soloModel {
	s := &settingsModel{store: m.store}
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
	s.inFields, s.field = false, 0
	if section(s.cursor) == secSystem {
		if s.backends == nil {
			s.backends = probeBackends()
		}
		if s.state == nil {
			info := s.store.Info()
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
	if s.inFields {
		return m.fieldKey(msg)
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
	case "enter", "l", "right":
		return m.enterSection()
	}
	return m, nil
}

// enterSection routes ↵ on a section: multi-field sections hand the cursor to
// their fields, Agents opens its single editor directly, and System has
// nothing to edit.
func (m soloModel) enterSection() (tea.Model, tea.Cmd) {
	s := m.settings
	s.msg, s.msgErr = "", false
	switch section(s.cursor) {
	case secFleet, secPanel:
		s.inFields, s.field = true, 0
	case secAgents:
		s.editing = true
		s.input = settingsInput("")
		return m, textinput.Blink
	}
	return m, nil
}

// fieldKey moves the field cursor inside an entered section and starts the
// selected field's editor on ↵.
func (m soloModel) fieldKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.settings
	switch msg.String() {
	case "esc", "h", "left":
		s.inFields = false
	case "q", ",":
		return m.closeSettings(), nil
	case "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		if s.field < sectionFields(section(s.cursor))-1 {
			s.field++
			s.msg = ""
		}
	case "k", "up":
		if s.field > 0 {
			s.field--
			s.msg = ""
		}
	case "enter":
		return m.editField()
	}
	return m, nil
}

// editField starts the editor for the field under the cursor. Each field
// declares its edit kind here: cycle, capture, or inline input.
func (m soloModel) editField() (tea.Model, tea.Cmd) {
	s := m.settings
	s.msg, s.msgErr = "", false
	switch {
	case section(s.cursor) == secFleet && s.field == 0:
		return m.cycleGhostDir()
	case section(s.cursor) == secFleet && s.field == 1:
		return m.cycleCreateDir()
	case section(s.cursor) == secPanel && s.field == 0:
		if toggleEnvLocked() {
			s.msg, s.msgErr = "GHOSTMUX_TOGGLE is set; unset it to change this setting", true
			return m, nil
		}
		s.capture = true
	case section(s.cursor) == secPanel && s.field == 1:
		s.editing = true
		s.input = settingsInput(strconv.Itoa(rail.Width()))
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
	cfg := settingsFromStore(m.store)
	cfg.Toggle = []string{key}
	if err := saveSettings(m.store, cfg); err != nil {
		return m.settingsSaveFailed(err)
	}
	s.state = nil
	m = m.applyToggleKeys(toggleKeys(m.store))
	s.msg = "toggle key: " + key
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

// editKey handles the inline text editor for rail width and Agents.
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
		case secPanel:
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

// applyWidth saves a clamped candidate before changing layout, then resizes
// the rail, viewport, and embedded terminal immediately.
func (m soloModel) applyWidth(value string) (tea.Model, tea.Cmd) {
	s := m.settings
	n, err := strconv.Atoi(value)
	if err != nil {
		s.msg, s.msgErr = "width must be an integer", true
		return m, nil
	}
	got := rail.ClampWidth(n)
	cfg := settingsFromStore(m.store)
	cfg.RailWidth = got
	if err := saveSettings(m.store, cfg); err != nil {
		return m.settingsSaveFailed(err)
	}
	s.state = nil
	rail.SetWidth(got)
	s.msg = "rail width: " + strconv.Itoa(got)
	if got != n {
		s.msg += " (clamped to 20-60)"
	}
	return m.resize(m.w, m.h)
}

// applyAgent toggles an additional command name. Built-in names are fixed.
func (m soloModel) applyAgent(name string) (tea.Model, tea.Cmd) {
	s := m.settings
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return m, nil
	}
	for _, builtin := range rail.BuiltinAgentCmds() {
		if builtin == name {
			s.msg, s.msgErr = name+" is built in and cannot be removed", true
			return m, nil
		}
	}
	cfg := settingsFromStore(m.store)
	extras := map[string]bool{}
	for _, extra := range cfg.Agents {
		extras[extra] = true
	}
	action := "added"
	if extras[name] {
		delete(extras, name)
		action = "removed"
	} else {
		extras[name] = true
	}
	list := make([]string, 0, len(extras))
	for extra := range extras {
		list = append(list, extra)
	}
	sort.Strings(list)
	cfg.Agents = list
	if err := saveSettings(m.store, cfg); err != nil {
		return m.settingsSaveFailed(err)
	}
	s.state = nil
	rail.SetExtraAgentCmds(list)
	s.msg = "agent " + action + ": " + name
	return m, nil
}

// cycleGhostDir flips between launch directory and last working directory.
// Two choices, one key — no free-text enum that could invent a third mode.
func (m soloModel) cycleGhostDir() (tea.Model, tea.Cmd) {
	s := m.settings
	cfg := settingsFromStore(m.store)
	next := rail.GhostDirLast
	msg := "ghost dir: last working directory"
	if rail.NormalizeGhostDir(cfg.GhostDir) == rail.GhostDirLast {
		next = ""
		msg = "ghost dir: launch directory"
	}
	cfg.GhostDir = next
	if err := saveSettings(m.store, cfg); err != nil {
		return m.settingsSaveFailed(err)
	}
	s.state = nil
	rail.SetGhostDir(cfg.GhostDir)
	s.msg = msg
	return m, nil
}

// cycleCreateDir flips where `n` starts a new tmux session: home, or the
// viewport session's active pane cwd.
func (m soloModel) cycleCreateDir() (tea.Model, tea.Cmd) {
	s := m.settings
	cfg := settingsFromStore(m.store)
	next := rail.CreateDirCurrent
	msg := "new session dir: current session's cwd"
	if rail.NormalizeCreateDir(cfg.CreateDir) == rail.CreateDirCurrent {
		next = ""
		msg = "new session dir: home"
	}
	cfg.CreateDir = next
	if err := saveSettings(m.store, cfg); err != nil {
		return m.settingsSaveFailed(err)
	}
	s.state = nil
	rail.SetCreateDir(cfg.CreateDir)
	s.msg = msg
	return m, nil
}

func (m soloModel) settingsSaveFailed(err error) (tea.Model, tea.Cmd) {
	s := m.settings
	s.state = nil
	if !errors.Is(err, state.ErrConflict) {
		s.msg = "state save failed: " + err.Error()
		s.msgErr = true
		return m, nil
	}

	adopted := settingsFromStore(m.store)
	width := adopted.RailWidth
	if width == 0 {
		width = rail.DefaultWidth()
	}
	widthChanged := rail.Width() != width
	applySettings(adopted)
	m = m.applyToggleKeys(toggleKeys(m.store))
	m.rail = m.rail.SyncState()
	s.msg = "state changed in another panel; change not saved"
	s.msgErr = true
	if widthChanged {
		return m.resize(m.w, m.h)
	}
	return m, nil
}

// settingsInput builds the shared inline editor style.
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
	case secFleet:
		lines = m.fleetDetail()
	case secAgents:
		lines = m.agentsDetail(w)
	case secPanel:
		lines = m.panelDetail()
	case secSystem:
		lines = m.systemDetail()
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

// fieldRow renders one editable field: a cursor marker when the field cursor
// sits on it, the label, and the current value.
func (m soloModel) fieldRow(sec section, idx int, label, value string) string {
	s := m.settings
	marker, labelSty := " ", stySetLabel
	if s.inFields && section(s.cursor) == sec && s.field == idx {
		marker, labelSty = "▸", stySetName
	}
	return " " + stySetTitle.Render(marker) + " " + labelSty.Render(padRight(label, 17)) + stySetValue.Render(value)
}

// fieldNavHint is the one navigation line every multi-field section ends on.
func (m soloModel) fieldNavHint() string {
	if m.settings.inFields {
		return " " + stySetHint.Render("j/k select · ↵ change · esc back")
	}
	return " " + stySetHint.Render("↵ selects a setting")
}

func (m soloModel) fleetDetail() []string {
	cfg := settingsFromStore(m.store)
	ghost := "launch directory"
	if rail.NormalizeGhostDir(cfg.GhostDir) == rail.GhostDirLast {
		ghost = "last working directory"
	}
	create := "home"
	if rail.NormalizeCreateDir(cfg.CreateDir) == rail.CreateDirCurrent {
		create = "current session's cwd"
	}
	return []string{
		m.fieldRow(secFleet, 0, "ghost dir", ghost),
		m.fieldRow(secFleet, 1, "new session dir", create),
		"",
		" " + stySetHint.Render("ghost dir: where ↵ summons a dead grouped member"),
		" " + stySetHint.Render("  launch = where the session was created (#{session_path})"),
		" " + stySetHint.Render("  last   = active pane cwd (#{pane_current_path})"),
		" " + stySetHint.Render("new session dir: where n starts a tmux session"),
		"",
		m.fieldNavHint(),
	}
}

func (m soloModel) panelDetail() []string {
	keys := m.activeToggleKeys()
	source := "default"
	if hasSavedToggle(m.store) {
		source = "saved setting"
	}
	if toggleEnvLocked() {
		source = "GHOSTMUX_TOGGLE"
	}
	out := []string{
		m.fieldRow(secPanel, 0, "toggle key", strings.Join(keys, "  ")),
		"   " + stySetLabel.Render(padRight("source", 17)) + stySetName.Render(source),
		m.fieldRow(secPanel, 1, "rail width", strconv.Itoa(rail.Width())+" cols (20–60)"),
		"",
	}
	switch {
	case m.settings.capture:
		out = append(out, " "+stySetTitle.Render("press new toggle key; esc cancels"))
	case m.settings.editing:
		out = append(out, " "+stySetLabel.Render("width: ")+m.settings.input.View())
	case toggleEnvLocked():
		out = append(out,
			" "+stySetHint.Render("GHOSTMUX_TOGGLE overrides the saved setting"),
			m.fieldNavHint())
	default:
		out = append(out,
			" "+stySetHint.Render("width applies immediately to the embedded terminal"),
			m.fieldNavHint())
	}
	return out
}

func (m soloModel) activeToggleKeys() []string {
	keys := []string{m.toggleLabel}
	var rest []string
	for key := range m.toggles {
		if key != m.toggleLabel {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	return append(keys, rest...)
}

func hasSavedToggle(store *state.Store) bool {
	if store == nil {
		return false
	}
	doc := store.Snapshot()
	return doc.Settings != nil && len(doc.Settings.Toggle) > 0
}

func (m soloModel) agentsDetail(w int) []string {
	out := []string{
		" " + stySetLabel.Render("built in   ") + stySetHint.Render(wrapList(rail.BuiltinAgentCmds(), max(w-13, 8))),
	}
	extras := rail.ExtraAgentCmds()
	if len(extras) == 0 {
		out = append(out, " "+stySetLabel.Render("additional ")+stySetHint.Render("(none)"))
	} else {
		out = append(out, " "+stySetLabel.Render("additional ")+stySetValue.Render(wrapList(extras, max(w-13, 8))))
	}
	out = append(out, "")
	if m.settings.editing {
		out = append(out, " "+stySetLabel.Render("command name: ")+m.settings.input.View())
	} else {
		out = append(out,
			" "+stySetHint.Render("↵ enter a command name to add or remove it"),
			" "+stySetHint.Render("built-ins cannot be removed"))
	}
	return out
}

// systemDetail is the one read-only surface: build identity, backend probes,
// and state-file health, stacked. Facts to check, not choices to make.
func (m soloModel) systemDetail() []string {
	s := m.settings
	out := []string{
		" " + stySetName.Render("ghostmux") + "  " + stySetHint.Render("The tmux fleet navigator"),
		" " + stySetLabel.Render("version  ") + stySetValue.Render(buildVersion()),
		"",
	}
	out = append(out, backendsDetail(s.backends)...)
	out = append(out, "")
	out = append(out, stateDetail(s.state)...)
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
	out = append(out, "", " "+stySetHint.Render("versions reported by installed binaries"))
	return out
}

func stateDetail(info *state.Info) []string {
	if info == nil {
		return nil
	}
	if info.Path == "" {
		return []string{" " + stySetHint.Render("state path unavailable; writes disabled")}
	}
	out := []string{
		" " + stySetLabel.Render("file    ") + stySetName.Render(info.Path),
		" " + stySetLabel.Render("status  ") + stySetValue.Render(stateStatusText(info.Status, info.Version)),
	}
	switch info.Status {
	case state.StatusMissing:
		out = append(out, "", " "+stySetHint.Render("created when settings or groups are first saved"))
	case state.StatusValid, state.StatusLegacy:
		counts := "groups " + strconv.Itoa(info.Groups) +
			" · members " + strconv.Itoa(info.Members) +
			" · dirs " + strconv.Itoa(info.Dirs) +
			" · collapsed " + strconv.Itoa(info.Collapsed)
		out = append(out,
			" "+stySetLabel.Render("saved file contents  ")+stySetValue.Render(counts),
			" "+stySetLabel.Render("modified             ")+stySetName.Render(info.ModTime.Format("2006-01-02 15:04:05")))
		if info.Status == state.StatusLegacy {
			out = append(out, "", " "+stySetHint.Render("schema version 1 is written on the next successful save"))
		}
	default:
		out = append(out, " "+stySetHint.Render("state is read-only"))
		if info.Error != "" {
			out = append(out, " "+stySetErr.Render(info.Error))
		}
	}
	out = append(out,
		"",
		" "+stySetLabel.Render("backup  ")+stySetName.Render(info.BackupPath),
		" "+stySetLabel.Render("status  ")+stySetHint.Render(stateStatusText(info.BackupStatus, info.BackupVersion)))
	if info.BackupError != "" {
		out = append(out, " "+stySetErr.Render(info.BackupError))
	}
	out = append(out, "", " "+stySetHint.Render("backup is retained but not restored automatically"))
	return out
}

func stateStatusText(status string, version int) string {
	switch status {
	case state.StatusValid:
		return "valid (schema version " + strconv.Itoa(version) + ")"
	case state.StatusLegacy:
		return "legacy (unversioned)"
	case state.StatusCorrupt:
		return "corrupt"
	case state.StatusUnreadable:
		return "unreadable"
	case state.StatusUnsupported:
		return "unsupported schema version"
	case state.StatusRecoveryRequired:
		return "recovery required"
	default:
		return "missing"
	}
}

// buildVersion reports linker build information. An unstamped build is dev.
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

// probeBackends asks tmux where it is and what version it is — once, on entry
// to the section. Absent reports exactly that.
func probeBackends() []backendFact {
	f := backendFact{name: "tmux"}
	path, err := exec.LookPath("tmux")
	if err != nil {
		return []backendFact{f}
	}
	f.installed, f.path = true, path
	if o, err := exec.Command("tmux", "-V").Output(); err == nil {
		f.version = strings.TrimSpace(strings.SplitN(string(o), "\n", 2)[0])
	}
	return []backendFact{f}
}

// wrapList joins complete names and reports the omitted count when needed.
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
