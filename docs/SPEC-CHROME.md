# SPEC — Help overlay + Settings mode

Status: ready to build. Branch: `panel-flip`. The working tree already
contains the UNCOMMITTED ghosts implementation (docs/SPEC-GHOSTS.md) — it is
intentional, must not be reverted, and must stay green. This file is the
binding contract.

## The rule these features follow

The two panes have a contract: **left selects, right shows what's selected.**
Anything that can honor that contract may be a mode (settings can: sections
left, fields right). Anything that can't must be an overlay (help: a flat
reference table with nothing to select). Never render unrelated content in
the right pane while the rail cursor points at a session.

Evidence that motivates help-as-overlay: at railWidth 30, 9 of 17 help rows
truncate — including `ctrl+\  toggle rail ⇄ vi…`, the one row a user with a
compositor-grabbed key needs intact.

## Laws (binding)

- Evidence, never inference — About/Backends/State sections show only
  provable facts (installed versions, file contents, build info); missing
  facts render as absent or "unknown", never invented.
- A field the user cannot change must say why (e.g. toggle overridden by
  GHOSTMUX_TOGGLE) rather than silently ignoring edits.
- One behavior, one place: `?` and `,` are intercepted in exactly one spot
  (the frame), and every close path goes through the same function.

## Ground truth (read before editing)

- `internal/app/app.go` — soloModel{rail, vp, focus, w, h, toggles,
  toggleLabel}; Update intercepts toggle keys; View composes
  rail│divider│viewport + statusLine; `block`/`pad`/`truncate` helpers are
  ANSI-aware.
- `internal/app/status.go` — bottom bar; `railKeys`/`viewportKeys` swap on
  focus; keys shed whole pairs when narrow.
- `internal/rail/help.go` — `keyHelpRows()` (single source of truth),
  `helpPage()` (in-rail help view, to be DELETED), `SetToggleKeys`,
  toggleLabel/toggleAll.
- `internal/rail/model.go` — `helpView bool` + `?` key + help-close keys
  (to be DELETED); `mode` enum; `updateNormalKey`.
