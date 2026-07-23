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

## Boundary commands

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

Phase 2 formalizes the boundary tool (repo shape, seam-correct ambient
`shell` mode if it proves out). Phase 3, after ghostty 1.4 (~Sept 2026):
a Go tmux control-mode (`-CC`) client library — no Go implementation
exists — and a bridge to ghostty's scripting API / native tmux control
mode, where tmux panes become GPU-rendered native splits.
