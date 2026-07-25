#!/usr/bin/env bash
# Acceptance for `ghostmux`: run the panel inside a
# scratch tmux pane and assert on capture-pane. Everything runs on a throwaway
# server (-L gm-solo -f /dev/null), so the user's real sessions are untouched.
set -u

REPO=$(cd "$(dirname "$0")/.." && pwd)
TA="-L gm-solo -f /dev/null"
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
tmux $TA send-keys -t zdriver "GHOSTMUX_TMUX_ARGS='$TA' $BIN" Enter
sleep 2.5

cap() { tmux $TA capture-pane -p -t zdriver; }
send() { tmux $TA send-keys -t zdriver "$@"; sleep "${SLEEP:-1.5}"; }

# --- frame chrome ---
if cap | grep -q "gmx"; then pass "bar: gmx identity block rendered"
else fail "bar: no gmx block"; cap | tail -3; fi

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

tmux $TA send-keys -t zdriver 'C-\'; sleep 0.8
send "printf 'SOLOKEYS-%s\\n' ok" Enter
if cap | grep -q "SOLOKEYS-ok"; then pass "focus: ctrl+\\ hands the keyboard to the child"
else fail "focus: child never saw keys after ctrl+\\"; cap | head -14; fi

tmux $TA send-keys -t zdriver 'C-\'; sleep 0.8

# F12 is the second accepted toggle: a desktop environment that grabs ctrl+\
# (1Password does, on some Linux setups) must not lock the user in the viewport
tmux $TA send-keys -t zdriver F12; sleep 0.8
send "printf 'F12KEYS-%s\\n' ok" Enter
if cap | grep -q "F12KEYS-ok"; then pass "focus: F12 toggles too (ctrl+\\ is not a single point of failure)"
else fail "focus: F12 did not hand the keyboard over"; cap | head -14; fi

# back to the rail, and prove `q` is a rail key again (not sent to the child)
tmux $TA send-keys -t zdriver F12; sleep 0.8

# the help overlay must name the keys actually bound, or it is worse than
# nothing — and it must name them WHOLE. In the 30-column rail this row read
# "ctrl+\  toggle rail ⇄ vi…", which is exactly the row a user whose desktop
# grabbed the chord needs intact.
send "?"
if cap | grep -q "F12\|f12"; then pass "help: ? reports the real toggle keys"
else fail "help: ? does not show the bound toggle"; cap | head -18; fi
if cap | grep -q "ghostmux · keys"; then pass "help: ? draws the overlay box with its title"
else fail "help: no overlay title"; cap | head -18; fi
if cap | grep -q "start group's dead sessions"; then
  pass "help: the longest keymap row renders un-truncated"
else fail "help: keymap row truncated in the overlay"; cap | head -20; fi
# any key closes it — including a key the rail would otherwise act on
send "j"
if cap | grep -q "ghostmux · keys"; then fail "help: overlay survived a keypress"; cap | head -8
else pass "help: any key closes the overlay"; fi
if cap | grep -q "alpha"; then pass "help: rail rows are back after the overlay closed"
else fail "help: rail did not come back"; cap | head -10; fi

# --- settings: a mode, because sections/fields honor the panes' contract ---
send ","
if cap | grep -q "Backends"; then pass "settings: , opened the sections list"
else fail "settings: , did not open settings"; cap | head -12; fi
if cap | grep -q "About"; then pass "settings: every section listed"
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

# --- heal loop guard: a session killed from outside must idle, not re-attach ---
send "" Enter          # re-point at alpha
tmux $TA kill-session -t =alpha
sleep 3
if cap | grep -q "the rail is watching"; then pass "heal: externally-killed session idled the viewport"
else fail "heal: viewport did not idle after its session was killed"; cap | head -12; fi
if cap | grep -q "\[alpha\]"; then fail "heal: still showing a dead session"; else pass "heal: dead session no longer rendered"; fi

# --- quit ---
send "q"
if cap | grep -q "the rail is watching"; then fail "quit: frame still painted after q"
else pass "quit: q exited the frame"; fi
if tmux $TA has-session -t =zdriver 2>/dev/null; then pass "quit: host session intact (the panel never kills sessions)"
else fail "quit: host session died"; fi
if tmux $TA show-hooks -g 2>/dev/null | grep -q "\[133\]"; then
  fail "quit: ghostmux hooks left behind at [133]"
else pass "quit: no [133] hooks left behind"; fi

# --- tmux-absent guard: a zellij-only box is a first-class case ---
tmux $TA new-session -d -s notmux -x 100 -y 30
tmux $TA send-keys -t notmux "PATH=/nonexistent-bin $BIN" Enter
sleep 2
NOTMUX=$(tmux $TA capture-pane -p -t notmux)
if echo "$NOTMUX" | grep -q "gmx"; then
  pass "no-tmux: frame still runs with tmux off PATH"
else fail "no-tmux: frame did not start without tmux"; echo "$NOTMUX" | head -6; fi
tmux $TA send-keys -t notmux "q"; sleep 0.5

