# SPEC — Owned chrome

Status: contract written 2026-08-02, implementation assigned. Binding; where
an implementation choice contradicts it, the spec wins.

## What it is

The sessions ghostmux owns — `gm-wall-*` composites and `gm-view-*` shadows —
currently wear stock tmux chrome: a redundant status bar that leaks internal
names (`[gm-view-a1b2…`), unlabeled pane borders. Owned chrome dresses the
frame ghostmux owns in ghostmux's own design language while leaving every
user session byte-for-byte native: the wall reads
`┌ ada ┐ ┌ beastie ┐ ┌ ifrit ┐` with member names engraved in themed borders,
and no owned session ever shows a status bar again.

## Laws (binding)

1. **Style only what is owned.** Every appearance option is set with an
   exact `-t <session-id>` (or that session's windows/panes) on a session
   tagged `@ghostmux_view_owner`. No global (`-g`) option is ever written,
   no user session or its windows are ever styled, and member pane CONTENT
   is the member rendering itself — untouched by definition.
2. **Chrome may fail; the view may not.** Styling is cosmetic. A failed
   `set-option`/`select-pane -T` is ignored (the wall works unstyled);
   it never aborts creation, never reaches the error surface, and never
   retires a capability.
3. **Identity is engraved, not leaked.** Wall pane borders carry the
   member's ORIGIN session name (never the shadow's `gm-view-*` name).
   Owned sessions run `status off` — the frame's bottom bar owns identity,
   and a bar that shows an internal name is worse than none.
4. **Colors follow the theme seam.** Border styles resolve through
   `internal/theme` like every other color: gruvbox hex by default, the
   terminal palette under `GHOSTMUX_THEME=ansi`. tmux spells ANSI indices
   `colour<n>` and hex as-is; `theme` gains a `Tmux(c string) string`
   adapter so no call site hand-converts.

## Mechanics

- **Wall composite** (in or beside `CreateWall`, best-effort after the
  session exists):
  - `status off` (session option on the wall).
  - Window options on the wall's window: `pane-border-status top`,
    `pane-border-format` rendering ` #{pane_title} `,
    `pane-border-style` in the dim border color,
    `pane-active-border-style` in the accent.
  - Each pane titled with its member's origin: `select-pane -T <origin>`
    for the first pane and after each `split-window`. Origins must
    therefore reach the create path alongside the shadow refs.
- **Shadows** (`CreateView`, both single grouped views and wall members):
  `status off` on the shadow session. This removes the `[gm-view-…`
  truncation leak from single grouped views; the frame's `▸` label and the
  wall's border titles carry identity instead.
- Colors: the app layer resolves two strings via `theme` (border dim,
  border accent) and passes them down; `tmux` package accepts them as
  opaque style values. `theme.Tmux` converts spelling only.
- README: extend the tmux-integration section — owned sessions wear
  ghostmux chrome, user sessions stay native, and viewing an unowned
  session shows its own untouched status bar (the asymmetry is the
  feature, stated plainly).

## Tests (required)

- Unit (`internal/tmux` against the fake runner): wall creation emits
  `status off`, border options, and one `select-pane -T <origin>` per
  member — every command targeting the owned session's exact ID; a failing
  styling command does not fail creation (law 2). `CreateView` emits
  `status off` on the shadow, same tolerance.
- Unit (`internal/theme`): `Tmux("#fe8019") == "#fe8019"`,
  `Tmux("9") == "colour9"`.
- Harness (self-contained, extending or beside §wall): while walled, the
  capture shows each member's origin name in a border title and does NOT
  contain the string `gm-view`; `show-options` on a member session proves
  no `pane-border-status`/`status` option was written to it; after
  teardown, members are intact. A single grouped view (member attached
  elsewhere) shows no `[gm-view` in the capture.

## Key code (expected shape)

- `internal/tmux/view.go` — styling applied at wall/shadow creation;
  origins threaded into `CreateWall`
- `internal/theme/theme.go` — `Tmux`
- `internal/app/viewport.go` — resolves the two border colors, passes
  origins + colors down
