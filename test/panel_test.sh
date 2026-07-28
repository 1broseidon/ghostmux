#!/usr/bin/env bash
# Acceptance for `ghostmux`: run the panel inside a
# scratch tmux pane and assert on capture-pane. Everything runs on a throwaway
# server (-L gm-solo -f /dev/null), so the user's real sessions are untouched.
set -u

REPO=$(cd "$(dirname "$0")/.." && pwd)
SOCK=gm-solo
TA="-L $SOCK -f /dev/null"
BIN=$(mktemp -d)/ghostmux
FAIL=0

# Isolate the panel's state file. Without this the harness reads — and could
# write — the user's real groups, so their own grouping would change what the
# test sees (and vice versa).
export XDG_STATE_HOME="$(mktemp -d)"

pass() { echo "PASS: $1"; }
fail() { echo "FAIL: $1"; FAIL=1; }

cleanup() { tmux $TA kill-server 2>/dev/null; }
trap cleanup EXIT

cd "$REPO" || exit 1
go build -o "$BIN" ./cmd/ghostmux || { echo "FAIL: build"; exit 1; }
pass "build"

cleanup
# alpha is the session under test; zdriver hosts the panel itself (named to sort
# last, so the rail's cursor starts on alpha).
tmux $TA new-session -d -s alpha -x 80 -y 20
tmux $TA new-session -d -s zdriver -x 120 -y 32
# User-owned tmux state is an acceptance fixture, not scratch space for the
# panel. These values are deliberately the opposite of ghostmux's old writes,
# and [133] is deliberately occupied.
tmux $TA set-option -g monitor-activity off
tmux $TA set-option -g visual-activity on
tmux $TA set-hook -g 'alert-bell[133]' 'display-message "user-133"'
tmux $TA send-keys -t zdriver "GHOSTMUX_TMUX_ARGS='$TA' $BIN" Enter
sleep 2.5

cap() { tmux $TA capture-pane -p -t zdriver; }
send() { tmux $TA send-keys -t zdriver "$@"; sleep "${SLEEP:-1.5}"; }
# Membership and current directory evidence use the same qualified key. Stop
# before dirs so an undo that correctly preserves evidence is not mistaken for
# a declaration that survived.
has_member() { sed '/^[[:space:]]*"dirs"/,$d' "$1" | grep -Fq "\"$2\""; }

# --- additive tmux lease and untouched user globals ---
MONITOR_DURING=$(tmux $TA show-options -gv monitor-activity 2>/dev/null)
VISUAL_DURING=$(tmux $TA show-options -gv visual-activity 2>/dev/null)
if [ "$MONITOR_DURING" = "off" ] && [ "$VISUAL_DURING" = "on" ]; then
  pass "tmux lease: monitor/visual globals untouched while running"
else fail "tmux lease: globals changed while running ($MONITOR_DURING/$VISUAL_DURING)"; fi
# tmux 3.4 splits global hooks across session (-g) and window (-gw)
# scopes; window-renamed exists only in the latter listing.
HOOKS_DURING=$({ tmux $TA show-hooks -g; tmux $TA show-hooks -gw; } 2>/dev/null)
if echo "$HOOKS_DURING" | grep -q 'alert-bell\[133\].*user-133'; then
  pass "tmux lease: occupied user hook [133] untouched while running"
else fail "tmux lease: user hook [133] changed while running"; echo "$HOOKS_DURING" | grep alert-bell; fi
LEASE_CHANNELS=$(echo "$HOOKS_DURING" | grep -oE 'ghostmux-refresh-v1-[0-9]+-[0-9a-f]{32}' | sort -u)
LEASE_COUNT=$(echo "$HOOKS_DURING" | grep -c 'ghostmux-refresh-v1-' || true)
if [ -n "$LEASE_CHANNELS" ] && [ "$(echo "$LEASE_CHANNELS" | wc -l)" -eq 1 ] && [ "$LEASE_COUNT" -eq 8 ]; then
  pass "tmux lease: one complete tokenized eight-hook panel lease exists"
else fail "tmux lease: expected one complete eight-hook lease (entries=$LEASE_COUNT channels=$LEASE_CHANNELS)"; fi

