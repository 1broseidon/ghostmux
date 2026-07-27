# ghostmux Phase 2 — Implementation Spec

> **Scope note (2026-07-27):** ghostmux is tmux-only. The multi-backend
> prototype (zellij beside tmux) was cut after the v0.3 panel shipped;
> references to zellij, aux backends, or `z` in this document are historical
> and no longer describe the product. ghostmux is the tmux fleet navigator.

**Repo:** `github.com/1broseidon/ghostmux` · baseline `948a9e2` · Go 1.24+ · tmux 3.4 · ghostty 1.3.1 (Linux/GTK)
**Design contract:** `docs/DESIGN.md` (the six-screen mockup spec). Where this document and the mockup spec disagree on visuals, the mockup spec wins. Where the mockup spec names a mechanism loosely (e.g. "OSC 133"), this document's mechanism wins.
**Product law (the purist test):** a feature ships only if neither tmux nor ghostty alone could do it. The rail hub passes: it is the coordination surface for a fleet of sessions rendered through nested clients — tmux alone gives you choose-tree (modal, blocking), ghostty alone gives you nothing.

---

## 0. Architecture decisions (made; do not reopen)

Each seam gets exactly one mechanism. Justifications are one sentence each; implementers execute, they don't re-litigate.

| Seam | Decision | Why |
|---|---|---|
| **D1. Double-prefix** (inner tmux needs `ctrl+b ctrl+b`) | **Hub session sets `prefix None`** (session-scoped), so `ctrl+b` passes straight through to the inner client — single prefix everywhere. | The hub's outer tmux is pure chrome (one window, two panes, fully managed by the rail), so it never needs its own prefix; the rail's own keys (`n`, `x`, `d`, `q`, `↵`) replace every prefix command you'd want there. Mouse works as a bonus (outer `mouse on` forwards events to the inner client, which requests mouse mode). Help popup still documents `ctrl+b ctrl+b` for users running `rail` manually inside a non-hub session, where `prefix None` is not applied. |
| **D2. Dead viewport pane** | **Auto-heal on tick**: if `#{pane_dead}` is 1 and a session is locked, respawn onto the locked session automatically; if the user detached intentionally (`d` key), respawn onto the idle placeholder instead. | The rail already owns the viewport lifecycle; making the user press ↵ to fix a pane the rail broke is busywork. The mockup's dead-pane mini-state remains reachable only in the ≤1s window before the tick heals it — hint-line swap (`↵ re-point viewport`) still implemented for that window. |
| **D3. Viewport claiming a neighbor shell** | **`ghostmux hub`** — one idempotent command that creates (or attaches to) a dedicated session named `hub` with the rail+viewport layout built by ghostmux, never by claiming an existing pane. `rail dock` is **removed**. | A dedicated session eliminates the "rail ate my shell" failure class and gives `prefix None` a safe blast radius. `findOrCreateViewport` stays as fallback for bare `ghostmux rail` in arbitrary sessions. |
| **D4. Hub-in-tree mirror guard** | **Keep**, generalized: exclude the session the rail runs in *by name* (works for `hub` and for manual `rail` runs). | Already validated in the prototype. |
| **D5. "Agent done" ✓ data source** | **Foreground-command transition tracking**, persisted in tmux user options. The rail polls `#{pane_current_command}` per pane each tick; when a pane transitions from a non-shell command to a shell (`zsh|bash|fish|sh`) while its session is **not** in the viewport and **not** attached elsewhere, set `@ghostmux_done 1` on that window (`tmux set -w -t <target> @ghostmux_done 1`); viewing the window clears it. | tmux 3.4 does not surface OSC 133 to hooks, so real semantic prompt-marking is impossible today; command-exit-to-shell is observable with zero shell integration and is the honest 90% of "the agent finished." Real OSC 133 lands in phase 3 via ghostty 1.4. Storing in `@options` (not rail memory) means ✓ survives rail restarts. |
| **D6. Refresh cadence** | **Event-driven via additive hook leases + `tmux wait-for`, with the 1s poll kept as fallback and as the command-transition sampler.** Every panel gets a private versioned channel and appends independent entries to the relevant global session- and window-hook arrays. It discovers tmux-assigned indices and removes only exact canonical commands it still owns. | Instant response without control mode or scalar-option changes; existing user hook entries and concurrent panels remain independent. `wait-for` is the tmux-native IPC primitive, while the fallback refresh handles unsupported hooks, startup without a server, and activity timestamps. |
| **D7. Bell blink** | **Second timer**: a 400ms `tea.Tick` runs **only while at least one bell mark exists**, advancing a phase counter; bell glyphs render on phases 0–1, hidden on phase 2 (≈800ms on / 400ms off, matching the mockup cadence). Data refresh stays on the 1s tick + events. | Blinking is view-only state; coupling it to the data tick would force 400ms polling of tmux for nothing. |
| **D8. termtile integration** | **Deferred to phase 3, seam reserved in data only**: the row model keeps `attached` and gains `clientTTY` (`#{client_tty}` of the attached client) so a future `ghostmux which <session>` / termtile call can map session → ghostty window. No X11 code in this repo, ever — focusing windows is termtile's competence, calling ghostmux for the mapping. | Ghostty 1.4's scripting API (~2 months out) will likely provide window focus natively; building EWMH focus now is duplicated effort in the wrong repo. |

