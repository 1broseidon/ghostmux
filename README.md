# ghostmux

Attach-anywhere mission control for your multiplexers.

## The idea

Type `ghostmux`. You're in the hub: a persistent rail listing every tmux
session with a live attention gutter, beside a viewport that renders
whatever you select as a live nested client. The rail never moves —
selection re-points the viewport, never you.

The hub is **itself a tmux session**. That one decision carries the whole
product:

- **Attach from anywhere.** ghostty on Linux, iTerm2 over ssh, a bare VT —
  the cockpit is identical everywhere, because the entire control surface
  lives in the multiplexer, never in the terminal. Disconnect, reconnect,
  and it's exactly as you left it: layout, viewport lock, cursor and all.
- **Your sessions stay pristine.** All chrome lives in the `hub` session.
  Unlike sidebar plugins that inject UI panes into your working sessions,
  ghostmux never touches them — your layouts, snapshots, and scripts see
  only what you made.
- **Multi-client sane.** Viewing a session that's attached elsewhere (an
  ssh client, another window) goes through a transient grouped session:
  independent size and focus, the other client never feels you looking.

Two laws govern the design. **Evidence, never inference**: the rail renders
only what it can observe — a bell that rang, a command that exited, output
you haven't seen — and never animates a claim it can't prove. **The purist
test**: a feature ships only if the multiplexer alone can't give it to you.

## Commands

- **`ghostmux`** — the hub (create or attach). This is the product.
- **`up <name> [dir]`** — attach a named session in place, creating on
  demand; switches clients instead of nesting when already inside tmux.
- **`ls`** — list sessions. `doctor` — diagnose the environment.
- **`rail once|idle|help`** — hub internals; `rail once --marks` is the
  machine-readable fleet state.

## Inside the hub

- **Keymap:** `j/k`/`↓↑` move · `g`/`G` first/last · `↵` view in viewport ·
  `tab` collapse/expand · `n` new session · `x` kill (`y`/`n` confirm) ·
  `/` filter · `r` refresh · `d` detach the viewport · `?` help ·
  `q` quit (tears down the hub). Mouse: click to focus panes, click a row
  to select, click again to view, wheel to scroll.
- **`prefix None` on the hub session** — `ctrl+b` passes straight through
  to the inner session's tmux. Single prefix everywhere.
- **Gutter:** `●` bell · `✓` done (foreground command exited to a shell
  while unwatched) · `~` activity · `▸` in the viewport. Session rows
  aggregate the highest-priority mark; viewed rows show no marks (you're
  looking at them). Title row totals what wants you (`●2 ✓1`).
- **One kind of session.** Agent-ness is ambient: a slot whose foreground
  command is a recognized agent CLI (`claude`, `codex`, `aider`, ...)
  renders in the agent accent. Observed, never declared.

## Ghostty extras (optional)

For ghostty users, `ghostmux ghostty install` wires one nav keymap across
all three layers: `ctrl+h/j/k/l` moves between ghostty splits, tmux panes,
and vim windows — ghostty consumes a chord only when it can actually move
(`performable:`), otherwise it falls through. `ghostmux ghostty uninstall`
reverts. Marker-delimited, reversible, and entirely optional: nothing in
the hub depends on ghostty.

## Requirements

tmux >= 3.2, Go 1.24+ to build (`go build -o ghostmux ./cmd/ghostmux`).
Any terminal.

## Roadmap

- **Multi-backend**: one rail for every multiplexer on the box — zellij
  first (session-level: list/attach/create/kill, honestly degraded gutter),
  screen later. Backends earn features by proving data, never by faked
  parity. The `Backend` interface extraction from `internal/tmux` is the
  first step.
- Go tmux control-mode (`-CC`) client library; event refresh migrates off
  hooks/wait-for.
- Deeper evidence for the gutter (output sampling, OSC 133 / terminal
  progress reporting where available) — held to the evidence law.
- Rail rename-in-place; `ctrl+o/i` jump list.
- termtile X11 focus integration (session → window mapping via
  `client_tty`), and a ghostty scripting-API adapter — both optional
  integrations at the edge, like the ghostty extras.
