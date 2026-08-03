# ghostmux

ghostmux is the tmux fleet navigator: a standalone panel that shows every tmux session — running or not — in one always-visible rail beside an embedded terminal viewport.

It exists for terminals you stop watching. Agents, builds, and long jobs run in tmux sessions; the rail shows one line of ambient attention for all of them while you work in the viewport, and one key jumps you to the oldest thing that finished while you weren't looking. It is all inside your terminal — no desktop app, no browser, and it works over ssh.

tmux keeps everything that makes it worth using: persistence, session truth, its own keymap. ghostmux owns only the frame, and adds the three things tmux itself doesn't have:

- **The ambient fleet.** The rail stays up beside your work. Bells, finished commands, and activity in every other session are visible without asking.
- **Dead slots.** A grouped session that stops becomes a ghost row (`○`) — a declared workspace with a remembered name and directory. `Enter` brings it back. `tmux choose-tree` structurally cannot list what isn't running.
- **The Return Queue.** `]` jumps the viewport to the oldest window that rang (`●`) or finished (`✓`) unseen, ordered by tmux's own activity timestamps. Viewing drains it; repeated presses walk the queue empty.
- **Unread terminals.** Rows bank the lines a window produced while unseen (`api ✓ +38`), and `[` shows exactly that output in a pager. The queue says which window wants you; the count says how much happened there.
- **Pulse.** Agent rows draw a sparkline of observed output cadence (`claude ▂▅█▃▁`) while alive and their quiet age when not — the waveform instead of a guessed "working/idle" label.

## Requirements

- An interactive terminal
- `tmux` on `PATH`
- Go 1.26.4 to build or install from source

## Install

Homebrew (macOS or Linux):

```bash
brew install 1broseidon/tap/ghostmux
```

Or from a source checkout:

```bash
go install ./cmd/ghostmux
```

## Quick start

```bash
ghostmux doctor
ghostmux
```

The rail starts with keyboard focus. If it is empty, press `n` to create a session. Select a row and press `Enter` to show it. Press `Right` to send input to the viewport; `l` previews a leaf first and focuses it when it is already viewed. The default `Ctrl+Alt+\` binding switches focus between the rail and viewport. With the rail focused, `q` exits ghostmux without killing any session.

## Commands

| Command | Purpose |
| --- | --- |
| `ghostmux` | Open the interactive panel. |
| `ghostmux doctor` | Report the detected tmux, visible sessions, and stale ghostmux tmux hooks. |
| `ghostmux ls` | List tmux sessions. |
| `ghostmux up <name> [dir]` | Create or attach to a named tmux session. The directory defaults to your home directory; inside tmux, this switches the current client instead of nesting. |
| `ghostmux rail once [--filter q] [--marks]` | Print one non-interactive rail frame. `--filter` marks non-matching rows; `--marks` emits `SESSION|WINDOW|flags` rows for debugging or scripts. |

Use `ghostmux --help` for the top-level command summary.

## Panel keys

These keys apply while the rail has focus. Press `?` in the panel for the complete current keymap.

| Key | Action |
| --- | --- |
| `j` / `k`, `Down` / `Up` | Move through the rail. |
| `Enter` | View a live session, fold a group, or start a ghost. |
| `h` / `l` | Collapse a structural row or move to its parent; expand, preview a live leaf, or focus an already-viewed one. |
| `Right` | Focus the viewport unconditionally. |
| `` ` `` | Toggle back to the previous session and exact window. |
| `]` | Return Queue: view the oldest unseen `●`/`✓` window. |
| `[` | Peek the selected row's unseen output in a pager (`j/k` scroll, any other key closes). |
| `v` | Wall a group: tile its live members into one tmux window. Pressed again, tears it down. |
| `Ctrl+Alt+\` | Switch focus between rail and viewport. |
| `n` | Create a tmux session. |
| `a`, `m`, `J` / `K`, `u` | Create a group, preview an organization move, move immediately, or undo the last organization change. |
| `S` | Start every dead member of a group at once. |
| `x` | Confirm a context-specific kill, ungroup, or forget action. |
| `/`, `d` | Filter rows, or detach the viewport client. |
| `?`, `,`, `q` | Open help, open settings, or quit the panel. |

The attention gutter uses `●` for bell, `✓` for a foreground command returning to a shell while unwatched, `~` for activity, `▸` for the viewport target, `○` for a declared session that is not running, and `?` for rows whose state could not be validated this tick. A `◆` after a session name means an outside client is attached to it, and `+N` beside the marks is the number of lines that window produced while unseen. The bottom bar's attention cluster is the Return Queue's depth; with the viewport focused, the bar also names the exact session the viewport holds.

## Ghosts and the Return Queue

A grouped tmux session that stops becomes a ghost row. Starting it creates a new tmux session with the recorded name and working directory; ghostmux does not restore processes, windows, layout, or commands — the name and the directory are the entire promise, and the settings screen chooses whether the remembered directory is the launch directory or the last active pane's.

The Return Queue is orderable because tmux proves `#{window_activity}` per window: `]` selects the unseen `●`/`✓` window with the oldest timestamp — agent windows first (detected ambiently from the foreground command), plain jobs after — points the viewport at it, and lets the existing clearing paths (native bell clear, `@ghostmux_done` clear-on-view) remove it from the queue. There is no queue state to corrupt — the queue is re-derived from evidence every tick, and a fold never hides a queue entry.