- `internal/rail/groups.go` — groupState{Groups, Collapsed, Dirs},
  loadState/saveState. Settings ride in this same file (it is "the state
  file", not "the groups file").
- `internal/rail/rows.go` — `agentCmds` map (hardcoded agent detection).
- `internal/rail/layout.go` — `railWidth = 30` const + `Width` exported
  const alias (app uses `rail.Width` in several places).
- `internal/term/term.go` — `overlayCursor` shows the ansi.Cut compositing
  idiom to imitate.
- Tests: `TestKeyHelpCoversBoundKeys` mirrors updateNormalKey's switch;
  app tests in `internal/app/app_test.go`; harness `test/panel_test.sh`
  (isolated XDG_STATE_HOME; 45 checks — keep all green).

## Tasks

### T1 — rail sheds help, exports what the frame needs

- Delete `helpView`, the `?` case, the help-close key block, and
  `helpPage()`. `keyHelpRows` stays (add `{",", "settings"}`; keep `{"?",
  "help"}` — table may document frame keys; the coverage test only checks
  the other direction). Remove `"?"` from that test's boundKeys list.
- Export `rail.HelpEntries() []HelpEntry` (`HelpEntry{Key, Desc string}`)
  returning keyHelpRows, and `rail.ToggleFooter() string` returning the
  "toggle: X or Y" line when more than one key is bound ("" otherwise).
- Export `rail.(Model) InPrompt() bool` — true when mode != modeNormal.
  The frame must not steal `?`/`,` while the user types a filter, name, or
  confirm.

### T2 — settings in the state file

- `groupState` gains `Settings *Settings` (`json:"settings,omitempty"`):

  ```go
  type Settings struct {
      Toggle    []string `json:"toggle,omitempty"`     // bubbletea key names
      RailWidth int      `json:"rail_width,omitempty"` // 0 = default
      Agents    []string `json:"agents,omitempty"`     // extra agent cmds
  }
  ```

- loadState/saveState carry it through (nil-safe; old files load
  unchanged; empty settings not written). Extend model plumbing so the
  frame can read and persist it (simplest correct seam wins — e.g.
  `rail.LoadSettings()`/`rail.SaveSettings(Settings)` that reuse
  loadState/saveState internally without racing the rail's own saves: the
  frame and rail share one process, single-threaded bubbletea update loop,
  so read-modify-write within one Update is safe).
- Precedence, resolved once at boot and re-resolved on edit:
  defaults < settings file < env (GHOSTMUX_TOGGLE). app.toggleKeys()
  gains the middle layer.
- `railWidth` const → package var (keep name); `Width` const alias →
  `func Width() int`; update app call sites mechanically. At boot, a valid
  Settings.RailWidth (clamp 20..60) is applied before the first render.
- `rail.AddAgentCmds([]string)` merges into agentCmds (lowercased,
  deduped). Applied at boot and re-applied on edit.

### T3 — frame key routing (internal/app/app.go)

soloModel gains `overlayHelp bool` and `settings *settingsModel` (nil =
off). In Update, BEFORE forwarding to the rail, when `focus == focusRail`
and `!m.rail.InPrompt()`:

- `"?"` → toggle overlayHelp.
- `","` → open settings (construct settingsModel).

While overlayHelp: ANY key or mouse press closes it (including the toggle
key — it closes, it does not also toggle focus); all other messages
(ticks, OutputMsg) flow normally so the fleet stays live underneath.

While settings is open: keys route to settingsModel; `esc`, `q`, and `,`
close it (one close func); toggle keys are inert; non-key messages still
forward to the rail (ticks, refresh, heal keep running — the bar's
attention counts must stay live in settings). The viewport widget is left
untouched (child keeps running; its frame reappears from the emulator
buffer on exit).

### T4 — overlay compositor + help box (internal/app/overlay.go)

- `compose(base string, box []string, x, y int) string` — ANSI-aware
  splice of box lines over the base frame at column x, row y (ansi.Cut
  left of x, box line, ansi.Cut right of x+boxWidth; the idiom of
  term.overlayCursor). Box lines carry their own background so nothing
  bleeds through; every spliced line ends with a reset.
- Help box: width `min(56, m.w-4)`, bordered (rounded, #504945), title
  ` ghostmux · keys ` in the title accent style, content from
  rail.HelpEntries() in two columns (key right-aligned #fabd2f, desc
  #a89984), gutter legend line (● bell ✓ done ~ act ▸ viewing ○ ghost),
  ToggleFooter when present, footer "any key closes". Centered. At width
  56 NO entry truncates — enforce with a test against the real table, not
  a fixture.
- Rendered in View() as the last step: `compose(frame, helpBox, ...)`.

### T5 — settings mode (internal/app/settings.go)

Same geometry as the panel: left list (rail.Width() cols) │ divider │
right detail, bottom bar last row. Bar in settings shows:
`j/k section · ↵ edit · esc back` via a third keys variant in status.go.

Sections (left, cursor bar same style as the rail's):

1. **Keys** — the toggle binding. Shows current keys and their source
   (default / state file / env). `↵` starts capture: right pane says
   "press the new toggle key · esc cancels"; next key becomes
   Settings.Toggle (single key replaces the list), saved, applied live
   (m.toggles, toggleLabel, rail.SetToggleKeys). When GHOSTMUX_TOGGLE is
   set, the field is read-only and says so — no capture.
2. **Rail** — width. `↵` → inline number edit (reuse textinput idiom),
   clamp 20..60, save, apply live: update the width var and re-run the
   WindowSizeMsg path with current w/h so rail, divider, viewport, and
   pty all resize now.
3. **Agents** — the detection list. Shows built-ins dimmed (not
   removable) and extras normal. `↵` → text input; entering a new name
   adds it; entering an existing extra removes it (toggle semantics —
   state the rule in the pane footer). Saved + applied live.
4. **Backends** — tmux/zellij: path + `-V`/`--version` first line, or
   "not installed". Probed once on section entry, not per render.
5. **State** — file path, groups/members/dirs/collapsed counts, file
   mtime. Read from the file, not the live fleet.
6. **About** — name, one-line thesis, version from
   runtime/debug.ReadBuildInfo (module version + vcs.revision short when
   present; otherwise "dev build"), and the two laws.

### T6 — tests

Unit (app + rail):
- compose(): spliced lines keep exact base width, content outside the box
  byte-identical, box lines reset styling.
- Help box: every HelpEntries desc fits untruncated at width 56
  (regression for the 9-truncated-rows finding).
- `?` gating: while rail is filtering (InPrompt true), `?` reaches the
  filter input, no overlay; while viewport-focused, `?` goes to the child.
- Any-key-closes, including the toggle key (and focus did NOT flip).
- Settings roundtrip: save → loadState returns it; old-format file (no
  settings key) loads; empty settings not serialized.
- Precedence: env beats file beats default for toggle; env makes the Keys
  field read-only.
- Width clamp; agents add/remove toggle semantics; AddAgentCmds affects
  isAgentCmd.
- Settings mode: `,` opens only from normal-mode rail focus; esc restores
  the exact prior frame (rail rows + viewport View unchanged).

Harness (append to test/panel_test.sh, keep isolation):
- `?` → capture-pane contains "start group's dead sessions" un-truncated
  and the box title; any key → gone, rail rows back.
- `,` → capture-pane contains "Backends" and "About"; esc → session rows
  visible again; fleet still live (create a session while settings open,
  see it after esc).

### T7 — docs touchups

`ghostmux help` usage text: mention `?` overlay and `,` settings in the
Keys paragraph. Nothing else.

## Out of scope

Command palette; row-scoped action menus; confirmations-as-overlays;
theme settings; any change to doctor, ghosts behavior, internal/term, or
the viewport lifecycle; committing.

## Verification (in order, all green)

```
go build ./... && go vet ./... && gofmt -l .   # gofmt output must be empty
go test -count=1 ./...
PATH="$HOME/.local/bin:$PATH" bash test/panel_test.sh   # ALL CHECKS PASSED
```

Report: files changed, deviations with one-line rationale, new unit test
count, harness total, verification tails. Confirm the pre-existing dirty
tree (ghosts work) was preserved untouched apart from files this spec
names.
