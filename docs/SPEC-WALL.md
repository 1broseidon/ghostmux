# SPEC — The Wall (group view)

Status: contract written 2026-08-02, implementation assigned. This file is
binding; where an implementation choice contradicts it, the spec wins.

## What it is

`v` on a group header composes every live member into one tmux window of
real split panes — ada | beastie | ifrit, side by side, live and
interactive — shown in the viewport like any session. tmux owns the panes
(navigation, mouse focus, `prefix+z` zoom to one member and back); ghostmux
only composes and owns the composite. Purist test: tmux alone cannot show N
sessions at once without hand-built plumbing; ghostmux alone has no panes.

## Laws (binding)

1. **Composition is ownership.** The wall is one owned session
   (`gm-wall-*`) tagged exactly like `gm-view-*` shadows, plus one owned
   grouped shadow view per member. Teardown retires every capability
   through the existing exact-ownership path (`KillViewIfOwned`, pending
   retirements). Startup never sweeps by prefix. A crash may leave owned
   residue; `doctor` reporting it is acceptable, destroying unowned
   sessions is not.
2. **Panes attach shadows, never originals.** Each wall pane runs a nested
   attach to a per-member grouped shadow (the `CreateView` machinery),
   NEVER a direct attach to the member — a direct attach would join the
   user's other clients and fight them for window focus. Members that are
   ghosts or not fresh are skipped and counted.
3. **The wall acknowledges its members.** Shadow display cannot clear
   origin winlink flags natively, but the operator IS looking at every
   member. While the wall is up, the ledger treats each walled member's
   active window as viewed: marks, bells, and unread drain through the
   same acknowledgement path as a normal view. This is the panel-local
   truth "you saw it", recorded the way bell-ack already records it.
4. **Enter stays fold.** Keyboard/mouse parity is law; a click on a group
   folds and Enter must match. The wall is `v` only. `v` is "show me THIS
   group": on the walled group it closes, on any other group it switches
   directly in one press — never a blind toggle. On a non-group row it
   closes an open wall, else flashes `v views a group`.
5. **Arrangement is the group's order.** Panes tile in the group's
   persisted member order, so `J`/`K` — the existing reorder keys — are
   the wall's layout editor. No second ordering mechanism exists.
6. **Bounded honestly.** At most 6 members tile; more flashes
   `wall: first 6 of N`. No live members flashes `nothing to wall`. Caps
   are announced, never silent.

## Mechanics

- `v` (rail, normal mode) on a group header:
  1. Collect fresh, non-ghost member sessions (raw rows, fold-independent),
     cap 6.
  2. Viewport `PointWall(group, members)`: retire the current child/view;
     create per-member shadow views; create the owned wall session sized to
     the viewport; first pane + `split-window` per remaining member, each
     running the nested shadow attach with `TMUX` unset; `select-layout
     tiled`; attach the wall session as the child.
  3. Lock reports `Sess: group, Wall: true`. The rail records the walled
     member set for ledger acknowledgement and cursor-follow (the group
     header is the wall's row).
- Teardown — `v` on the walled group (a different group switches in one
  press), pointing at any row, `d`, quit, or heal
  finding the wall session gone — kills the wall session and retires every
  member shadow. Failed retirements go to the pending-retirement pool
  exactly as single views do today.
- Heal: a dead wall child with the wall session still present re-attaches;
  an absent wall session idles. Member shadows whose origin died are
  retired; the wall survives with the remaining panes (tmux handles a dead
  pane's exit natively).
- Departure: leaving the wall resizes members back; the existing
  departure-settle absorption already covers the redraw storm. No new
  mechanism.

## Keymap and chrome

- Help table gains `v · group wall`; the table's length assertion in
  `TestKeyHelpCoversBoundKeys` moves from ≤20 to ≤22 (the row is earned).
- `boundKeys` in that test gains `"v"`.
- The bar's viewport-focus `▸` label reads the group name while walled.
- README: one paragraph under "Ghosts and the Return Queue" territory plus
  a key-table row.

## Tests (required)

- Unit: member collection (fresh/non-ghost filter, cap 6, count flash);
  `v` on non-group flashes; toggle semantics; ledger acknowledgement of
  walled members' active windows (marks/unread drain while walled, stay
  drained after leaving via the settle); fakeViewport `PointWall`
  recording.
- Viewport unit (`internal/app`): PointWall argv/creation order against
  the fake view-tmux and fake child seams; teardown retires wall + all
  shadows exactly once; heal paths (dead child vs absent session);
  pending-retirement on a failed shadow kill.
- Harness (`test/panel_test.sh`, self-contained section like §unread):
  group of two live members with distinct markers; `v` shows BOTH markers
  in one capture; member marks drain; `v` again tears down — no `gm-wall`
  or new `gm-view` sessions left; members intact with their content.

## Honest caveats (documented, not hidden)

- While walled, member windows resize to pane geometry — other attached
  clients see the squeeze. tmux physics; README says so.
- The wall acknowledges everything it shows (law 3): `v` is a bulk
  "I looked". That is its purpose; the spec says it out loud.

## Key code (expected shape)

- `internal/tmux/view.go` — wall-session create/kill beside the view
  machinery, same ownership tag
- `internal/rail/viewport.go` — `PointWall(group string, members []string)`
  on the Viewport interface; `ViewState.Wall bool`
- `internal/rail/model.go` — `v` key, member collection, walled-member
  ledger set, toggle/teardown routing
- `internal/rail/activity.go` — walled members' active windows count as
  viewed in `observeActivity`
- `internal/app/viewport.go` — the composite lifecycle