Agent rows state their evidence instead of a verb: while a window is producing output they draw a sparkline of its observed cadence (`claude ▂▅█▃▁` — eight 8-second buckets of tmux activity advances), and when every bucket is silent they show the quiet age (`claude 26m`). ghostmux will not claim an agent is "working" or "stuck" from evidence that cannot prove either — it draws the waveform and lets you read it. `ghostmux doctor` reports which agents are on `PATH` and whether Claude Code's bell hook is wired.

Unread counts are line arithmetic, not content inspection: a pane's write position is `#{history_size} + #{cursor_y}`, banked only when the window's activity timestamp proves output happened (a resize reflow moves the totals without a byte being emitted and is absorbed). Viewing the window drains its bank; `[` captures the unseen tail lazily, capped at 200 lines with the cap named in the pager title. Full-screen programs are marked `TUI` in the peek because their line history under-describes them.

`v` on a group header walls it: up to six live, non-ghost members tile into one real tmux window of split panes — ada | beastie | ifrit, side by side, live and interactive — with tmux itself owning navigation, mouse focus, and `prefix+z` zoom to one member and back. Each pane attaches a per-member shadow, never the member directly, so the wall never fights your other clients for window focus. More than six members walls the first six and says so (`wall: first 6 of N`); no live members flashes `nothing to wall`; `v` on anything but a group flashes `v views a group` and does nothing. While a group is walled, shadow display cannot clear its members' own attention flags — but the operator is looking at all of them, so the panel acknowledges every walled member's active window itself: their marks and unread counts drain exactly as a normal view's do, for as long as the wall is up. Pressed again, `v` tears the wall down and every member resizes back to whatever else is attached to it — that squeeze is tmux physics, not a bug. A crashed panel can leave an owned `gm-wall-*`/`gm-view-*` residue behind; ghostmux never sweeps it by name on the next launch, only by its exact ownership tag when a panel retires it.

## Making agents ring the queue

The queue's two entry marks map onto agent life without inventing anything:

- `✓` needs no setup. It fires when a window's foreground command exits back
  to a shell while the window is unwatched — any agent or job that runs to
  completion enters the queue by itself.
- `●` is tmux's own bell flag, set whenever a process writes the BEL byte
  (`\a`) to an unwatched pane. This is the "needs you while still running"
  signal — an agent waiting at a permission prompt has not exited, so `✓`
  cannot catch it, but a bell can.

Any tool can ring it (`long-job; printf '\a'`). For Claude Code, add hooks
that write BEL to the pane's tty — hook stdout is captured by Claude Code, so
the write must target the tty directly:

```json
{
  "hooks": {
    "Notification": [{"hooks": [{"type": "command",
      "command": "printf '\\a' > \"$(tmux display-message -p -t \"$TMUX_PANE\" '#{pane_tty}')\""}]}],
    "Stop": [{"hooks": [{"type": "command",
      "command": "printf '\\a' > \"$(tmux display-message -p -t \"$TMUX_PANE\" '#{pane_tty}')\""}]}]
  }
}
```

Put this in `~/.claude/settings.json` to cover every session, or in a
project's `.claude/settings.json` for just that workspace. `Notification`
rings when the agent needs input; `Stop` rings when it finishes a turn.
Viewing the window clears the mark, so drained entries do not come back.

## State and settings

Groups and user settings share one JSON state file:

- `$XDG_STATE_HOME/ghostmux/groups.json` when `XDG_STATE_HOME` is set
- `$HOME/.local/state/ghostmux/groups.json` otherwise

Organization changes — group creation or deletion, moves, and folds — can be undone once with `u`; a new organization change replaces that undo, and session destruction clears it.

