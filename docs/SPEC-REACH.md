# SPEC — Reach rows (ssh ghosts)

Status: **v1 PROTOTYPE**, shipped 2026-07-28. This file records the contract
and the intended v2 so the prototype cannot silently become the product.

## The problem

Driving a remote fleet today means tmux-in-tmux: ssh into the box, wrap the
panel in an outer tmux for keepalive, and pay the nested-prefix tax. But the
viewport is just a pty running a child — ssh is as good a child as
`tmux attach`. A declared remote workspace should be one `↵` away, with no
outer tmux and no nesting.

## Why this survives the tmux-only cut

The multi-backend cut's recorded escape clause: another backend is in scope
only if it offers the same provable evidence. A remote tmux is not another
backend — it is the same engine reached over a different transport, offering
identical evidence (v2). ghostmux's claim grows from "the tmux on this box"
to "every tmux you can reach."

## v1 laws (binding for the prototype)

1. **A reach row proves nothing.** It renders `↗`, its host in the dim
   command slot, and NO marks — it never joins the attention census, the
   Return Queue, or organization. The rail has no evidence about the remote
   side and refuses to pretend otherwise.
2. **`↵` is the entire verb.** It runs
   `ssh -t <host> -- tmux new-session -A -s <session>` as the viewport
   child. No probing, no polling, no background connections.
3. **A finished ssh idles.** Detach and dropped link are indistinguishable
   locally; blind reconnect could loop on a dead link or an auth prompt, so
   heal idles and `↵` reconnects deliberately.
4. **Declaring is CLI-only for now.**
   `ghostmux reach add <name> <host> [session]` / `rm <name>` / `ls`,
   persisted in the state file's `reach` list. `x` on the row points at the
   CLI instead of destroying.

## v2 (not built — the actual moat)

A persistent ControlMaster link per declared host running the same 11-field
query on a slower tick: identical parser, identical marks, the existing
stale/unvalidated machinery for dropped links. Then the Return Queue spans
machines. Gate on v1 feel: if reach rows are used weekly, v2 is worth the
ssh lifecycle plumbing; if not, they stay a launcher.

## Key code

- `internal/state/state.go` — `ReachTarget`, `Document.Reach`
- `internal/wiring/reach.go` — the CLI
- `internal/rail/model.go` — `reachRows`, activateRow routing
- `internal/app/viewport.go` — `PointRemote`, remote heal
- `test/panel_test.sh` — end-to-end proof with an ssh PATH shim
