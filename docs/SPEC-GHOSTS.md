# SPEC — Ghosts (Design A of docs/RETURN.md)

Status: ready to build. Branch: `panel-flip` (on `0ed2234`). Design
rationale lives in docs/RETURN.md §Design A; this file is the binding
contract. Where they disagree, this file wins.

## Laws (binding)

1. **Evidence, never inference.** A ghost row asserts exactly two facts:
   "this name is declared here (groups.json) or listed by its backend
   (zellij EXITED)" and "it is not running now." It carries NO other marks —
   no bell, done, activity, attached, agent accent. `○` is the only glyph a
   ghost may show.
2. **The purist test.** Summoning uses only `tmux new-session`,
   `zellij attach`, `zellij attach --create-background`. No layout restore,
   no command replay, no snapshots. (A+ start commands: OUT OF SCOPE.)
3. **Verbs tell the truth.** `x` confirm prompts must name the real action:
   kill (live session) / ungroup (group) / forget (declaration ghost) /
   delete (zellij EXITED — removes zellij's serialized session).

## Phase 0 — probe before building (record results in the final report)

Run everything against scratch resources only: tmux via
`GHOSTMUX_TMUX_ARGS='-L gm-ghost -f /dev/null'`, zellij session names
prefixed `gm-spec-`, and `XDG_STATE_HOME` pointed at a mktemp dir. Never
touch the user's live tmux server or ~/.local/state/ghostmux.

- P1: `tmux new-session -d -s X` when X exists — capture the exact error
  text (expected to contain "duplicate session").
- P2: `#{session_path}` — confirm `list-sessions -F '#{session_name}\t#{session_attached}\t#{session_path}'`
  emits the -c dir of a session.
- P3: `zellij kill-session <s>` then `list-sessions --no-formatting` —
  confirm the row gains `(EXITED - attach to resurrect)`.
- P4: on an EXITED session, does `zellij attach --create-background <s>`
  resurrect it (row loses EXITED)? Record yes/no — this decides whether `S`
  can summon EXITED zellij members (T6).
- P5: `zellij delete-session --force <s>` removes an EXITED row.

## Ground truth (verified 2026-07-24; read these files before editing)

- `internal/tmux/query.go` — `Sessions()` parses
  `#{session_name}\t#{session_attached}`, tolerates short rows
  (`len(f) < 2 → skip`). Test fixtures use 2-field rows ("alpha\t0\n").
- `internal/rail/backends.go` — `auxSessions()` SKIPS lines containing
  "EXITED". `killAux` uses `zellij kill-session`. `createAux` uses
  `zellij attach --create-background`.
- `internal/rail/groups.go` — `groupState{Groups, Collapsed}`,
  `loadState() ([]Group, map[string]bool)`, `saveState(groups, collapsed)`,
  `applyGroups(rows, groups)` synthesizes group headers and currently
  **skips** members with no live block (`if !ok || used[key] { continue }`).
  `memberKey(backend, sess)` → "tmux:api" / "zellij:myz". `foldKey`,
  `forgetMember` exist.
- `internal/rail/rows.go` — `railRow` has `isGroup, isWin, group, count,
  flat, cmd, backend`; `gutter()` priority bell>done>act>inView; `plain()`
  and `marks()` feed `rail once`.
- `internal/rail/model.go` — `activateRow` (↵ + double-click, single path),
  `pointRow` (refuses groups), `killTarget/killBackend/killGroup` +
  `updateKillConfirmKey`, `toggleFold`, `m.dirs` does not exist yet.
- `internal/rail/view.go` — `renderRow` (dim rendering exists for filter:
  everything in `hexCursorBg`), `hintLine` (normal mode returns ""),
  collapsed-group suffix uses `r.count`.
- `internal/app/` — no changes expected anywhere in this spec.
- Tests: `internal/rail/*_test.go` with `withFakeRunner` fixtures;
  `fakevp_test.go` is the Viewport double. Harness: `test/panel_test.sh`
  (exports its own XDG_STATE_HOME at the top; add to it, keep that).

## Tasks

### T1 — `Session.Path` (internal/tmux/query.go)

Append `#{session_path}` to the Sessions() format. Parse tolerantly:
`len(f) >= 3 → Path = f[2]`, else Path stays "". Existing 2-field fixtures
must keep passing unchanged. Add Path only to fixtures in tests that need
it.

### T2 — state file learns dirs (internal/rail/groups.go)

- `groupState` gains `Dirs map[string]string \`json:"dirs,omitempty"\``.
- `loadState` returns `([]Group, map[string]bool, map[string]string)`;
  `saveState(groups, collapsed, dirs)`. Update all callers (model.New,
  m.saveState, tests). A missing/nil map loads as empty, never nil-panics.
- `forgetMember` also deletes the dirs entry.
- Old files (no `dirs` key) load cleanly; new files omit the key when empty.

### T3 — dir capture (internal/rail/model.go refresh path)

After fetching sessions in `refresh()`: for each live tmux session whose
`memberKey("", name)` is in some group, if `s.Path != ""` and differs from
`m.dirs[key]`, update the map. If anything changed, ONE `m.saveState()` per
refresh. Zellij members never get dirs (CLI proves none).

### T4 — ghost rows

`railRow` gains `ghost bool` and `ghostCount int` (group headers), and
`dir string` (ghosts only, for the hint/suffix).

Sources of ghosts:
1. **Declaration ghosts** — in `applyGroups` (which gains a `dirs`
   parameter), a member key with no live block synthesizes, instead of the
   current skip:
   `railRow{depth: 1, ghost: true, flat: true, label: name, sess: name,
   backend: backendOf(key), group: g.Name, dir: dirs[key]}`.
   It contributes to `ghostCount`, NOT to `count`, and NEVER to the
   header's marks aggregation.
2. **zellij EXITED** — `auxSession` gains `exited bool`; `auxSessions()`
   stops skipping EXITED lines (name = text before first " "), sets
   `exited: true`, still skips the "No active" prose line. `auxRows`
   renders exited sessions with `ghost: true` (and still zero marks —
   extend the existing no-unproven-marks test to cover exited rows). An
   exited row that is also a group member nests under its group like any
   live member (applyGroups finds its block by key; the ghost flag rides
   along).

Rendering (internal/rail/view.go):
- Ghost row: label + suffix entirely in `hexCursorBg` dim (reuse the
  filter-dim mechanics), cursor-bar background still applies.
- Gutter: `gutter()` returns `"○"` for ghosts (and nothing else can be set).
- Suffix slot: declaration ghosts show the dir tail, truncated from the
  LEFT (`…ects/api`) to fit; zellij ghosts keep `· zellij`.
- Group header: dim the label when `count == 0 && ghostCount > 0`.
  Collapsed suffix: live count as today when `count > 0`, plus ` ○N` when
  `ghostCount > 0` (so: `▸ work 2 ○1`, `▸ agents ○2`).
- `plain()`: ghost rows use `○` in the mark position so `rail once` shows
  them. `marks()`: add flag `ghost`.

### T5 — summon (↵ on a ghost)

In `activateRow`, before `pointRow`: if `r.ghost`, route to
`m.summonRow(r)`:
- tmux declaration ghost: `tmux.Run("new-session", "-d", "-s", name, "-c",
  dir)` with dir falling back to home when unrecorded or stat fails (flash
  "dir gone, started in ~" only in the stat-fail case). A duplicate-session
  error (P1 text) is NOT an error — the session appeared between render and
  keypress; proceed. Then `m.vp.Point(name, "", false)` + refresh.
- zellij EXITED ghost: plain `m.vp.PointAux("zellij", name)` — the attach
  IS the resurrection (zellij's own feature; we relay).
- zellij declaration ghost (not listed by zellij at all):
  `createAux("zellij", name)` then `PointAux`.
- `pointRow` additionally refuses ghost rows (belt-and-braces, like
  groups), so no other path can attach the viewport to a dead name.

Hint line (normal mode, cursor on a ghost — hintLine has m.cursor):
- tmux ghost: `↵ start in <dir-tail> · x forget` (dir omitted when none)
- zellij EXITED: `↵ resurrect · x delete`
Truncate to railWidth. This replaces the current empty-string return only
when the cursor row is a ghost.

### T6 — `S` on a group row: summon the fleet

`S` in normal mode, cursor on a group header: summon every declaration
ghost in that group (tmux create + zellij create-background; no viewport
re-point, no confirm — creating is safe). Behavior for EXITED zellij
members depends on P4: resurrect them too if create-background resurrects;
otherwise leave them (their hint says `↵ resurrect`) and flash
`started N · M need ↵`. On any row that isn't a group header, `S` is a
no-op. After summoning: refresh.

### T7 — `x` grows two honest verbs

Replace `killGroup bool` with `killKind` (small enum: kill / ungroup /
forget / delete), captured at `x`-press from the row:
- live session → kill (unchanged)
- group header → ungroup (unchanged)
- declaration ghost → **forget**: `forgetMember(key)` + save; no mux calls.
- zellij EXITED ghost → **delete**: new injectable
  `var deleteAux = func(backend, name string) error` using
  `zellij delete-session --force`; ALSO forgetMember if it was grouped
  (deleting the serialized session makes a grouped member a pure
  declaration ghost otherwise — one x should fully remove what you see).
Confirm prompt renders the verb: `forget api? y/n`, `delete myz? y/n`.

### T8 — keymap bookkeeping

`keyHelpRows`: add `{"S", "start group's dead sessions"}`; adjust the `x`
description to `"kill / ungroup / forget"`. `TestKeyHelpCoversBoundKeys`
boundKeys list: add `"S"`. Bottom bar: UNCHANGED (already full; `?` and the
hint line carry this).

### T9 — tests

Unit (internal/rail):
- applyGroups synthesizes a declaration ghost (dim facts: ghost, dir,
  depth, group, no marks) and headers aggregate count/ghostCount correctly.
- auxSessions keeps EXITED rows with exited=true; prose line still skipped;
  exited rows carry zero unproven marks (extend the existing test).
- summon: tmux ghost runs new-session with -c dir then Points (fakeViewport
  records it); duplicate-session error still Points; missing dir falls back
  to home; zellij EXITED summon calls PointAux only (no createAux).
- x on declaration ghost forgets (member + dirs entry gone, state saved);
  x on EXITED calls deleteAux (injectable, assert args).
- dir capture: refresh with a fixture Path writes dirs once; unchanged
  Path does not re-save (count saveState calls via a temp XDG_STATE_HOME
  file mtime or a test seam — simplest correct approach wins).
- S summons only dead members, leaves live ones untouched.
- Ghost hint line content for both backend flavors.

Harness (append to test/panel_test.sh, keep its XDG_STATE_HOME isolation):
- Group a session, `q`, kill it with tmux, relaunch: rail shows `○` ghost;
  `↵` summons it (has-session proves it; viewport shows inner bar);
  session_path recorded in groups.json (`"dirs"` present).
- Kill again, relaunch, `x` then `y`: ghost gone AND groups.json member
  gone.
- zellij (guarded on PATH as the existing sections are):
  `zellij kill-session` an existing scratch session → rail shows it as a
  ghost (EXITED surfaced); `↵` resurrects (list-sessions row loses EXITED).

## Out of scope

A+ declared start commands; Design B entirely; window-level anything;
bottom-bar layout changes; any internal/app or internal/term change; any
`ghostmux up/ls/doctor` change; committing (leave the tree dirty).

## Verification (all must pass, in this order)

```
go build ./... && go vet ./... && gofmt -l .   # gofmt output must be empty
go test ./...
PATH="$HOME/.local/bin:$PATH" bash test/panel_test.sh   # ALL CHECKS PASSED
```

Report: probe results P1–P5, any deviation from this spec with one-line
rationale, new test count, harness check count.