The primary file stores group order and membership, fold state, observed working directories for grouped sessions, and saved settings. New writes include schema `"version": 1`; existing unversioned files are migrated on their next successful save. Settings cover the rail/viewport toggle, rail width, additional agent command names, the ghost directory mode, and the new-session directory mode. A comma-separated `GHOSTMUX_TOGGLE` value overrides the saved toggle binding for that run. Member keys written by the retired multi-backend prototype are preserved on disk but never rendered, and top-level keys this build does not recognize — typically written by a newer or experimental build — are carried through saves verbatim rather than treated as corruption: switching binaries can never destroy another version's state. Only malformed JSON or a known key with the wrong shape puts state editing into read-only mode.

Writes use a persistent `groups.json.lock` process lock, atomic replacement, and a retained `groups.json.bak` containing the previous valid primary (or the initial primary on first creation). Backups are not restored automatically. Invalid, unreadable, or unsupported primary files remain unchanged and put state editing into a visible read-only mode. If the primary is missing while the backup path still exists, ghostmux blocks edits to protect that copy; restore the backup to the primary path, or remove it deliberately before saving again.

## tmux integration and limitations

Viewing a session that is already attached elsewhere uses a temporary `gm-view-*` grouped session so each client gets an independent view. Because no client displays the origin session's own window links in that mode, tmux never clears the origin's bell flag — ghostmux tracks the acknowledgement itself: a bell you have viewed stays cleared until that window produces new output.

Sessions ghostmux owns wear ghostmux's own chrome; sessions it does not own stay byte-for-byte native. Every grouped shadow (`gm-view-*`) runs with `status off`, so its own status bar — which used to duplicate identity the rail already shows, and could truncate a session name into a misleading one under tmux's default `status-left-length 10` (`[gm-agent-00]` glued to the window list) — never appears at all; the frame's bottom bar and, on a wall, the pane border titles are the authoritative identity instead. A wall composite (`gm-wall-*`) additionally engraves each pane's border with its member's own session name — never the shadow's internal `gm-view-*` name — in the panel's theme colors. Viewing a session ghostmux does *not* own (attaching directly, or watching from outside) shows that session's own untouched status bar: the asymmetry is the feature, not an inconsistency. Every one of these options is written to an exact, ownership-tagged session ID, never globally and never to a session ghostmux does not own; styling is cosmetic and its failure is silently absorbed rather than blocking a view or wall from coming up. Owned views retain one deliberate crash-only residual: if the ghostmux process dies after writing the exact `@ghostmux_view_owner` tag but before starting the attach client, that detached view can remain behind. Clean lifecycle paths retain and retry exact session-ID capabilities, but startup never sweeps `gm-view-*` names or prefixes because that could mutate an unowned session.

Queries are outage-safe. If tmux becomes unavailable, the panel keeps the last validated in-process snapshot, marks its rows with `?`, suppresses stale attention and ghost actions, and retries automatically. A declaration becomes a `○` ghost only after a successful query proves it absent. Attach, summon, kill, and forget actions stay disabled for uncertain rows; destructive confirmations are revalidated immediately before execution. `ghostmux rail once` has no cache: it prints rows from a successful query and exits with an error otherwise.

ghostmux leaves the global `monitor-activity` and `visual-activity` scalar options untouched. Each panel additively leases its own entries in tmux's global session- and window-hook arrays, using a private versioned refresh channel; it never selects or overwrites a fixed array index. Clean exit removes only entries whose exact canonical commands still match that panel's lease. Crash leftovers remain harmless and are reported by `ghostmux doctor`, including the exact `hook[index]` names an operator may inspect or unset; doctor also warns when an active lease is incomplete. PID status in that report is informational only because PIDs can be reused.

Attention marks do not require ghostmux to enable native tmux monitoring. The fleet query tracks stable window IDs and activity timestamps per panel, while still honoring `window_activity_flag` when users enabled monitoring themselves. `@ghostmux_done` remains a per-window user option written by ghostmux.

## Theme

ghostmux ships its engraved gruvbox look by default. `GHOSTMUX_THEME=ansi` renders every color as an ANSI-16 index instead, so the whole panel takes on your terminal's own palette — the panel in your rice, on every box you attach from.

## Development

Build and run the unit tests without creating a repository-root binary:

```bash
go build ./...
go test ./...
```

The integration scripts build temporary binaries and use scratch tmux servers and isolated state directories:

```bash
bash test/gate1_preflight.sh
bash test/panel_test.sh
```

They require tmux.

## Where to go next

- New users: follow the [quick start](#quick-start).
- Regular users: review the [commands](#commands), then use the in-panel `?` overlay and `,` settings screen.
- Contributors: use the [development checks](#development) before submitting changes.
