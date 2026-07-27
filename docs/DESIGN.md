# ghostmux rail hub — complete screen spec (TUI mockup)

> **Scope note (2026-07-27):** ghostmux is tmux-only. The multi-backend
> prototype (zellij beside tmux) was cut after the v0.3 panel shipped;
> references to zellij, aux backends, or `z` in this document are historical
> and no longer describe the product. ghostmux is the tmux fleet navigator.

Design source of truth for the all-screens HTML artifact. This is a **terminal
mockup**, not a web app: every pixel belongs to a monospace character grid, every
color is from Gruvbox Dark Hard, all chrome is box-drawing characters and a tmux
status bar. The builder renders SIX terminal frames (screens below), stacked
vertically in one self-contained HTML page, each labeled with a small caption
above it (caption styling defined in §9).

Grounded in the real prototype at `/home/george/Projects/personal/ghostmux/rail.go`:
rail is a 30-col left tmux pane running a bubbletea TUI; enter re-points the right
viewport pane via `respawn-pane -k` + nested `TMUX= tmux attach`. The rail NEVER
moves. This spec evolves that prototype (adds ✓ done marker, filter, create flow,
help popup) — it does not reinvent it.

---

## 1. Canvas & character grid (identical for all six screens)

| Token | Value |
|---|---|
| Terminal grid | **152 cols × 44 rows** (43 content rows + 1 tmux status row) |
| Font | `"JetBrains Mono", "JetBrainsMono Nerd Font", monospace` |
| Font size / line height | **13px / 17px** (cell height = 17px exactly) |
| Cell width | **7.8px** (13 × 0.6 advance; builder: use `ch` units on a `<pre>`/grid so it is exact) |
| Terminal pixel size | 152 × 7.8 = **1185.6px wide**, 44 × 17 = **748px tall** |
| Terminal padding | 8px all sides of `#1d2021` before the grid starts (ghostty `window-padding-x/y = 8`) |
| Outer frame | ghostty window: 10px radius corners, 1px border `#3c3836`, no titlebar (George runs `window-decoration = none` style); drop shadow `0 12px 40px rgba(0,0,0,.55)` |
| Font smoothing | `-webkit-font-smoothing: antialiased`; `font-variant-ligatures: none` (terminal honesty) |

**Column layout inside the grid (cols are 1-indexed):**