# --- frame chrome ---
if cap | grep -q "gmx"; then pass "bar: gmx wordmark rendered"
else fail "bar: no gmx wordmark"; cap | tail -3; fi

if cap | grep -q "alpha"; then pass "rail: scratch session listed"
else fail "rail: alpha row missing"; cap | head -10; fi

if cap | grep -q "move"; then pass "bar: keymap rendered in the bottom bar"
else fail "bar: no keymap"; cap | tail -3; fi
if cap | grep -q "view"; then pass "bar: ↵ view hint present"
else fail "bar: no view hint"; cap | tail -3; fi

if cap | grep -q "│"; then pass "layout: rail/viewport divider drawn"
else fail "layout: no divider column"; cap | head -6; fi

if cap | grep -q "the rail is watching"; then pass "viewport: idle placeholder before any selection"
else fail "viewport: idle placeholder missing"; cap | head -12; fi

# No rendered line may exceed the terminal width — an overflow here is what a
# zig-zagging divider looks like in real use. wc -L measures display columns
# (the rail is full of multibyte glyphs; byte length would lie).
MAXW=$(cap | wc -L)
if [ "$MAXW" -le 120 ]; then pass "layout: no line exceeds the terminal width (max $MAXW)"
else fail "layout: a line overflowed 120 cols (max $MAXW)"; fi

# --- selection: enter attaches the embedded client ---
send "" Enter
if cap | grep -q "\[alpha\]"; then pass "viewport: ↵ attached a tmux client (inner status bar)"
else fail "viewport: no inner client after ↵"; cap | head -14; fi
if cap | grep -q "the rail is watching"; then
  fail "viewport: idle placeholder still showing after ↵"
else pass "viewport: placeholder replaced by the live client"; fi

# --- toggle: keys reach the child only after ctrl+\ ---
SLEEP=0.8 send "printf 'SOLOKEYS\\n'" Enter
if cap | grep -q "SOLOKEYS"; then
  fail "focus: keys reached the child while the rail had focus"
else pass "focus: rail-focused keys do not leak to the child"; fi

# The default toggle is the single chord ctrl+alt+\ (tmux spells it C-M-\).
tmux $TA send-keys -t zdriver 'C-M-\'; sleep 0.8
send "printf 'SOLOKEYS-%s\\n' ok" Enter
if cap | grep -q "SOLOKEYS-ok"; then pass "focus: ctrl+alt+\\ hands the keyboard to the child"
else fail "focus: child never saw keys after ctrl+alt+\\"; cap | head -14; fi

# back to the rail, and prove `q` is a rail key again (not sent to the child)
tmux $TA send-keys -t zdriver 'C-M-\'; sleep 0.8

# the help overlay must name the key actually bound, or it is worse than
# nothing — and it must name it WHOLE. In the 30-column rail this row read
# "ctrl+\  toggle rail ⇄ vi…", which is exactly the row a user whose desktop
# grabbed the chord needs intact.
send "?"
if cap | grep -q "alt+ctrl"; then pass "help: ? reports the real toggle key"
else fail "help: ? does not show the bound toggle"; cap | head -18; fi
if cap | grep -q "ghostmux · keys"; then pass "help: ? draws the overlay box with its title"
else fail "help: no overlay title"; cap | head -18; fi
if cap | grep -q "kill / ungroup / forget"; then
  pass "help: the longest keymap row renders un-truncated"
else fail "help: keymap row truncated in the overlay"; cap | head -20; fi
if cap | grep -q "oldest unseen"; then pass "help: the Return Queue key is documented"
else fail "help: ] row missing from the overlay"; cap | head -20; fi
# any key closes it — including a key the rail would otherwise act on
send "j"
if cap | grep -q "ghostmux · keys"; then fail "help: overlay survived a keypress"; cap | head -8
else pass "help: any key closes the overlay"; fi
if cap | grep -q "alpha"; then pass "help: rail rows are back after the overlay closed"
else fail "help: rail did not come back"; cap | head -10; fi

