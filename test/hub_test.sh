#!/usr/bin/env bash
# test/hub_test.sh — end-to-end acceptance for `ghostmux hub` (SPEC.md §5,
# Task 11), driven against a scratch tmux server so it never touches the
# real one. Run from anywhere: `bash test/hub_test.sh`.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

SOCK="gm-test"
TMUX_ARGS=(-L "$SOCK" -f /dev/null)
export GHOSTMUX_TMUX_ARGS="-L $SOCK -f /dev/null"

t() { tmux "${TMUX_ARGS[@]}" "$@"; }

fail() { echo "FAIL: $1"; exit 1; }
pass() { echo "PASS: $1"; }

cleanup() {
	t kill-server >/dev/null 2>&1 || true
	# Belt-and-braces: some sandboxed filesystems leave the socket inode
	# behind even after the server process is gone (kill-server reports "no
	# server running" but the file lingers) — remove it explicitly so the
	# scratch socket never accumulates across runs.
	sock_dir="${TMUX_TMPDIR:-/tmp}/tmux-$(id -u)"
	rm -f "$sock_dir/$SOCK" 2>/dev/null || true
}
trap cleanup EXIT

GM="$REPO_ROOT/ghostmux"

# ---- 1. build ----
go build -o "$GM" ./cmd/ghostmux || fail "go build"
pass "build"

# ---- 2. hub --no-attach: layout, prefix None, mouse on, idempotency ----
"$GM" hub --no-attach || fail "hub --no-attach"

panes_before="$(t list-panes -t hub -F '#{pane_id} #{pane_width} #{pane_current_command}')"
npanes="$(printf '%s\n' "$panes_before" | wc -l)"
[ "$npanes" -eq 2 ] || fail "hub: expected 2 panes, got $npanes"

rail_line="$(printf '%s\n' "$panes_before" | head -1)"
rail_id="$(printf '%s' "$rail_line" | awk '{print $1}')"
rail_width="$(printf '%s' "$rail_line" | awk '{print $2}')"
rail_cmd="$(printf '%s' "$rail_line" | awk '{print $3}')"
[ "$rail_width" -eq 30 ] || fail "hub: rail pane width $rail_width != 30"
[ "$rail_cmd" = "ghostmux" ] || fail "hub: rail pane not running ghostmux (got $rail_cmd)"
pass "hub: exactly 2 panes, rail pane width 30 running ghostmux"

prefix="$(t show-options -t hub prefix)"
printf '%s' "$prefix" | grep -q "None" || fail "hub: prefix not None ($prefix)"
pass "hub: prefix None"

mouse="$(t show-options -t hub mouse)"
printf '%s' "$mouse" | grep -q "on" || fail "hub: mouse not on ($mouse)"
pass "hub: mouse on"

pane_ids_before="$(printf '%s\n' "$panes_before" | awk '{print $1}' | sort | tr '\n' ' ')"
"$GM" hub --no-attach || fail "hub --no-attach (rerun)"
pane_ids_after="$(t list-panes -t hub -F '#{pane_id}' | sort | tr '\n' ' ')"
[ "$pane_ids_before" = "$pane_ids_after" ] || fail "hub: pane ids changed on idempotent rerun"
pass "hub: idempotent rerun leaves pane ids unchanged"

# ---- 3. rail once shows created scratch sessions ----
t new-session -d -s alpha -c /tmp
t new-window -t alpha
t new-session -d -s beta -c /tmp

out="$("$GM" rail once)"
printf '%s\n' "$out" | grep -q "alpha" || fail "rail once: alpha missing"
printf '%s\n' "$out" | grep -q "beta" || fail "rail once: beta missing"

alpha_windows="$("$GM" rail once --marks | awk -F'|' '$1=="alpha" && $2!=""' | wc -l)"
[ "$alpha_windows" -eq 2 ] || fail "rail once: alpha should show 2 window rows, got $alpha_windows"
beta_windows="$("$GM" rail once --marks | awk -F'|' '$1=="beta" && $2!=""' | wc -l)"
[ "$beta_windows" -eq 1 ] || fail "rail once: beta should show 1 window row, got $beta_windows"
pass "rail once: shows created scratch sessions (alpha x2 windows, beta)"

# ---- 4. bell ----
t send-keys -t beta 'printf "\a"' Enter
bell_seen=0
for _ in $(seq 1 20); do
	marks="$("$GM" rail once --marks)"
	if printf '%s\n' "$marks" | awk -F'|' '$1=="beta" && $3 ~ /bell/ {found=1} END{exit !found}'; then
		bell_seen=1
		break
	fi
	sleep 0.1