- Cols 1–30 — **rail pane** (matches `split-window -hbf -l 30`)
- Col 31 — **pane divider**: `│` (U+2502) every row 1–43. Color `#504945`; when the
  rail pane is the *active* tmux pane (screens 1–6 except screen 3 mid-jump) the
  divider renders `#98971a` (tmux `pane-active-border-style` green, muted — NOT
  bright green; it's chrome, not signal).
- Cols 32–152 — **viewport pane** (121 cols).
- Row 44 — **tmux status bar**, full width (spec §4).

No horizontal page scroll ever: each terminal frame sits in a container with
`overflow-x: auto` for narrow browsers, page body never scrolls sideways.

---

## 2. Palette — Gruvbox Dark Hard, exact (the ONLY colors allowed)

| Role | Token | Hex |
|---|---|---|
| Terminal background | bg0_h | `#1d2021` |
| Status bar / popup bg | bg1 | `#3c3836` |
| Cursor-bar bg (rail selection) | bg2 | `#504945` |
| Pane divider, borders | bg3 | `#665c54` (inactive `#504945`) |
| Default foreground | fg1 | `#ebdbb2` |
| Secondary text | fg3 | `#bdae93` |
| Dim / comments / hints | gray | `#928374` |
| Bell ● / errors | bright red | `#fb4934` |
| Activity ~ / warnings | bright yellow | `#fabd2f` |
| Done ✓ / active window / attached ● | bright green | `#b8bb26` |
| Rail title, session names focus | bright aqua | `#8ec07c` |
| Prompts, links, selected-window title | bright blue | `#83a598` |
| Agent/AI accents, spinners | bright purple | `#d3869b` |
| Running ▸ / status-left block | bright orange | `#fe8019` |
| Muted green (active pane border) | green | `#98971a` |
| Muted yellow (status segments) | yellow | `#d79921` |
| Near-white (bold pop) | fg0 | `#fbf1c7` |

Type system: ONE font, ONE size. Hierarchy comes only from weight (400/700),
color, and reverse-video. Bold is allowed on: rail title, session names, bell
marks, popup title, status session block. Nothing else. No italics except
comments in code output (`#928374` italic allowed there — Gruvbox convention).

---

## 3. Rail anatomy (cols 1–30, rows 1–43)

```
row 1   ▍ ghostmux ▸ rail          ← title line
row 2   (blank)
row 3+  session/window tree
...
row 42  (blank)
row 43  j/k move · ↵ view · ? help ← hint line, gray #928374
```

- **Title line (row 1):** `▍` half-block in `#fe8019`, then `ghostmux` bold
  `#8ec07c`, ` ▸ rail` in `#928374`. Left-padded 1 col.
- **Session row:** 1-col left margin, then name, bold `#ebdbb2`
  (`#8ec07c` bold if it owns the viewport — see "viewport lock" below). If
  attached elsewhere: suffix ` ●` `#b8bb26`. Collapsible: prefix `▾ `/`▸ `
  in `#928374` (evolves prototype; collapsed shows aggregate gutter marks).
- **Window row:** indented 2 further cols: `{index}:{name}` — active window of
  its session in `#b8bb26`, others `#928374`. Truncate names at available width
  with `…` (U+2026), gutter marks are never truncated (right-aligned, see below).
- **Attention gutter:** marks sit right-aligned in cols 27–29, one col gap to
  divider. Glyphs, priority order (max 2 shown, highest first):
  - `●` bell — `#fb4934` bold, **blinking** (§8)
  - `✓` agent done — `#b8bb26` (OSC 133 command-finished while unattended; clears on view)
  - `~` activity — `#fabd2f`
  - `▸` running/attached-here (this session is in the viewport) — `#fe8019`
- **Cursor bar:** the selected row renders with background `#504945` across the
  FULL rail width cols 1–30 (not reverse-video: bg2 bar keeps glyph colors
  legible — evolves the prototype's `Reverse(true)`). Exactly one cursor bar per
  frame.
- **Viewport lock marker:** the session currently rendered in the viewport gets
  `▸` gutter on its session row and its name in `#8ec07c` bold. Cursor position
  and viewport lock are INDEPENDENT — this is the core interaction truth of the
  hub (browse without switching).
- **Hint line (row 43):** `#928374`, centered-ish (1-col left pad):
  `j/k move · ↵ view · ? help`. In filter mode this line becomes the filter
  prompt (screen 5).
- **Scroll indicators:** when the tree overflows rows 3–41: last visible row is
  replaced by `  ↓ 3 more…` `#928374` (and `  ↑ 2 more…` at top when scrolled).
  Rail scrolls; it still never *moves*.

## 4. tmux status bar (row 44, full 152 cols) — identical structure all screens

Gruvbox tmux theme, hand-rolled:

- Whole row bg `#3c3836`, fg `#a89984`.
- **Left segment:** ` hub ` — bg `#d79921`, fg `#1d2021`, bold; then one space of
  `#3c3836`.
- **Window list:** ` 0:rail* ` — current window: bg `#504945` fg `#fbf1c7`, the
  `*` in `#fabd2f`. (The hub session has only window 0.)
- **Right segment (right-aligned):** `ghostty 1.4 │ 23-Jul │ 14:32 ` — fg
  `#a89984`, separators `│` in `#665c54`; final block ` gm ` bg `#689d6a` fg
  `#1d2021` bold at extreme right.
- Screen-specific status-left session name changes are noted per screen (nested
  viewport shows the INNER session's status bar at viewport bottom — see §5).

## 5. Viewport anatomy (cols 32–152)

The viewport is a live nested tmux client, and the mockup must show that
honestly:

- Rows 1–42: content of the inner session's active pane.
- **Row 43: the INNER tmux status bar** — same structure as §4 but 121 cols wide,
  status-left shows the inner session name (bg `#689d6a` fg `#1d2021` bold, e.g.
  ` agent-web `), window list shows the inner session's windows
  (`1:agent*  2:test  3:logs`), right side just ` 14:32 `. This double-status
  seam is a real, owned artifact of the nested-client design — render it, don't
  hide it.
- Shell prompts in viewport content use a two-line prompt:
  `┌─(~/src/project)` gray + `└─❯ ` with `❯` in `#b8bb26` (or `#fb4934` after
  nonzero exit).

---

## SCREEN 1 — `hub-default` · rail + viewport, agent running

**Purpose:** the money shot. Establishes the hub at rest: rail on the left with a
small healthy tree, viewport rendering an AI agent mid-task in `agent-web`. Shows
viewport lock (▸), attached marker, active-window coloring, double status bar.

**Rail tree (rows 3–14; cursor on `agent-web` session row; viewport lock also
`agent-web`):**

```
 ▾ agent-web ▸            ← #8ec07c bold, gutter ▸ #fe8019, CURSOR BAR bg #504945
     1:claude             ← #b8bb26 (active window)
     2:test               ← #928374
     3:logs            ~  ← #928374, gutter ~ #fabd2f
 ▾ agent-api
     1:claude             ← #b8bb26
     2:server             ← #928374
 ▾ dotfiles ●             ← attached elsewhere: ● #b8bb26 after name
     1:nvim               ← #b8bb26
```

**Viewport content (agent-web, window 1:claude):** a Claude Code-style agent
transcript, believable and specific (this is George's actual use case — AI agents
in parallel panes). Compose ~34 rows:

- Line 1: `● claude-code — task: wire OSC 133 done-marker into rail gutter` in `#d3869b`.
- A tool-call block: `⏺ Read(rail.go)` `#8ec07c`, followed by 2 gray result lines
  (`Read 259 lines`).
- `⏺ Edit(rail.go)` then a small diff hunk: 3 context lines `#928374`, 2 removed
  lines prefixed `-` in `#fb4934`, 4 added lines prefixed `+` in `#b8bb26` —
  real-looking Go from the actual file (e.g. adding `done` to `railRow` gutter
  handling).
- A running line at bottom: `⠹ Running go build…` — braille spinner `#d3869b`,
  text `#ebdbb2`, spinner ANIMATES (§8).
- Last row before inner status: `esc to interrupt · ctrl+b ctrl+b for inner prefix` gray.

**Inner status bar:** ` agent-web ` green block · `1:claude* 2:test 3:logs` ·
right ` 14:32 `.

**States:**
- *Success (default, shown):* as above.
- *Loading (first paint, described in spec only for builder's state notes; render
  as inset caption variant if space permits):* rail shows title + `scanning tmux…`
  gray at row 3 for one tick (≤1s), viewport blank bg0_h.
- *Error (not inside tmux):* full-terminal state — no split, single pane printing
  `rail runs inside tmux — try `ghostmux up <name>` first, then `ghostmux rail dock`` in `#fb4934` on `#1d2021` after a shell prompt. (Render this
  as a small 152×8 mini-terminal beneath screen 1, captioned "error state".)

**Interactions documented in caption:** `j/k` moves cursor only; viewport
untouched. 1s tick refreshes gutter marks in place (no reflow flash).

---

## SCREEN 2 — `attention-states` · the gutter earns its keep

**Purpose:** show the rail as a notification surface while the user works on
something else. Viewport shows `dotfiles` vim; three different attention marks
glow in the rail simultaneously.

**Rail tree (cursor parked on `dotfiles` session row — same row as viewport lock
here; the OTHER sessions light up):**

```
 ▾ agent-web           ✓  ← ✓ #b8bb26 — agent finished (OSC 133) while unattended
     1:claude          ✓
     2:test
     3:logs
 ▾ agent-api           ●  ← ● #fb4934 bold BLINKING — bell (agent needs input)
     1:claude          ●
     2:server          ~  ← ~ #fabd2f — activity churn
 ▾ dotfiles ▸             ← viewport lock, name #8ec07c bold, CURSOR BAR
     1:nvim
```

Session rows aggregate their windows' highest-priority mark (bell > done >
activity) — visible here: `agent-api` shows `●` even though only window 1 rang.

**Viewport content:** vim editing `~/.config/ghostty/config` — realistic: line
numbers gray, a few config lines (`keybind = ctrl+j>performable:goto_split:down`
etc.) with values in `#b8bb26`/`#83a598`, comment lines italic `#928374`
(include George's real-world flavor: `# agents ring the bell when they need me`),
vim statusline row 42: reverse-video bg `#504945` fg `#ebdbb2`
` config  utf-8  ghostty-config  73%  ln 41:12 `, mode ` NORMAL ` block bg
`#83a598` fg `#1d2021` bold at left. Inner status bar: ` dotfiles ` ·
`1:nvim*`.

**Legend strip (part of this screen's caption, not the terminal):** the four
gutter glyphs with one-line meanings — ● bell / needs you · ✓ agent done ·
~ activity · ▸ in viewport.

**States:** default as above. *Cleared state* (documented): viewing a marked
window clears its marks (tmux clears bell/activity flags on visit; ✓ clears on
view). No separate frame needed — note in caption.

---

## SCREEN 3 — `jump-interaction` · enter re-points the viewport

**Purpose:** freeze the signature interaction mid-beat: user pressed `↵` on
window row `agent-api / 1:claude`. Rail unchanged (it NEVER moves); viewport has
just been respawned onto agent-api.

**Rail tree:** same as screen 2, with three changes:
- Cursor bar now on `    1:claude` under `agent-api`.
- `agent-api` bell marks are CLEARED (visit clears them), it now carries `▸`
  lock; name `#8ec07c` bold.
- `dotfiles` lost `▸`, back to plain bold fg1.

**Viewport content:** the just-attached agent-api claude window — an agent
waiting on a question (this is why it rang):

- Transcript tail (~10 rows): `⏺ Bash(go test ./…)` `#8ec07c`, gray result
  `2 packages · 1 failure`, then a short failing-test excerpt with `FAIL` in
  `#fb4934`.
- Agent question block: `● I can fix the flaky TestRespawnPane two ways:` then
  numbered options 1/2 (`#ebdbb2`), then an input prompt line
  `❯ 1. retry with -count=1  2. mark t.Skip  ` with a **block cursor** `▉`
  `#ebdbb2` steady-blink (§8) — the agent is literally waiting for input.
- FIRST row of viewport (row 1): the re-point seam made visible — a one-frame
  attach banner is NOT a thing in tmux; instead show honest freshness: the pane
  simply has less scrollback (content starts at row ~20; rows 1–19 empty bg0_h).
  Caption explains: respawn = fresh client, no stale scrollback.
- Inner status bar: ` agent-api ` · `1:claude* 2:server` · ` 14:33 `.

**Transition spec (motion, §8):** on ↵ — viewport goes fully `#1d2021` for
**120ms** (respawn kill), then inner content + inner status bar appear in ONE
frame (no fade, no slide — terminals don't tween). Rail gutter updates (● → ▸)
on the next 1s tick, at most 1000ms later. Builder: this screen is a static
freeze AFTER the transition; document the beat in the caption.

**States:** *Dead viewport* (known seam — inner client detached): render as a
second mini-terminal (152×12) under this screen: viewport area shows tmux's
`Pane is dead (status 0, Thu Jul 23 14:35:12 2026)` line in `#fb4934` on bg0_h,
rail hint line swaps to `↵ re-point viewport` — the rail heals it on next enter.

---

## SCREEN 4 — `empty-cold` · no sessions yet

**Purpose:** first-run. tmux has only the hub session; the tree is empty; the
rail offers creation; the viewport idles with a quiet identity mark.

**Rail (rows 3–12):**

```
   no sessions yet          ← #928374
                            
   n  new session           ← n in #fabd2f bold, rest #ebdbb2
   a  agent session         ← a in #fabd2f bold  (creates gm-agent-NN)
                            
   sessions made anywhere   ← #928374, wrapped 3 lines
   (tmux new, ambient, ssh) 
   appear here live         
```

Hint line: `n new · a agent · ? help · q quit`.

**Viewport:** vertically-centered (rows 18–24), horizontally centered in the
121-col pane, all dim `#504945` EXCEPT the ▸: 

```
      ▸ ghostmux
   the rail is watching
tmux new -s work  →  it appears
```

(`▸` in `#fe8019` at 50% opacity feel — use `#9d5f24`-free approach: just
`#fe8019` but the rest stays bg2-dim. No logo art beyond this — cold states
whisper.) No inner status bar (nothing attached) — viewport row 43 is empty
bg0_h; ONLY the outer status bar (row 44) exists. Divider col 31 still full
height.

**Create flow state (render as second mini-terminal, 152×10):** after pressing
`n` — rail hint line becomes an inline prompt: `new session: myproj▉` (`❯`-less;
label gray, input `#ebdbb2`, block cursor blinking); caption: ↵ creates
`myproj`, points viewport at it, tree gains its first row.

**States:** default (shown), create-prompt (mini-frame shown), *error — tmux
server died:* rail body swaps to `tmux server gone` `#fb4934` + `r retry` gray
(caption note only).

---

## SCREEN 5 — `dense-fleet` · 8+ sessions, hierarchy under pressure

**Purpose:** prove the rail at fleet scale: agent fleet naming (`gm-agent-*`),
name truncation, collapsed sessions, scroll indicator, filter mode — all in 30
cols without breaking the grid.

**Rail tree (rows 3–41, exactly filled; cursor on `gm-agent-03`; viewport lock
on `gm-agent-01`):**

```
 ▾ gm-agent-01 ▸           #8ec07c bold, ▸ #fe8019
     1:claude
     2:test             ~
 ▾ gm-agent-02          ✓
     1:claude           ✓
 ▾ gm-agent-03          ●  CURSOR BAR; ● blinking
     1:claude           ●
     2:server
 ▸ gm-agent-04          ~  collapsed session: aggregate ~ 
 ▸ gm-agent-05          ✓  collapsed, aggregate ✓
 ▾ payments-refactor-sp…   truncated with … at col 26
     1:claude
     2:worktree-mgmt-l…    truncated window name
 ▸ dotfiles ●
 ▸ scratch
   ↓ 2 more…               #928374 scroll indicator (last tree row)
```

(Builder: pad with one more expanded session if rows remain — the frame must
LOOK full, rows 3–41 occupied.)

**Filter mode (this screen's hint line, active):** row 43 =
`/agent▉                       ` — `/` `#fabd2f`, query `#ebdbb2`, cursor
blinking. Caption: filter dims non-matching rows to `#504945` in place (rows
never disappear or reflow — positions are sacred; matching is fuzzy on
session+window names). In the frame, render `payments-refactor-sp…`, `dotfiles`,
`scratch` rows dimmed to `#504945` (marks too) as live filter effect of `agent`.

**Viewport:** gm-agent-01 running a long agent build — tail of a compile+test
loop: `go build ./…` prompt line, `ok  github.com/george/ghostmux  0.41s` in
`#b8bb26`, a `⠼ agent: writing rail_test.go` spinner line `#d3869b`. Inner
status: ` gm-agent-01 ` · `1:claude* 2:test`.

**States:** default+filter (shown). *Overflow scroll:* indicated by `↓ 2 more…`.
*All-quiet fleet variant:* caption note — no marks means every agent is still
working, silence is information.

---

## SCREEN 6 — `help-overlay` · keymap popup

**Purpose:** discoverability without leaving the hub. A `tmux display-popup`
-style centered overlay ON TOP of screen 1's exact frame (screen 1 dimmed
beneath).

**Backdrop:** screen 1's full frame with every cell's fg dimmed one step
(fg1→`#928374`, gray→`#504945`, all marks→`#504945`; status bar dims too).
No blur, no dark scrim — terminals dim by color, not alpha.

**Popup:** centered box, 56 cols × 22 rows (cols 49–104, rows 11–32), bg
`#3c3836`, single-line box border `┌─┐│└┘` in `#665c54`, title embedded in top
border: `┤ ghostmux rail — keys ├` with title text `#8ec07c` bold.

Inside (2-col padding, key col `#fabd2f` bold right-aligned width 9, dot leader
gray, description `#ebdbb2`):

```
      j/k   move cursor
      g/G   first / last row
      ↵     view in pane →
      tab   collapse / expand session
      n     new session
      a     new agent session (gm-agent-NN)
      x     kill selected (y/n confirm)
      /     filter rows
      r     refresh now (auto: 1s)
      d     detach inner client
      ?     close this help
      q     quit rail

   gutter:  ● bell   ✓ done   ~ activity   ▸ viewing
   inner tmux prefix: ctrl+b ctrl+b
```

Last two lines gray `#928374`; gutter glyphs keep their true colors even here
(the one exception to popup monochrome — it's a legend).

**Transition:** popup appears/disappears in ONE frame (instant), backdrop dims
the same frame. No motion.

**States:** open (shown). Closed = screen 1. `x` confirm prompt (caption note):
hint line becomes `kill agent-api? y/n` with `y` `#fb4934`.

---

## 8. Motion language (global, terminal-honest)

Terminals do not tween. ALL motion is frame-based `steps()`; zero easing curves,
zero opacity fades, zero position transitions. The page reads as alive through
exactly four periodic behaviors:

| Behavior | Spec |
|---|---|
| Bell ● blink | `steps(1)` toggle, 800ms on / 400ms off, infinite (`animation: bell 1.2s steps(1) infinite`). All bells blink in phase. |
| Block cursor ▉ blink | 530ms on / 530ms off, `steps(1)`, 1.06s period — classic terminal cadence. |
| Braille spinner | frames `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`, 80ms/frame via `steps(10)` over 800ms, infinite. |
| Rail tick | conceptual (1s state refresh); in mockup, optionally pulse the `~` marks: swap `~`/`∼` glyph every 1s `steps(1)` — subtle, may be omitted. |
| Viewport re-point | 120ms full-`#1d2021` blank, then complete new frame at once. (Documented; screens are post-transition freezes.) |
| Help popup | 0ms — appears/disappears in one frame. |

`prefers-reduced-motion: reduce` → all animations paused at their "on" frame.

---

## 9. Builder notes (HTML artifact)

- One HTML file at the given `out` path; six labeled terminal frames stacked
  with 56px vertical rhythm on a page bg `#141617` (darker than bg0_h so frames
  read as windows); page max-width 1300px centered.
- Captions above each frame: JetBrains Mono 12px, `#928374`, format
  `01 · hub-default — rail + live agent viewport`, with 1–2 sentence
  description under it in `#665c54`-toned text 12px. Mini-state frames (screen 1
  error, screen 3 dead-pane, screen 4 create-prompt) are smaller terminals
  indented under their parent with caption prefix `└ state:`.
- Render each terminal as a CSS grid or `<pre>` of rows; EVERY character must
  land on the grid — verify col 31 divider alignment by counting `ch` widths.
  Use `ch`-based widths so cols are exact (30ch rail, 1ch divider, 121ch
  viewport).
- All colors from §2 only. No border-radius inside the grid, no shadows inside
  the grid, no images, no external fonts — embed JetBrains Mono as a data-URI
  woff2 subset if feasible, else fall back
  `"JetBrains Mono", "Cascadia Mono", "DejaVu Sans Mono", monospace`.
- Theme: page is dark-only by design (a terminal mockup) — still set
  `color-scheme: dark` and keep the page bg fixed regardless of viewer theme.
- `<title>`: `ghostmux rail hub — screens`. Favicon suggestion: 👻.
```