# --- settings: a mode, because sections/fields honor the panes' contract ---
send ","
if cap | grep -q "Fleet"; then pass "settings: , opened the sections list"
else fail "settings: , did not open settings"; cap | head -12; fi
if cap | grep -q "System"; then pass "settings: every section listed"
else fail "settings: sections missing"; cap | head -12; fi
# the fleet stays live underneath: a session made now must be there on the way out
tmux $TA new-session -d -s settled -x 80 -y 20
sleep 1.5
send "" Escape
if cap | grep -q "alpha"; then pass "settings: esc restored the session rows"
else fail "settings: rail did not come back after esc"; cap | head -12; fi
if cap | grep -q "settled"; then pass "settings: the fleet stayed live while settings was open"
else fail "settings: session created during settings is missing"; cap | head -12; fi
tmux $TA kill-session -t =settled 2>/dev/null

# --- detach: d idles the viewport without touching the session ---
send "d"
if cap | grep -q "the rail is watching"; then pass "detach: d returned the viewport to idle"
else fail "detach: d did not idle the viewport"; cap | head -12; fi
if tmux $TA has-session -t =alpha 2>/dev/null; then pass "detach: alpha survived (frame only, never the session)"
else fail "detach: alpha was destroyed"; fi

# --- owned grouped views: unique, hidden, and exact-cleanup only ---
# Prefix-only legacy sessions are user sessions unless the exact owner marker
# binds their current name. Keep both an untagged and malformed one alive
# through every automatic path below.
tmux $TA new-session -d -s gm-view-legacy -x 80 -y 20
tmux $TA new-session -d -s gm-view-malformed -x 80 -y 20
tmux $TA set-option -t gm-view-malformed @ghostmux_view_owner v1:someone-else
sleep 1.5
if cap | grep -q "gm-view-legacy" && cap | grep -q "gm-view-malformed"; then
  pass "ownership: untagged/malformed gm-view sessions remain visible"
else fail "ownership: legacy prefix sessions were hidden"; cap | head -14; fi

# An outside client makes alpha require a grouped viewport. It runs in a
# throwaway tmux session on the same scratch server.
tmux $TA new-session -d -s watcher -x 80 -y 20
tmux $TA send-keys -t watcher "env -u TMUX -u TMUX_PANE tmux -L $SOCK attach-session -t alpha" Enter
sleep 1.5
send "" Enter
VIEW1=$(tmux $TA list-sessions -F '#{session_name} #{@ghostmux_view_owner}' 2>/dev/null |
  awk '$1 ~ /^gm-view-/ && $2 == "v1:" $1 {print $1}' | head -1)
if [ -n "$VIEW1" ]; then pass "ownership: grouped attach created an exactly tagged shadow"
else fail "ownership: no exactly tagged grouped shadow"; tmux $TA list-sessions -F '#{session_name} #{@ghostmux_view_owner}'; fi
if [ -n "$VIEW1" ] && ! cap | grep -q "$VIEW1"; then pass "ownership: valid owned shadow hidden from the rail"
else fail "ownership: owned shadow visible in the rail"; cap | head -14; fi

send "d"
if [ -n "$VIEW1" ] && ! tmux $TA has-session -t "=$VIEW1" 2>/dev/null; then
  pass "ownership: detach cleaned the exact owned shadow"
else fail "ownership: detach left $VIEW1 behind"; fi
if tmux $TA has-session -t =gm-view-legacy 2>/dev/null && tmux $TA has-session -t =gm-view-malformed 2>/dev/null; then
  pass "ownership: detach preserved unowned legacy sessions"
else fail "ownership: detach killed an unowned legacy session"; fi

# A second attach to the same target must get a fresh name. Kill the ORIGINAL
# target while the child still runs: heal must not let the shadow keep it alive.
send "" Enter
VIEW2=$(tmux $TA list-sessions -F '#{session_name} #{@ghostmux_view_owner}' 2>/dev/null |
  awk '$1 ~ /^gm-view-/ && $2 == "v1:" $1 {print $1}' | head -1)
if [ -n "$VIEW2" ] && [ "$VIEW2" != "$VIEW1" ]; then pass "ownership: repeated attach used a unique shadow"
else fail "ownership: shadow name was reused ($VIEW1 -> $VIEW2)"; fi
tmux $TA kill-session -t =alpha
sleep 3
if cap | grep -q "the rail is watching"; then pass "heal: externally-killed original target idled the viewport"
else fail "heal: grouped shadow kept a killed target rendered"; cap | head -12; fi
if [ -n "$VIEW2" ] && ! tmux $TA has-session -t "=$VIEW2" 2>/dev/null; then
  pass "heal: target kill cleaned the owned shadow"
