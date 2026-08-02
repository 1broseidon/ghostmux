# SPEC — Unread

Status: shipped 2026-08-02. This file is the binding contract. Unread
completes the inbox the Return Queue started: the queue answers *which*
window wants you, Unread answers *what happened there*.

## The problem it solves

Landing in a window you haven't watched means reconstructing what happened
by scrolling — manual archaeology, performed dozens of times a day by anyone
running agents. Chat software killed "scroll up to find what's new" decades
ago. Unread kills it for terminals: rows carry a banked count of unseen
lines, and one key shows exactly that output.

## Laws (binding)

1. **The count is arithmetic, never capture.** A pane's absolute write
   position is `#{history_size} + #{cursor_y}` (history alone in the
   alternate screen, where the TUI cursor proves nothing). Banked lines =
   position now − position when last viewed. tmux's numbers, subtracted.
2. **Growth counts only when output is proven.** The totals move on resize
   reflow without a byte being emitted; a delta unaccompanied by a
   `#{window_activity}` advance is absorbed, never banked. The departure
   settle absorbs the panel's own redraw the same way it does for marks.
3. **Viewing drains; shrink wipes.** Landing in the window zeroes the bank
   (same acknowledgement as marks and bells). A shrinking position (clear,
   pane death) rebaselines to zero rather than going negative or claiming
   lines whose content is gone.
4. **Text is fetched lazily.** The rail never captures pane content on a
   tick. `[` captures the tail once, on demand, capped at 200 lines — the
   count stays exact; only the peek is capped, and the title says so.
5. **The peek claims what it can prove.** An alternate-screen window's
   title carries `· TUI`: its line history under-describes a full-screen
   program, and the pager must not pretend otherwise.

## Mechanics

- Rows render banked lines as `+N` (capped display `+999`) flush against
  the attention marks; the viewed row's count is suppressed with its marks.
- `[` (frame key, rail focus, not mid-prompt — the `]` key's sibling) opens
  the selected row's unseen tail in a centered pager: `j/k`/arrows scroll,
  `g`/`G` jump, any other key closes. Nothing banked → no-op.
- `]` is unchanged; because viewing drains, walking the queue now also
  clears the counts.

## Key code

- `internal/tmux/query.go` — `PaneStat` (8-field pane query), `CaptureTail`
- `internal/rail/activity.go` — line banking in the one acknowledgement
  ledger (marks, bells, lines: one pattern, three evidences)
- `internal/rail/model.go` — `UnreadPeek`
- `internal/app/overlay.go` — the pager
- Tests: `unread_test.go`, `activity_test.go`, chrome pager test, harness
  §unread
