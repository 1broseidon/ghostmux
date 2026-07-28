# Changelog

All notable changes to ghostmux are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

## [0.3.0] - 2026-07-28

The first public release. ghostmux is the tmux fleet navigator: an
always-visible rail of every tmux session — running or not — beside an
embedded terminal viewport, built for terminals you stop watching.

### Added
- **The panel owns the outer frame**: bare `ghostmux` opens a standalone
  Bubble Tea frame with an embedded terminal viewport (`tmux attach` on a
  real pty) — no outer tmux, per-client independent viewports, works over
  ssh. The old tmux-hosted hub is gone.
- **Groups**: persistent organization (`a`, `m`, `J`/`K`, one-level undo
  with `u`), fold state, and a versioned, lock-protected state file.
- **Ghosts**: a grouped session that stops becomes a summonable `○` row
  with its remembered working directory; `Enter` starts it again, `S`
  summons a whole group. Directory evidence is captured while sessions
  live (launch dir or last pane cwd — a setting).
- **The Return Queue**: `]` views the oldest unseen `●`/`✓` window,
  agent windows first, ordered by tmux's own `#{window_activity}`.
  Viewing drains it; the queue keeps no state of its own.
- **Agent lens**: ambient agent detection from the foreground command
  (accent + queue priority), quiet-age display (`claude 4m`), and a
  `doctor` report of detected agents and Claude Code bell wiring.
- **Help overlay (`?`) and settings mode (`,`)**: toggle rebind by key
  capture, rail width, agent list, fleet directory modes, read-only
  system facts.
- **Attention marks** from tmux evidence only: `●` bell, `✓` done (command
  returned to a shell unwatched), `~` activity, `▸` in view, `◆` attached
  elsewhere, `?` unvalidated. Bells and activity are acknowledged
  panel-side, including the unclearable grouped-attach bell flag and the
  panel's own departure-redraw output.
- **Event-driven refresh** via additive tmux hook leases with exact-match
  cleanup, alongside the 1s polling fallback; `doctor` reports stale
  leases.
- MIT license; Homebrew tap releases via GoReleaser
  (`brew install 1broseidon/tap/ghostmux`).

### Changed
- ghostmux is **tmux-only**: the v0.2 multi-backend prototype (zellij
  beside tmux) was removed. State entries written by that prototype are
  preserved on disk but never rendered.
- The default rail ⇄ viewport toggle is the single chord `ctrl+alt+\`,
  rebindable in settings or via `GHOSTMUX_TOGGLE`.

## [0.1.0] - 2026-07-23

- Initial prototype: tmux-hosted hub with a rail pane, session/window
  tree, attention gutter, and viewport pane driven by nested clients.