else fail "heal: target-kill shadow survived ($VIEW2)"; fi
if cap | grep -q "\[alpha\]"; then fail "heal: still showing a dead session"; else pass "heal: dead session no longer rendered"; fi
if tmux $TA has-session -t =gm-view-legacy 2>/dev/null && tmux $TA has-session -t =gm-view-malformed 2>/dev/null; then
  pass "ownership: target-kill cleanup preserved legacy sessions"
else fail "ownership: target-kill cleanup killed a legacy session"; fi

# --- quit ---
send "q"
if cap | grep -q "the rail is watching"; then fail "quit: frame still painted after q"
else pass "quit: q exited the frame"; fi
if tmux $TA has-session -t =zdriver 2>/dev/null; then pass "quit: host session intact (the panel never kills sessions)"
else fail "quit: host session died"; fi
HOOKS_AFTER=$({ tmux $TA show-hooks -g; tmux $TA show-hooks -gw; } 2>/dev/null)
if echo "$HOOKS_AFTER" | grep -q 'ghostmux-refresh-v1-'; then
  fail "quit: tokenized ghostmux lease entries remained"
else pass "quit: only ghostmux lease entries were removed"; fi
if echo "$HOOKS_AFTER" | grep -q 'alert-bell\[133\].*user-133'; then
  pass "quit: user hook [133] survived exact cleanup"
else fail "quit: user hook [133] was removed or changed"; echo "$HOOKS_AFTER" | grep alert-bell; fi
MONITOR_AFTER=$(tmux $TA show-options -gv monitor-activity 2>/dev/null)
VISUAL_AFTER=$(tmux $TA show-options -gv visual-activity 2>/dev/null)
if [ "$MONITOR_AFTER" = "off" ] && [ "$VISUAL_AFTER" = "on" ]; then
  pass "quit: monitor/visual globals remain untouched"
else fail "quit: globals changed after exit ($MONITOR_AFTER/$VISUAL_AFTER)"; fi
if tmux $TA has-session -t =gm-view-legacy 2>/dev/null && tmux $TA has-session -t =gm-view-malformed 2>/dev/null; then
  pass "ownership: panel exit preserved all unowned legacy sessions"
else fail "ownership: panel exit killed an unowned legacy session"; fi
# They proved survival; remove only as scratch-fixture cleanup before later
# cursor/order-sensitive scenarios.
tmux $TA kill-session -t =gm-view-legacy 2>/dev/null
tmux $TA kill-session -t =gm-view-malformed 2>/dev/null
tmux $TA kill-session -t =watcher 2>/dev/null

# --- tmux-absent guard: the frame must run and say so without tmux ---
tmux $TA new-session -d -s notmux -x 100 -y 30
tmux $TA send-keys -t notmux "PATH=/nonexistent-bin $BIN" Enter
sleep 2
NOTMUX=$(tmux $TA capture-pane -p -t notmux)
if echo "$NOTMUX" | grep -q "gmx"; then
  pass "no-tmux: frame still runs with tmux off PATH"
else fail "no-tmux: frame did not start without tmux"; echo "$NOTMUX" | head -6; fi
tmux $TA send-keys -t notmux "q"; sleep 0.5

# --- self-exclusion: the panel run INSIDE a session must not list its own host ---
# This is what makes `tmux new -A -s gm ghostmux` safe: relaunching rebuilds
# live rows from the muxes and saved organization from state — but only if ↵
# can never render the frame inside itself.
tmux $TA kill-session -t zdriver 2>/dev/null
tmux $TA new-session -d -s zdriver -x 120 -y 32
tmux $TA new-session -d -s beta -x 80 -y 20
tmux $TA send-keys -t zdriver "GHOSTMUX_TMUX_ARGS='$TA' $BIN" Enter
sleep 2.5
if cap | grep -q "beta"; then pass "self-host: other sessions still listed"
else fail "self-host: exclusion removed everything"; cap | head -10; fi
if cap | grep -q "zdriver"; then fail "self-host: rail listed the session hosting it"
else pass "self-host: own host session excluded from the rail"; fi
tmux $TA send-keys -t zdriver "q"; sleep 0.5