done
[ "$bell_seen" -eq 1 ] || fail "bell: beta row never showed bell within 2s"
pass "bell: beta row shows bell within 2s of send-keys"

# ---- 5. done ----
t new-session -d -s gamma -c /tmp
gamma_win="$(t list-windows -t gamma -F '#{window_index}' | head -1)"
t send-keys -t "gamma:$gamma_win" 'sleep 2' Enter
sleep 4
marks="$("$GM" rail once --marks)"
printf '%s\n' "$marks" | awk -F'|' -v w="$gamma_win" '$1=="gamma" && $2==w && $3 ~ /done/ {found=1} END{exit !found}' \
	|| fail "done: gamma:$gamma_win row never showed done after 4s"
pass "done: gamma:$gamma_win row shows done after 4s"

done_opt="$(t show-options -w -t "gamma:$gamma_win" -v '@ghostmux_done' 2>/dev/null || true)"
[ "$(printf '%s' "$done_opt" | tr -d '[:space:]')" = "1" ] || fail "done: @ghostmux_done != 1 (got '$done_opt')"
pass "done: @ghostmux_done = 1 on gamma:$gamma_win"

# ---- 6. hooks torn down on rail quit ----
hooks="$(t show-hooks -g 2>/dev/null || true)"
printf '%s\n' "$hooks" | grep -q '\[133\]' || fail "hooks: no [133] entries while rail lives"
pass "hooks: [133] entries present while rail lives"

t send-keys -t "$rail_id" q Enter
sleep 2

if t has-session -t hub 2>/dev/null; then
	fail "hooks: hub session still present after q"
fi
pass "hooks: hub session gone after q (or server exited)"

if t list-sessions >/dev/null 2>&1; then
	hooks_after="$(t show-hooks -g 2>/dev/null || true)"
	if printf '%s\n' "$hooks_after" | grep -q '\[133\]'; then
		fail "hooks: [133] entries remain after rail quit"
	fi
fi
pass "hooks: zero [133] entries after rail quit (or server gone)"

# ---- 7. filter ----
# The hub is gone (step 6 killed the last session's server in this scratch
# environment); rebuild a couple of sessions fresh for the filter check.
t new-session -d -s work -c /tmp
t new-session -d -s wobble -c /tmp
t new-session -d -s other -c /tmp

plain="$("$GM" rail once)"
filtered="$("$GM" rail once --filter w)"
marks="$("$GM" rail once --marks)"

plain_n="$(printf '%s\n' "$plain" | wc -l)"
filtered_n="$(printf '%s\n' "$filtered" | wc -l)"
marks_n="$(printf '%s\n' "$marks" | wc -l)"
[ "$plain_n" -eq "$filtered_n" ] || fail "filter: row count changed ($plain_n vs $filtered_n)"
[ "$plain_n" -eq "$marks_n" ] || fail "filter: marks row count differs from plain ($marks_n vs $plain_n)"

# rail once, rail once --marks and rail once --filter all walk the same row
# slice in the same order, so line i of --marks tells us line i's session —
# no window name in this test contains "w", so a row is expected to be
# dimmed exactly when its session name doesn't contain "w" (case-insensitive).
mapfile -t plain_lines <<<"$plain"
mapfile -t filtered_lines <<<"$filtered"
mapfile -t marks_lines <<<"$marks"
n="${#plain_lines[@]}"
for i in $(seq 0 $((n - 1))); do
	pl="${plain_lines[$i]}"
	fl="${filtered_lines[$i]}"
	sess="$(printf '%s' "${marks_lines[$i]}" | cut -d'|' -f1)"
	body="${fl:1}"
	[ "$body" = "$pl" ] || fail "filter: row $i body changed order/content ($fl vs $pl)"
	prefix_char="${fl:0:1}"
	lower_sess="$(printf '%s' "$sess" | tr '[:upper:]' '[:lower:]')"
	if printf '%s' "$lower_sess" | grep -q "w"; then
		[ "$prefix_char" = " " ] || fail "filter: matching row $i (sess=$sess) not left un-dimmed ($fl)"
	else
		[ "$prefix_char" = "·" ] || fail "filter: non-matching row $i (sess=$sess) not dimmed ($fl)"
	fi
done
pass "filter: dimmed rows prefixed with ·, order/positions unchanged"

echo
echo "ALL CHECKS PASSED"