# --- zellij as the viewport child: the multi-backend claim ---
if command -v zellij >/dev/null 2>&1; then
  ZS=gm-solo-zj
  zellij delete-session "$ZS" --force >/dev/null 2>&1
  zellij attach --create-background "$ZS" >/dev/null 2>&1
  sleep 1
  tmux $TA kill-session -t zdriver 2>/dev/null
  tmux $TA new-session -d -s zdriver -x 120 -y 32
  tmux $TA send-keys -t zdriver "GHOSTMUX_TMUX_ARGS='$TA' $BIN" Enter
  sleep 2.5
  if cap | grep -q "$ZS"; then pass "zellij: session listed in the rail beside tmux"
  else fail "zellij: session not listed"; cap | head -10; fi
  # aux rows are appended last, so G lands on the zellij row
  send "G"
  send "" Enter
  sleep 2
  if cap | grep -Eq "Ctrl \+|Tab #1"; then pass "zellij: ↵ attached a zellij client in the viewport"
  else fail "zellij: no zellij chrome after ↵"; cap | head -16; fi
  send "d"
  zellij delete-session "$ZS" --force >/dev/null 2>&1
  tmux $TA send-keys -t zdriver "q"; sleep 0.5
else
  echo "SKIP: zellij not on PATH"
fi

# --- self-exclusion: the panel run INSIDE a session must not list its own host ---
# This is what makes `tmux new -A -s gm ghostmux` safe: the panel is
# stateless, so relaunching it rebuilds the cockpit — but only if ↵ can never
# render the frame inside itself.
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
# Filter to gamma first: the rail also lists the developer's real zellij
# sessions — including EXITED ones, as ghosts — and those are appended last,
# so counting from the bottom would grab somebody else's session.
send "/"
tmux $TA send-keys -t zdriver "gamma"; sleep 0.5
send "" Enter
send "g"
send "j"
send "K"
if [ -f "$XDG_STATE_HOME/ghostmux/groups.json" ]; then pass "group: membership persisted to the state file"
else fail "group: no state file written"; fi
if grep -q '"name": "work"' "$XDG_STATE_HOME/ghostmux/groups.json" 2>/dev/null; then
  pass "group: state file names the group"
else fail "group: state file malformed"; cat "$XDG_STATE_HOME/ghostmux/groups.json" 2>/dev/null; fi
if grep -qE '"tmux:[a-z]+"' "$XDG_STATE_HOME/ghostmux/groups.json" 2>/dev/null; then
  pass "group: K moved a session into the group"
else fail "group: no member recorded"; cat "$XDG_STATE_HOME/ghostmux/groups.json" 2>/dev/null; fi

# fold the group, then relaunch: a folder that springs open is not a folder
send "g"
send ""  Enter
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
# g/j, never G: the rail also lists whatever zellij sessions the developer
# really has, and those are appended LAST. A test that lands on one of them by
# counting from the bottom would group — and later kill — somebody's session.
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

# --- ghosts on zellij: same declaration, the backend's own honesty ---
# NOTE: `zellij kill-session` leaves NO resurrectable row on zellij 0.44.3 —
# it removes the serialized session with the process (probed). So this section
# exercises the zellij ghost that IS reproducible: a declared member zellij no
# longer lists at all, summoned back with `attach --create-background`. The
# EXITED flavour (a session zellij still lists, resurrected by attaching) is
# covered by unit tests, because no CLI command manufactures one.
if command -v zellij >/dev/null 2>&1; then
  ZG=gm-ghost-zj
  ZSTATE=$(mktemp -d)
  ZRUN="XDG_STATE_HOME='$ZSTATE' GHOSTMUX_TMUX_ARGS='$TA' $BIN"
  ZJSON="$ZSTATE/ghostmux/groups.json"
  zellij delete-session "$ZG" --force >/dev/null 2>&1
  zellij attach --create-background "$ZG" >/dev/null 2>&1
  sleep 1
  tmux $TA kill-session -t zdriver 2>/dev/null
  tmux $TA new-session -d -s zdriver -x 120 -y 32
  tmux $TA send-keys -t zdriver "$ZRUN" Enter
  sleep 2.5
  send "a"
  tmux $TA send-keys -t zdriver "zfleet"; sleep 0.5
  send "" Enter
  # Filter to OUR session first: j only stops on matching rows, so the cursor
  # cannot land on a zellij session this test did not create.
  send "/"
  tmux $TA send-keys -t zdriver "$ZG"; sleep 0.5
  send "" Enter
  send "g"
  send "j"
  send "K"
  if grep -q "\"zellij:$ZG\"" "$ZJSON" 2>/dev/null; then
    pass "ghost/zellij: a zellij session can be declared into a group"
  else fail "ghost/zellij: membership not recorded"; cat "$ZJSON" 2>/dev/null; fi

  send "q"; sleep 0.5
  zellij kill-session "$ZG" >/dev/null 2>&1
  sleep 1
  tmux $TA send-keys -t zdriver "$ZRUN" Enter
  sleep 2.5
  if cap | grep -qE "$ZG.*○"; then pass "ghost/zellij: dead member renders as a ○ row"
  else fail "ghost/zellij: no ghost row after the session died"; cap | head -10; fi

  send "/"
  tmux $TA send-keys -t zdriver "$ZG"; sleep 0.5
  send "" Enter
  send "g"
  send "j"
  send "" Enter
  sleep 2
  if zellij list-sessions --no-formatting 2>/dev/null | grep -q "^$ZG "; then
    pass "ghost/zellij: ↵ summoned the session back onto its own backend"
  else fail "ghost/zellij: ↵ did not bring the session back"; cap | head -12; fi
  send "q"; sleep 0.5
  zellij delete-session "$ZG" --force >/dev/null 2>&1
else
  echo "SKIP: zellij not on PATH (ghost section)"
fi


echo
[ $FAIL -eq 0 ] && echo "ALL CHECKS PASSED" || echo "SOME CHECKS FAILED"
exit $FAIL
