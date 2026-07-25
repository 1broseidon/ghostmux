# After the reboot: the return path

Status: proposal — two designs, mutually exclusive at the philosophy level,
sharing most of their plumbing. Written 2026-07-24, on top of `panel-flip`
(`0ed2234`). Nothing here is built.

## The problem

Today the panel survives a reboot better than it admits: groups, order, and
fold state come back from `groups.json`, and a session recreated by name
rejoins its group. But the *sessions* are gone, the rail shows bare folders
with no hint of what used to fill them, and the file quietly accumulates
member names that will never return. The return path exists — it just isn't
designed.

The real question underneath "what happens on reboot" is: **which layer owns
continuity?**

- **Apps** increasingly own their own: `claude --continue` resumes the
  conversation, nvim has sessions, shells have history. A restored *process*
  is worth less than the app's own resume.
- **Muxes** own the middle: zellij ships session serialization — dead
  sessions stay listed as `EXITED` and `zellij attach` resurrects layout and
  re-runs pane commands. tmux ships nothing, and the ecosystem filled the gap
  twice, in two schools: **snapshot** (tmux-resurrect/continuum: record what
  ran, replay it) and **declaration** (tmuxinator/tmuxp/zellij layouts: say
  what should run, start it).
- **The panel** owns the fleet view — and, today, one file of user intent.

