# SPEC — Return Queue

Status: shipped 2026-07-27, with the tmux-only cut. This file is the binding
contract. The queue is the panel's flagship loop: the fleet as an inbox.

## The problem it solves

Agents, builds, and long jobs run in tmux sessions you stop watching. The
rail already shows their attention marks ambiently (`●` bell, `✓` done), but
acting on them meant scanning the rail and picking a row by hand. The queue
makes attention actionable: one key views the oldest thing that wants you,
and keeps pressing until nothing does.

## Laws (binding)

1. **Evidence, never inference.** Queue order is tmux's own
   `#{window_activity}` timestamp — the newest output the window ever
   produced, which for a `✓` window approximates "when it finished." No
   panel-side clock, no arrival bookkeeping.
2. **No queue state.** The queue is re-derived from row evidence on every
   press. Membership is exactly what `AttentionSummary` counts: fresh live
   windows (or flat sessions) carrying `●` or `✓`. Activity (`~`) stays
   gutter-only; ghosts, stale rows, group headers, and session aggregates
   are never queue entries.
3. **Viewing is draining.** No explicit acknowledge verb. The paths that
   already clear marks — tmux's native bell clear on display,
   `@ghostmux_done` cleared on view, the activity ledger's viewed
   acknowledgement — are the only dequeue mechanism.
4. **A fold hides rows from the eye, not from the queue.** `]` searches raw
   rows, so attention inside a collapsed group is still one press away.

## Mechanics

- `]` (rail focus, normal mode): find the attention leaf with the minimum
  `activityAt` among rows carrying `●` or `✓`; point the viewport at it;
  flash `return · <target>`. If the viewport refuses the exact view, flash
  `view unavailable` and change nothing else. Empty queue flashes
  `queue empty` and never moves the viewport.
- Repeated presses walk oldest → newest as each view drains its entry.
- Queue depth is already in the frame's bottom bar: the `●`/`✓` attention
  cluster is the queue, counted by the same `AttentionSummary`.
- `` ` `` (previous session) is the natural return ticket after a triage
  jump.

## Key code

- `internal/rail/model.go` — `returnOldest`, the `]` case
- `internal/rail/navigation.go` — `attentionLeaf` (membership)
- `internal/rail/rows.go` — `railRow.activityAt` threaded from
  `tmux.Window.ActivityAt`
- `internal/rail/returnqueue_test.go` — drain order, empty queue,
  membership, folded reachability, refused view