---

## 1. Repo restructure (Task 1)

Minimal, idiomatic, three internal packages. No `pkg/`, no interfaces beyond the one test seam, no config framework.

```
ghostmux/
├── cmd/ghostmux/main.go        # arg dispatch + usage text only (~100 lines)
├── internal/tmux/
│   ├── tmux.go                 # Run/Output/Lines helpers, injectable runner
│   └── query.go                # typed queries: Sessions(), Windows(), PaneDead(), etc.
├── internal/rail/
│   ├── rail.go                 # cmdRail entry, once-mode, help-mode, idle-mode
│   ├── rows.go                 # railRow building, gutter logic, done-tracking
│   ├── model.go                # bubbletea model: Update, keys, filter, create, collapse
│   ├── view.go                 # View() + gruvbox styles
│   ├── viewport.go             # viewport lifecycle: jump, heal, idle, detach
│   ├── events.go               # wait-for goroutine, hook install/uninstall, blink timer
│   └── rail_test.go            # unit tests with faked tmux runner
├── internal/wiring/
│   └── wiring.go               # install/uninstall/ambient/shell/doctor/up/restore/ls,
│                               # snippet constants, ensureBlock/removeBlock, hub cmd
├── go.mod / go.sum / README.md / .gitignore
```

Rules:

- `cmd/ghostmux/main.go` contains **only** the switch and `usage()`. Every `cmdX` moves into `internal/wiring` or `internal/rail` unchanged in behavior.
- `internal/wiring` is deliberately one file to start (~600 lines is fine); split only if a later task forces it. Do not create `internal/config`, `internal/paths`, etc.
- `internal/tmux` exposes a package-level injectable runner for tests:

```go
// internal/tmux/tmux.go
package tmux

// Runner executes a tmux command and returns stdout. Swapped in tests.
var Runner = func(args ...string) (string, error) { /* exec.Command("tmux", args...) */ }

func Output(args ...string) string           // Runner, err → ""
func Lines(args ...string) []string          // split trimmed output
func Run(args ...string) error               // fire-and-forget
```

`internal/tmux/query.go` adds the typed layer used by rows.go:

```go
type Session struct{ Name string; Attached bool; ClientTTY string }
type Window struct {
    Session string; Index, Name string
    Active, Bell, Activity, Done bool   // Done ← @ghostmux_done
    PaneCmds []string                    // pane_current_command per pane
}
func Sessions() []Session
func Windows() []Window          // one -a call incl. @ghostmux_done via #{@ghostmux_done}
func SetDone(sess, index string, on bool)
func PaneDead(paneID string) bool
```

`ghostmux hub`, `up`, `restore`, `ls`, `install`, etc. all migrate to call `internal/tmux` instead of raw `exec.Command` — mechanical substitution.

---

## 2. `ghostmux hub` (Task 3)

The one command George runs. Semantics, in order:

1. If session `hub` does not exist:
   a. Create the session with the rail as window 0's command: `tmux new-session -d -s hub -n rail '<exe> rail'`.
   b. `tmux set-option -t '=hub' prefix None` and `tmux set-option -t '=hub' prefix2 None`.
   c. Split the viewport to the right: `split-window -h -d -l '75%' -P -F '#{pane_id}'`, then `resize-pane -t <rail-pane> -x 30`; set `remain-on-exit on` on the viewport pane (pane-scoped).
   d. Point the viewport at the idle placeholder (`<exe> rail idle`).
2. Attach or switch: inside tmux → `switch-client -t '=hub'`; outside on a TTY → `syscall.Exec tmux attach -t '=hub'`; `hub -w` → `ghostty +new-window -e tmux attach -t '=hub'`.
3. Idempotent: if `hub` exists, skip 1 entirely. If its rail pane died (single-pane hub window), rebuild the layout before attaching.
4. On rail exit (`q`): when the rail's session is named `hub`, its last act is `tmux kill-session -t '=hub'`; a manual `ghostmux rail` in another session just exits. Unset the `[133]` hooks (D6) on any exit path (defer in `cmdRail`).