# --- grouping: the one thing ghostmux owns that it cannot rediscover ---
tmux $TA kill-session -t zdriver 2>/dev/null
tmux $TA new-session -d -s zdriver -x 120 -y 32
tmux $TA new-session -d -s gamma -x 80 -y 20
tmux $TA send-keys -t zdriver "XDG_STATE_HOME='$XDG_STATE_HOME' GHOSTMUX_TMUX_ARGS='$TA' $BIN" Enter
sleep 2.5
send "a"
tmux $TA send-keys -t zdriver "work"; sleep 0.5
send "" Enter
if cap | grep -q "work"; then pass "group: a created a named group folder"
else fail "group: folder not rendered"; cap | head -10; fi

# move a session up into the group (ungrouped rows sit below every group).
# Filter to gamma first so the cursor cannot land on an unrelated session.
send "/"
tmux $TA send-keys -t zdriver "gamma"; sleep 0.5
send "" Enter
send "g"
send "j"
# Modal movement is a draft: any number of previews, and Esc, write nothing.
send "m"
send "k"
if cap | grep -q "moving gamma"; then pass "group: m shows the move-preview hint"
else fail "group: move-preview hint missing"; cap | tail -4; fi
if has_member "$XDG_STATE_HOME/ghostmux/groups.json" "tmux:gamma" 2>/dev/null; then
  fail "group: preview wrote membership before drop"
else pass "group: preview made no state write"; fi
send "Escape"
if has_member "$XDG_STATE_HOME/ghostmux/groups.json" "tmux:gamma" 2>/dev/null; then
  fail "group: Esc persisted the discarded draft"
else pass "group: Esc discarded the draft"; fi
send "m"
send "k"
send "" Enter
if has_member "$XDG_STATE_HOME/ghostmux/groups.json" "tmux:gamma" 2>/dev/null; then
  pass "group: Enter dropped and persisted the preview"
else fail "group: Enter did not persist the preview"; cat "$XDG_STATE_HOME/ghostmux/groups.json" 2>/dev/null; fi
send "u"
if has_member "$XDG_STATE_HOME/ghostmux/groups.json" "tmux:gamma" 2>/dev/null; then
  fail "group: u did not undo the dropped move"; cat "$XDG_STATE_HOME/ghostmux/groups.json"; cap | tail -4
else pass "group: u undid the organization move"; fi
# The immediate expert path remains available and uses the same one-step move.
send "K"
if [ -f "$XDG_STATE_HOME/ghostmux/groups.json" ]; then pass "group: membership persisted to the state file"
else fail "group: no state file written"; fi
if grep -q '"version": 1' "$XDG_STATE_HOME/ghostmux/groups.json" 2>/dev/null; then
  pass "group: state file uses schema version 1"
else fail "group: state file has no schema version"; cat "$XDG_STATE_HOME/ghostmux/groups.json" 2>/dev/null; fi
if [ -f "$XDG_STATE_HOME/ghostmux/groups.json.bak" ] && [ -f "$XDG_STATE_HOME/ghostmux/groups.json.lock" ]; then
  pass "group: backup and lock sidecars exist"
else fail "group: backup or lock sidecar missing"; ls -la "$XDG_STATE_HOME/ghostmux" 2>/dev/null; fi
if grep -q '"name": "work"' "$XDG_STATE_HOME/ghostmux/groups.json" 2>/dev/null; then
  pass "group: state file names the group"
else fail "group: state file malformed"; cat "$XDG_STATE_HOME/ghostmux/groups.json" 2>/dev/null; fi
if grep -qE '"tmux:[a-z]+"' "$XDG_STATE_HOME/ghostmux/groups.json" 2>/dev/null; then
  pass "group: K moved a session into the group"
else fail "group: no member recorded"; cat "$XDG_STATE_HOME/ghostmux/groups.json" 2>/dev/null; fi