Both designs agree the panel must never own *content* (scrollback, program
state — the purist test forbids it and the evidence law can't render it).
They differ on whether the panel records **declarations** (Design A) or
**snapshots** (Design B).

## Facts on the ground (verify at build time)

- `zellij list-sessions --no-formatting` lists dead-but-serialized sessions
  with `(EXITED - attach to resurrect)` — our parser currently **skips**
  them (`backends.go`). Verify they persist across a reboot (serialization
  cache lives under `~/.cache/zellij`), and what `attach` actually re-runs.
- `#{session_path}` reflects a tmux session's directory; verify it tracks
  reality well enough to record.
- `new-session -d -s X` on an existing name errors — the summon path wants
  attach-if-exists semantics instead (idempotent).
- tmux-resurrect stores its state under `~/.tmux/resurrect/` (Design B only:
  detect + delegate, never parse-and-own).

---

## Design A — Ghosts

> **The name is the workspace.** Sessions are instances; the rail is the
> declaration. Nothing is "restored" because nothing meaningful was lost —
> the meaningful part is one keystroke from existing again.

The insight A builds on: grouping already *is* pinning. The moment you put
`api` in `work`, you declared "this name belongs in my fleet." A group
member whose session is dead doesn't vanish — it renders as a **ghost**: the
declared slot, dim, waiting. Ungrouped sessions stay truly ephemeral cattle.
The rule is teachable in one line: **group it and it outlives the machine.**

(And ghostmux finally earns its name after the ghostty divorce.)

### What persists

`groups.json`, exactly as today, plus one sibling map — members stay
strings, old files load unchanged:

```json
{
  "groups":    [{ "name": "work", "members": ["tmux:api", "tmux:web"] }],
  "collapsed": ["grp:agents"],
  "dirs":      { "tmux:api": "/home/george/Projects/api" }
}
```

`dirs` is captured from evidence, not asked for: on each refresh, a grouped
member's observed `session_path` is recorded on change. zellij members get
no dir (the CLI can't prove one) — consistent with v0.2's honest
degradation.

### The rail after a reboot

```
 ▾ work
     api  …ects/api         ○
     web  …ects/web         ○
 ▸ agents                 ○ 2
```

- Ghost rows: dim label, dir tail dim in the cmd-suffix slot, `○` in the
  gutter. `○` is a fact — "declared here, not running" — observed absence,
  not inference. No other marks, ever (nothing is running to earn one).
- A group with zero live members dims its header; folded, it shows `○ N`.
- Hint line with the cursor on a ghost: `↵ start in ~/Projects/api` — you
  see where it will be created *before* you commit.
- The "no sessions yet" empty state only appears when there are no ghosts
  either — the post-reboot blank-rail wart dissolves by design.

### Keys

| key | on a ghost | note |
|---|---|---|
| `↵` | start the session (in its recorded dir) and view it | idempotent: if it sprang to life between render and press, just attach |
| `S` | on a group row: start every dead member | the whole workspace in one keystroke; no confirm — creating is safe |
| `x` | **forget** the ghost (remove the declaration) | third verb, same honesty rule: kill / ungroup / forget — the confirm prompt says which |

`x`-on-ghost turns the stale-member wart into a managed feature: the file
accumulates only what you can now see and prune.

### Backends stay honestly different

- tmux ghost → summoned **fresh**: `new-session -d -c <dir>`, then the
  viewport points at it. We never imply it's the old one — it's a new
  session with the declared name and dir, which is exactly the claim.
- zellij ghost → if zellij still lists it as `EXITED`, `↵` is a **real
  resurrection** — zellij's own feature, we relay its own label. If deleted
  entirely, `attach --create-background` starts fresh. Stop skipping EXITED
  rows in `auxSessions()`; render them as ghosts.

### The feel (the ten-second script)

Reboot. `ghostmux`. The fleet is there, dim — names, dirs, order, folds, all
of it. Cursor is on `work`. `S`. Six sessions pop live inside a second.
`↵` on `gm-agent-00`, type `claude -c` — the conversation resumes, because
*the app* owns that layer and does it better than any pty replay could.
Total: four keystrokes and one shell command. You never "needed" a
persistent workspace; you needed the return path to be shorter than your
memory of what you were doing.

### A+ — one optional dial, later

A declared start command per member:

```json
"start": { "tmux:api": "claude -c" }
```

Still a declaration — a Procfile line, not a snapshot. `S` on `work` then
means: six sessions exist *and* the agent is already resuming. This is the
single most tempting Design B feature, graftable onto A without importing
B's risks. Not in scope for the first cut.

### Evidence & purist audit

- Ghost = recorded declaration + observed absence. Both facts. ✓
- Summon uses only `new-session` / `attach` — nothing the muxes couldn't
  do; what they can't do is *show you the fleet and make return one key*. ✓

### Cost

S/M: render ghosts (grouped members minus live sessions), `dirs` capture,
summon + `S` + forget, un-skip zellij EXITED, tests. One state-file field.

---

## Design B — The Standing Fleet

> **The fleet is a service.** Reboot is downtime; restore is recovery. The
> panel's job is to bring the whole thing back — structure, windows,
> commands — with one key.

### What persists

`groups.json` as today, plus an auto-captured manifest, `fleet.json`,
written debounced on change during the refresh the rail already does:

```json
{
  "sessions": [
    { "key": "tmux:api", "dir": "~/Projects/api", "windows": [
      { "name": "vim",  "dir": "~/Projects/api", "cmd": "nvim" },
      { "name": "zsh",  "dir": "~/Projects/api" }
    ]}
  ],
  "recipes": { "claude": "claude --continue", "nvim": "nvim" },
  "updated": "2026-07-24T16:58:00Z"
}
```

Capture is observation (evidence-clean). The dangerous half is *replay*.

### Restore

- After a reboot the rail shows every manifest session as a dead row; the
  bar offers `R restore 6` (y/n confirm with the count). `↵` restores one.
- Restore recreates sessions and windows with their cwds. Commands are
  re-run **only** through the opt-in `recipes` map — never "whatever was
  observed." Re-running arbitrary recorded commands is action taken on
  stale evidence; a recipe is the user saying "this command is safe to
  start and this is how." (Note what just happened: pushed to honesty, the
  snapshot becomes a declaration.)
- **tmux-resurrect detected?** Offer *its* restore and stand down. Integrate,
  never reimplement — the purist test applies to the ecosystem too.
- zellij: native resurrection, zero ghostmux storage, same as Design A.

### The ↺ mark

Restored ≠ resumed. Every session ghostmux restarted carries `↺` in the
gutter until first viewed: "this process was started by ghostmux at
restore-time, not a survivor." Without this mark, B builds a workspace that
lies — the exact failure the spinner deletion was about.

### Risks (why B costs what it costs)

- A second state file with drift, migration, and corruption surface.
- Restart side effects: a recipe that bills, writes, or deploys. Allowlist
  + confirm mitigates; can't eliminate.
- Double-restore races with tmux-continuum users.
- Purist strain: the tmux half shadows tmux-resurrect (snapshot school) and
  tmuxinator (declaration school) simultaneously.

### Cost

L: manifest capture + debounce, restore engine, recipes, resurrect
detection, `↺` plumbing, and a much larger failure-mode matrix.

---

## Choosing

The ecosystem already ran this experiment. The snapshot school
(resurrect/continuum) restores more but lies more — half-dead layouts,
re-run commands that shouldn't have been. The declaration school
(tmuxinator/tmuxp/zellij layouts) restores less and is trusted more,
because it only ever does what you wrote down. Design A is the declaration
school **with zero config files to write** — the rail is the declaration,
edited by keystrokes you already know (`a`, `J/K`, `x`).

And note the convergence: every time B is pushed toward honesty — recipes
instead of raw replay, confirms instead of auto-restore — it turns into A+
with extra bookkeeping. The only thing B ultimately adds that A+ can't
absorb is window-level layout restore, which is tmuxinator/zellij-layout
territory: integrate if ever, don't own.

**Recommendation: build A** (which already contains B's one free lunch, the
zellij EXITED surfacing), keep A+ in the drawer for when a summoned fleet
feels one step short of magic, and give snapshot-school users a
tmux-resurrect hook instead of a competitor. B stays on the shelf as an
additive layer — A's ghost is B's row, so nothing is thrown away if we're
wrong.
