# HANDOFF — 2026-07-27 session (evening)

Working tree: `main`, everything committed this session (one commit on top of
`36f8e41`, which also carried the previously uncommitted settings/ghost-dir
work). `go test ./...`, `go vet ./...` green; `test/panel_test.sh` 69/69.

## Shipped this session

### 1. The tmux-only cut (recorded decision `product.tmux-only`, now executed)
- `internal/rail/backends.go` deleted wholesale (zellij query/parse, aux
  create/kill/delete, probes). `z` key, `PointAux`, `AuxAttachArgv`,
  `zellijCache`, `selfAux`, `killDelete`/`deleteGhost` all gone.
- Identity plumbing simplified: `ViewState`/`viewRef`/`railRow` lost their
  `backend` fields; `memberKey(sess)` = `"tmux:" + sess` (on-disk format
  unchanged); `Viewport.OnKill(sess)`; `InHost(sess)`; `createSession(name)`.
- State compat: keys with a foreign prefix (`zellij:x`) from the prototype
  are preserved in groups.json but never rendered (`foreignKey` in
  groups.go; tested in outage_test + groups_test round-trip).
- App layer: `ptyViewport` is tmux-only (no lockBackend/probeAuxSession);
  settings System section probes only tmux; doctor drops the zellij check;
  main.go/help/hints reworded. All zellij-specific tests deleted or
  converted; `zellij_live_test.go` removed.

### 2. Return Queue — the flagship loop (decision `product.flagship-loop`)
- `]` in normal mode: `returnOldest()` (model.go) views the attention leaf
  with the minimum `#{window_activity}` among rows carrying ●/✓. Membership
  = exactly `attentionLeaf && (bell || done)` — same census as
  `AttentionSummary`, so the footer's attention cluster IS the queue depth.
- No queue state: drain happens through the existing clearing paths (native
  bell clear on display, `@ghostmux_done` clear-on-view, activity ledger).
  Searches raw rows so folds can't hide entries. Empty queue → "queue
  empty"; refused view → "view unavailable", nothing else changes.
- `railRow.activityAt` threaded from `tmux.Window.ActivityAt` (flat +
  window rows). Help overlay row: `] oldest unseen ●/✓`.
- Tests: `internal/rail/returnqueue_test.go` — drain order via a stateful
  fake-tmux runner (clears bell on display, honors set-option), empty
  queue, membership exclusions, folded reachability, refused view.
- Binding contract: **docs/SPEC-QUEUE.md**.

### 3. Docs own the tmux story
- README rewritten: "the tmux fleet navigator", agent-first framing
  (ambient fleet / dead slots / Return Queue as the three things tmux
  doesn't have), accurate key table (old one documented the deleted hub
  keymap), tmux-only requirements and limitations.
- Scope note prepended to SPEC.md, DESIGN.md, RETURN.md, SPEC-GHOSTS.md,
  SPEC-CHROME.md: zellij references in them are historical.

### 4. Harness repairs (panel_test.sh, now 69 checks)
The integration script had drifted against the previous session's
uncommitted keymap trim — it caught real mismatches nobody had run:
- fold-group step used the removed `g` key → now `h`,`h` via semanticLeft;
- help check grepped removed row text; settings check expected the old
  "Backends/About" sections → now Fleet/System;
- zellij sections removed; added a check that `]` is documented in `?`.
**Law worth keeping: run panel_test.sh whenever keys or chrome change.**

## Carried in the same commit (built last session, was uncommitted)
Ghost-dir setting (launch vs last pane cwd), create-dir setting (home vs
current), settings consolidation to 4 sections (Fleet/Agents/Panel/System),
`tmux.Session.CurrentPath` + `Window.PanePath` (11-field window format).

## Next steps

1. **Live feel-pass on `]`** — George drives the queue against his real
   fleet (agents in several sessions, let two finish, triage with `]`,
   bounce back with `` ` ``). Open UX questions: should the flash show
   remaining depth ("return · api · 2 left")? Should agent windows
   (agentCmds) outrank plain bells (cheap v2)?
2. **Morning Summon** remains the candidate follow-up loop (recency on
   ghost rows `api ○ 2h`, summon-all per group after boot, viewport
   restore). Not chosen; Return Queue shipped first per decision.
3. Optional cleanup: `movePage`/`moveNonWindow` (+ their pure helpers in
   navigation.go) are production-dead since the keymap trim — kept only by
   tests. Cut or rebind deliberately.
4. Optional: grouped attach (session attached elsewhere) may not clear
   native bell flags on view since no client displays the origin winlink —
   suppressViewedMarks hides them while viewed, but such a window could
   re-enter the queue after pointing away while still attached elsewhere.
   Observe in real use before engineering anything.

## Key files
- `internal/rail/model.go` — `returnOldest`, tmux-only refresh/kill/summon
- `internal/rail/groups.go` — `memberKey`/`foreignKey`, single-validity
  `applyGroups`
- `internal/rail/returnqueue_test.go` — the queue's contract in tests
- `docs/SPEC-QUEUE.md` — the queue's binding contract
- `test/panel_test.sh` — 69-check acceptance harness
