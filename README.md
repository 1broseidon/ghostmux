# ghostmux

The coordination layer for the ghostty/tmux boundary.

## The problem

Your terminal and your multiplexer each stop dead at the other's boundary —
and your workflow doesn't. tmux can't reach up: it can't spawn terminal
windows, yield keys to the terminal, or see ghostty splits. Ghostty can't
reach down: it has no idea what a session is, and its windows die with it.
Everyone running tmux inside ghostty maintains that seam by hand: two config
files that must mirror each other exactly, a navigation keymap that
half-works, terminfo hacks, and `command=` one-liners with race conditions.

ghostmux owns the seam. **The purist test is the admission criterion: a
feature ships only if neither tmux nor ghostty could do it alone.**

## The hub

`ghostmux hub` is the entry point. Run it once, stay there. It creates (or
attaches to) a dedicated session named `hub`: a persistent rail pane on the
left (30 columns) and a viewport pane on the right, both built and owned by
ghostmux — never by claiming a pane you were already using.

- **The rail never moves.** It's a live tree of every tmux session and
  window (excluding `hub` itself), with a gutter that shows what needs your
  attention. Pressing `↵` on a row doesn't move the rail — it re-points the
  viewport pane at that session/window instead.
- **`prefix None` on the hub session.** The rail's own keys (`n`, `x`, `d`,
  `q`, `↵`, `/`, `tab`, ...) replace every prefix command you'd reach for
  there, so the hub's outer tmux needs no prefix of its own — `ctrl+b` in
  the viewport passes straight through to the inner session's tmux
  (single prefix everywhere, no `ctrl+b ctrl+b`). Running `rail` by hand
  outside the hub keeps your normal prefix; the help popup documents
  `ctrl+b ctrl+b` for that case.
- **Mouse click-to-focus.** The hub sets `mouse on` session-scoped, so
  clicking between the rail and the viewport focuses them, regardless of
  your own tmux mouse setting elsewhere.
- **Keymap:** `j/k`/`↓↑` move · `g`/`G` first/last · `↵` view in viewport ·
  `tab` collapse/expand · `n` new session · `a` new agent session
  (`gm-agent-NN`) · `x` kill (`y`/`n` confirm) · `/` filter · `r` refresh ·
  `d` detach the viewport (goes idle) · `?` help popup · `q` quit (tears
  down the hub).
- **Gutter legend:** `●` bell · `✓` done (the foreground command exited back
  to a shell while you weren't looking) · `~` activity · `▸` currently in
  the viewport. Highest-priority mark wins when a session aggregates its
  windows.

## Boundary commands

- **`hub`** — see [The hub](#the-hub) above: the coordination surface for a
  fleet of sessions, rendered through nested clients — tmux alone gives you
  `choose-tree` (modal, blocking), ghostty alone gives you nothing.
- **`ambient on|off`** — the wow switch. Every new ghostty surface becomes a
  persistent tmux session (`gm-*`), no typing ever. Quit ghostty with four
  windows of running processes; open ghostty again and the first window
  reclaims its session *and unfolds the rest* — the other windows reopen
  themselves, reattached, processes intact. Seam-correct where the
  `command=` one-liners aren't: quick terminal exempted
  (`GHOSTTY_QUICK_TERMINAL`), never nests inside tmux, claims are
  flock-serialized so parallel windows can't race into one session,
  reclaim order is deterministic (lowest index first).
- **`install` / `uninstall`** — matched-pair config for both sides of the
  seam: one `ctrl+h/j/k/l` keymap across ghostty splits, tmux panes, and vim
  windows. Ghostty consumes a key only when it can move (`performable:`);
  otherwise it falls through to tmux, which forwards to vim when the pane
  runs vim. Wiring is marker-delimited and reversible; snippets live in
  `~/.config/ghostmux/`.
- **`restore`** — reopen a ghostty window for every orphaned (unattached)
  tmux session. Stateless: tmux owns session truth, ghostmux does the half
  tmux can't — opening terminals. Quit or crash ghostty, run `restore`, and
  every session gets its window back. Across reboots it composes with
  tmux-resurrect: resurrect restores the sessions, ghostmux restores the
  windows.
- **`up -w <name> [dir]`** — a ghostty window attached to a named session,
  created on demand.
- **`doctor`** — diagnose the seam: versions, `xterm-ghostty` terminfo,
  config wiring on both sides.

## Conveniences

`up <name> [dir]` attaches in place (and switches clients instead of nesting
when already inside tmux). `ls` lists sessions. Both fail the purist test —
they're aliases — and stay only because they cost nothing.

## Requirements

ghostty >= 1.3 (`performable:` fall-through, `+new-window -e` forwarding),
tmux >= 3.2, Linux/GTK. Build: `go build -o ghostmux .`

## Known seams

- Fall-through is top-down: mixing ghostty splits *and* tmux panes in one
  window routes keys to ghostty first. Convention: one layer per window.
  A true fix needs ghostty 1.4's scripting API.
- `ctrl+l` inside tmux navigates; clear screen is `prefix ctrl+l`.
- ghostty `copy-on-select = clipboard` can fight tmux copy-mode.

## Roadmap

Phase 2 shipped the hub: rail + viewport, event-driven refresh, the
attention gutter. Deferred to phase 3 (post ghostty 1.4, ~Sept 2026):

- Go tmux control-mode (`-CC`) client library; event-driven refresh then
  migrates off hooks/wait-for.
- Ghostty scripting-API bridge (native splits for panes, window focus).
- termtile X11 focus integration (D8 — seam reserved as `clientTTY` data
  only).
- Real OSC 133 prompt-mark semantics for ✓ (replaces D5's command-transition
  heuristic).
- tmux-resurrect composition beyond what `restore` already does.

Cut, not deferred — rejected:

- Mouse support inside the rail TUI itself (mouse-first applies to the
  viewport; rail is keyboard-native).
- Collapse-state persistence across rail restarts.
- Custom theming of the outer/inner tmux status bars (user's tmux config
  territory; fails the purist test).
- Activity pulse animation (mockup marks it optional; skip).
- Pane-depth (depth 2) rows in the tree — sessions and windows only.
- Any config file for ghostmux itself.