# fold the group, then relaunch: a folder that springs open is not a folder.
# After the K move the cursor sits on the moved member: h selects the group
# header, a second h collapses it through the persisted fold path.
send "h"
send "h"
if grep -q '"collapsed"' "$XDG_STATE_HOME/ghostmux/groups.json" 2>/dev/null; then
  pass "group: fold state written to the state file"
else fail "group: fold not persisted"; cat "$XDG_STATE_HOME/ghostmux/groups.json" 2>/dev/null; fi

# a relaunch must rebuild the grouping from disk — it cannot be rediscovered
send "q"
sleep 0.5
tmux $TA send-keys -t zdriver "XDG_STATE_HOME='$XDG_STATE_HOME' GHOSTMUX_TMUX_ARGS='$TA' $BIN" Enter
sleep 2.5
if cap | grep -q "work"; then pass "group: survived a relaunch (state file reloaded)"
else fail "group: lost on relaunch"; cap | head -10; fi
if cap | grep -q "▸ work"; then pass "group: still folded after relaunch"
else fail "group: sprang open on relaunch"; cap | head -10; fi
send "q"


# --- ghosts: the fleet outlives its processes ---
# Grouping IS the declaration, so a member whose session is gone does not
# vanish from the rail: it renders as a ghost — dim, ○, still in its folder —
# and ↵ starts it again in the dir it was last observed in. Nothing here is a
# restore: the name and the dir are the whole claim, and both are facts the
# rail watched while the session lived.
#
# Its own state dir, so the grouping section above cannot decide what this one
# sees. The other scratch sessions go first: with only the declared one left,
# the cursor lands where the test says it does.
GHOSTSTATE=$(mktemp -d)
GHOSTRUN="XDG_STATE_HOME='$GHOSTSTATE' GHOSTMUX_TMUX_ARGS='$TA' $BIN"
GJSON="$GHOSTSTATE/ghostmux/groups.json"

tmux $TA kill-session -t zdriver 2>/dev/null
for s in alpha beta gamma notmux; do tmux $TA kill-session -t "=$s" 2>/dev/null; done
tmux $TA new-session -d -s zdriver -x 120 -y 32
tmux $TA new-session -d -s ghosty -c /tmp -x 80 -y 20
tmux $TA send-keys -t zdriver "$GHOSTRUN" Enter
sleep 2.5

send "a"
tmux $TA send-keys -t zdriver "fleet"; sleep 0.5
send "" Enter
# g/j, never G: filtering first keeps the cursor off unrelated sessions. A
# test that lands on one of them by counting from the bottom would group —
# and later kill — somebody's session.
send "g"
send "j"
send "K"
if grep -q '"tmux:ghosty"' "$GJSON" 2>/dev/null; then pass "ghost: session declared into a group"
else fail "ghost: membership not recorded"; cat "$GJSON" 2>/dev/null; fi
if grep -q '"dirs"' "$GJSON" 2>/dev/null && grep -q '"tmux:ghosty": "/tmp"' "$GJSON" 2>/dev/null; then
  pass "ghost: session_path captured while the session lived"
else fail "ghost: dir not recorded"; cat "$GJSON" 2>/dev/null; fi

# kill it from outside and relaunch: the declaration must survive the process
send "q"
sleep 0.5
tmux $TA kill-session -t =ghosty
tmux $TA send-keys -t zdriver "$GHOSTRUN" Enter
sleep 2.5
if cap | grep -qE "ghosty.*○"; then pass "ghost: dead member renders as a ○ row"
else fail "ghost: no ghost row after the session died"; cap | head -10; fi

# ↵ summons it back — into the dir the rail recorded, not wherever we are
send "g"
send "j"
send "" Enter
sleep 1.5
if tmux $TA has-session -t =ghosty 2>/dev/null; then pass "ghost: ↵ summoned the session back"
else fail "ghost: ↵ did not start the session"; cap | head -12; fi
# list-sessions, not display-message: display-message resolves its format
# against a client, and there is no client on this session — it answers "".
GDIR=$(tmux $TA list-sessions -F '#{session_name} #{session_path}' 2>/dev/null | awk '$1=="ghosty"{print $2}')
if [ "$GDIR" = "/tmp" ]; then pass "ghost: summoned into its recorded dir"
else fail "ghost: summoned into $GDIR, want /tmp"; fi
if cap | grep -q "\[ghosty\]"; then pass "ghost: viewport attached to the summoned session"
else fail "ghost: no inner client after the summon"; cap | head -14; fi