`rail dock` is removed from dispatch and usage. `rail` (run in current pane) and `rail once` remain.

---

## 3. Rail v2 — data layer (Tasks 4–6)

### 3.1 Row model (`rows.go`)

```go
type railRow struct {
    depth     int    // 0 session, 1 window
    label     string
    sess      string
    window    string // window index for depth-1
    attached  bool   // session attached by an outside client
    active    bool   // tmux current window of its session
    bell, done, act bool
    collapsed bool   // session rows: collapsed in the rail
    inView    bool   // session currently locked in the viewport (▸)
}
```

- Gutter priority (render max 2, highest first): `●` bell > `✓` done > `~` activity > `▸` in-viewport. Session rows aggregate the highest-priority mark across their windows (mockup screen 2).
- Done-tracking (D5): rows.go keeps `map[paneKey]string` of last-seen `pane_current_command`; on each 1s tick, a transition non-shell→shell in a session that is neither `inView` nor `attached` triggers `tmux.SetDone(sess, idx, true)`. Viewing a window (↵ on it or its session while it's active) triggers `SetDone(..., false)`. Shell set: `{zsh,bash,fish,sh,dash}`.
- Bell/activity flags clear natively when the nested client displays the window; additionally clear `@ghostmux_done` on view.
- Hub exclusion by name (D4).

### 3.2 Viewport manager (`viewport.go`)

```go
type viewport struct {
    pane     string // %id
    lockSess string // session rendered, "" = idle
    lockWin  string
    detached bool   // user pressed d
}
func (v *viewport) point(sess, window string)  // respawn-pane -k + TMUX= attach [; select-window]
func (v *viewport) idle()                       // respawn onto `<exe> rail idle`
func (v *viewport) heal()                       // tick check: pane_dead → point(lock) or idle()
```

- `point` is the prototype's `jump()` verbatim, plus updating lock state and clearing marks.
- `heal` runs on every data refresh (tick or event).
- `d` key: `v.idle(); v.detached = true` — heal respects `detached` and re-idles instead of re-pointing.
- `rail idle` subcommand: prints the mockup's centered placeholder (▸ in `#fe8019`, rest `#504945`) sized to the pane, then blocks on `io.ReadAll(os.Stdin)`. ~40 lines.

### 3.3 Events + blink (`events.go`)

Per D6/D7:

```go
lease, _ := NewHookLease()       // private versioned channel and additive hook entries
go lease.Listen(ctx, program)    // wait-for signal → refreshMsg; retries server loss
lease.Close()                    // compare exact commands before removing owned entries
```

- Every panel appends independent entries to tmux's global session- and window-hook arrays. It never selects a fixed index, overwrites an existing entry, or changes `monitor-activity`/`visual-activity`.
- The goroutine's blocking `tmux wait-for` process is killed by context cancellation. Cleanup removes an entry only while its canonical command still exactly matches the lease.
- The one-second fleet refresh remains the fallback. Hooks reduce event latency; stable window activity timestamps preserve per-panel activity marks without forcing native tmux monitoring.
- Blink: `blinkMsg` on a 400ms tick started **only** when a refresh finds ≥1 bell row and no blink timer is running; the timer stops itself (returns nil cmd) when bells clear. Model holds `blinkPhase int` (mod 3).

---

## 4. Rail v2 — UI (Tasks 7–10)

All visuals from `docs/DESIGN.md` §2–§3. Styles table (lipgloss, truecolor hex — ghostty renders RGB; hex only):

| Style | Hex | Use |
|---|---|---|
| title accent `▍` | `#fe8019` | row 1 |
| title "ghostmux" | `#8ec07c` bold | row 1; ` ▸ rail` in `#928374` |
| session name | `#ebdbb2` bold | `#8ec07c` bold when `inView` |
| attached ● | `#b8bb26` | suffix |
| active window | `#b8bb26` | others `#928374` |
| bell ● | `#fb4934` bold, blinking | |
| done ✓ | `#b8bb26` | |
| activity ~ | `#fabd2f` | |
| in-view ▸ | `#fe8019` | |
| cursor bar | bg `#504945`, full 30-col width, glyph colors preserved (**replace `Reverse(true)`**) | |
| hint line | `#928374` | `j/k move · ↵ view · ? help` |
| filter-dimmed rows | fg `#504945` (marks too) | |
| collapse arrows `▾`/`▸ ` | `#928374` | |

Behaviors:

- **Layout:** title row, blank, tree rows filling `height-4`, blank, hint row. Gutter marks right-aligned at cols 27–29; labels truncated with `…` to fit; marks never truncated.
- **Scroll:** viewport-window over rows with `↑ N more…` / `↓ N more…` indicator rows in `#928374` replacing the edge row when overflowing (mockup screen 5). Cursor stays in view.
- **Collapse (`tab`):** `map[string]bool` in the model, in-memory only; collapsed session hides its window rows and shows the aggregate mark; `▸ ` prefix.
- **Filter (`/`):** mode with live query on the hint line (`/query▉`, `/` in `#fabd2f`); matching = case-insensitive substring against `session` and `index:name`; non-matching rows render dimmed **in place** (no reflow — positions are sacred, per mockup); `j/k` skips dimmed rows; `esc` clears; `↵` exits filter mode keeping the filter (second `esc` clears).
- **Create (`n` / `a`):** `n` turns the hint line into `new session: name▉`; `↵` runs `tmux new-session -d -s <name> -c ~` and points the viewport at it; empty input or tmux error flashes the error in `#fb4934` on the hint line for 3s. `a` creates `gm-agent-NN` (lowest free NN, generalizing `freeGMName` to a prefix) with no prompt, points viewport at it.
- **Kill (`x`):** hint line becomes `kill <name>? y/n` (`y` in `#fb4934`); `y` → `tmux kill-session -t '=<name>'`; if it was the viewport lock, viewport goes idle.
- **Empty state:** when the tree is empty, render mockup screen 4's rail body (`no sessions yet`, `n`/`a` key hints, three-line explainer) and ensure the viewport is idle.
- **Help (`?`):** run `tmux display-popup -E -w 58 -h 24 '<exe> rail help'` via a tea.Cmd; `rail help` prints the mockup screen-6 keymap (keys `#fabd2f` bold, gutter legend in true colors; display-popup draws the border — do **not** draw a second box) then blocks on a single byte from stdin. If `display-popup` returns non-zero, fall back to a full-rail help overlay page (swap View() body).
- **Keys final map:** `j/k/↓/↑` move, `g/G` first/last, `↵` view, `tab` collapse, `n` new, `a` agent, `x` kill, `/` filter, `r` refresh, `d` detach viewport, `?` help, `q`/`ctrl+c` quit. Matches the help popup exactly.

---

## 5. Headless testing — extend `rail once` (Task 11)

`rail once` grows flags and becomes the acceptance harness:

```
ghostmux rail once            # plain rows, as today, using live tmux
ghostmux rail once --filter q # rows after filter dimming: dimmed rows prefixed "·"
ghostmux rail once --marks    # rows as "SESS|WIN|bell,done,act,view" machine format
```

Plain format per row: `{indent}{*active}{gutter:2} {label}` (unchanged), with new marks included in gutter.

Two test layers:

1. **Unit tests** (`rail_test.go`): swap `tmux.Runner` with a fake returning canned `list-sessions`/`list-windows` output; assert row building, gutter priority, aggregation, done-transition logic, filter matching, collapse. No tmux needed. Must pass with `go test ./...`.
2. **Integration script** (`test/hub_test.sh`, bash): drives a scratch server via `tmux -L gm-test -f /dev/null`. To keep tests off the real server, `internal/tmux` honors env `GHOSTMUX_TMUX_ARGS` (e.g. `-L gm-test`), prepended to every tmux invocation. Script asserts `rail once` output with `grep`, creates a bell (`send-keys 'printf "\a"' Enter`), asserts `●` appears, kills the server, traps cleanup.

---

## 6. Task breakdown

Execute in numeric order; tasks marked ∥ may run in parallel after their dependency.

| # | Task | Route | Deps | Acceptance criteria (headless where possible) |
|---|---|---|---|---|
| **1** | **Repo restructure** to §1 layout, mechanical move, `internal/tmux` runner + typed queries, all commands migrated to it | Sonnet | — | `go build ./... && go vet ./...` clean; `./ghostmux help` byte-identical to baseline except `rail dock` line removed and `hub` line added (stub ok); `GHOSTMUX_TMUX_ARGS='-L gm-t1' ghostmux rail once` output format unchanged vs prototype for a scripted 2-session server; `cmd/ghostmux/main.go` ≤ 120 lines; no file at repo root except go.mod/go.sum/README/.gitignore |
| **2** | **`GHOSTMUX_TMUX_ARGS` + unit-test seam**: fake runner, first unit tests for existing row building | Sonnet | 1 | `go test ./...` passes using the fake runner only (no tmux dependency) |
| **3** | **`ghostmux hub`** per §2, incl. `prefix None`, `-w`, idempotency, broken-layout rebuild, `rail dock` removal, hub kill-session on rail quit | **Opus** | 1 | Script: on scratch socket with hidden `--no-attach` flag; assert exactly 2 panes, left width 30, one running `ghostmux`; `show-options -t hub prefix` → `None`; second `hub --no-attach` run creates nothing new (pane ids unchanged) |
| **4** | **Viewport manager** (§3.2): point/heal/idle/detach, `rail idle` subcommand | **Opus** | 3 | Script: point viewport at `alpha`, kill inner client, wait 2s → `#{pane_dead}` back to 0 (healed), `pane_current_command` is `tmux` again; after simulated `d`, viewport runs `ghostmux` (idle) and stays idle across ticks |
| **5** | **Events + blink** (§3.3): hooks `[133]`, wait-for goroutine, conditional 400ms blink timer | **Opus** | 1 | Script: hook list shows exactly the 7 `[133]` hooks while rail runs; bell in another session triggers refresh <300ms (`GHOSTMUX_DEBUG=1` timestamps on stderr); after rail exit, `show-hooks -g` contains no `[133]` entries |
| **6** | **Gutter v2** (§3.1): done-tracking via command transitions + `@ghostmux_done`, aggregation, priority, clear-on-view | **Opus** | 2 | Unit tests: transition table (cmd→shell unattended ⇒ done; attended ⇒ not; shell→cmd ⇒ clears nothing; view ⇒ clears). Integration: `sleep 2` via send-keys; after exit, `rail once --marks` shows `done`; `show -w @ghostmux_done` = `1` |
| **7** ∥ | **UI v2** (§4 table + layout): gruvbox hexes, cursor bar, truncation, scroll indicators, collapse, empty state | Sonnet | 6 | `rail once` unaffected (plain); visual check by George; unit tests for truncation width math and scroll-window row selection (write them as pure functions) |
| **8** ∥ | **Filter mode** (§4) + `rail once --filter` | Sonnet | 7 | `rail once --filter agent` dims (prefix `·`) exactly the non-matching rows; positions/order identical to unfiltered output |
| **9** ∥ | **Create/kill flows** (`n`, `a`, `x`, confirm, error flash) | Sonnet | 7 | Factor as `createSession(name)`, `agentSession()`, `killSession(name)` in model.go; unit/integration test directly; `agentSession` with `gm-agent-00` present creates `gm-agent-01` |
| **10** ∥ | **Help popup** (`?`, `rail help`, popup-failure fallback) | Sonnet | 7 | `ghostmux rail help </dev/null` exits 0, prints ≥12 key lines incl. `ctrl+b ctrl+b`; keymap single-sourced from a `[]keyHelp` slice used by both Update() and help printing — enforced via unit test |
| **11** | **Test harness**: `rail once --marks`, `test/hub_test.sh` covering tasks 3–6 assertions end-to-end | Sonnet | 3–6 | `bash test/hub_test.sh` exits 0 with tmux 3.4; leaves no `gm-test` socket behind (trap cleanup) |
| **12** | **Docs + doctor**: README hub section (purist-test framing), doctor checks hub layout + hook residue + `prefix None`, usage text final | Sonnet | 3,5 | `ghostmux doctor` reports hub checks; README contains no `rail dock`; roadmap lists §7 phase-3 items |

Verification pass (after 12): `test/hub_test.sh`, `go test ./...`, then a live smoke: `ghostmux hub` in ghostty, mockup-vs-reality check against screens 1–6.

---

## 7. Out of scope

**Deferred to phase 3** (post ghostty 1.4, ~Sept 2026):
- Go tmux control-mode (`-CC`) client library; event-driven refresh then migrates off hooks/wait-for.
- Ghostty scripting-API bridge (native splits for panes, window focus).
- termtile X11 focus integration (D8 — seam reserved as `clientTTY` data only).
- Real OSC 133 prompt-mark semantics for ✓ (replaces D5's command-transition heuristic).
- tmux-resurrect composition beyond what `restore` already does.

**Cut** (not deferred — rejected):
- Mouse support inside the rail TUI itself (mouse-first applies to the viewport; rail is keyboard-native).
- Collapse-state persistence across rail restarts.
- Custom theming of the outer/inner tmux status bars (user's tmux config territory; fails the purist test).
- Activity pulse animation (mockup marks it optional; skip).
- Pane-depth (depth 2) rows in the tree — sessions and windows only.
- Any config file for ghostmux itself.