# x on a ghost is not a kill — there is nothing left to kill. It forgets the
# declaration, and says so before it does it.
send "q"; sleep 0.5
tmux $TA kill-session -t =ghosty
tmux $TA send-keys -t zdriver "$GHOSTRUN" Enter
sleep 2.5
send "g"
send "j"
send "x"
if cap | grep -q "forget ghosty"; then pass "ghost: x names the real verb (forget, not kill)"
else fail "ghost: confirm prompt does not say forget"; cap | tail -4; fi
send "y"
if cap | grep -q "ghosty"; then fail "ghost: the row survived x"; cap | head -10
else pass "ghost: x removed the ghost row"; fi
if grep -q '"tmux:ghosty"' "$GJSON" 2>/dev/null; then
  fail "ghost: state file still declares the forgotten member"; cat "$GJSON"
else pass "ghost: declaration pruned from the state file"; fi
send "q"; sleep 0.5

# --- reach rows (PROTOTYPE): declared remote workspaces summoned over ssh ---
# A PATH shim stands in for ssh. The panel's whole contract here is "run
# `ssh -t <host> -- tmux new-session -A -s <session>` as the viewport child";
# the shim proves that exact argv end-to-end by execing the same tmux command
# against the scratch server instead of a network.
SHIM=$(mktemp -d)
cat > "$SHIM/ssh" <<'SHIMEOF'
#!/usr/bin/env bash
# fake ssh: expect -t <host> -- tmux <args...>; run tmux on the scratch server
shift 2
[ "$1" = "--" ] && shift
[ "$1" = "tmux" ] && shift
exec env -u TMUX -u TMUX_PANE tmux -L gm-solo -f /dev/null "$@"
SHIMEOF
chmod +x "$SHIM/ssh"

RSTATE=$(mktemp -d)
XDG_STATE_HOME="$RSTATE" "$BIN" reach add faraway fakehost gm-reach-demo \
  || fail "reach: add command failed"
if XDG_STATE_HOME="$RSTATE" "$BIN" reach ls | grep -q "faraway	fakehost	gm-reach-demo"; then
  pass "reach: declared target listed by the CLI"
else fail "reach: ls does not list the declaration"; fi

tmux $TA kill-session -t zdriver 2>/dev/null
tmux $TA new-session -d -s zdriver -x 120 -y 32
tmux $TA send-keys -t zdriver "PATH='$SHIM':\$PATH XDG_STATE_HOME='$RSTATE' GHOSTMUX_TMUX_ARGS='$TA' $BIN" Enter
sleep 2.5
if cap | grep -qE "faraway.*↗|↗.*faraway"; then pass "reach: declared row renders with the ↗ mark"
else fail "reach: row missing from the rail"; cap | head -10; fi
if cap | grep -q "fakehost"; then pass "reach: host shown in the dim command slot"
else fail "reach: host not shown"; cap | head -10; fi

send "/"
tmux $TA send-keys -t zdriver "faraway"; sleep 0.5
send "" Enter
send "j"
send "" Enter
sleep 2
if tmux $TA has-session -t =gm-reach-demo 2>/dev/null; then
  pass "reach: ↵ summoned the remote session through ssh"
else fail "reach: no session created by the summon"; cap | head -14; fi
# status-left-length 10 truncates the inner bar to "[gm-reach-" — the exact
# illusion the README documents; assert on the truncated truth.
if cap | grep -q "\[gm-reach-"; then pass "reach: viewport attached to the summoned session"
else fail "reach: viewport not showing the remote session"; cap | head -14; fi

send "d"
if tmux $TA has-session -t =gm-reach-demo 2>/dev/null; then
  pass "reach: detach left the remote session running"
else fail "reach: detach killed the remote session"; fi
send "q"; sleep 0.5
tmux $TA kill-session -t =gm-reach-demo 2>/dev/null


echo
[ $FAIL -eq 0 ] && echo "ALL CHECKS PASSED" || echo "SOME CHECKS FAILED"
exit $FAIL
