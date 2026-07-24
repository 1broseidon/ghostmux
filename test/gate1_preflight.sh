#!/usr/bin/env bash
# Gate 1 pre-flight: the machine-checkable half of the fidelity checklist.
# Drives cmd/termspike inside a scratch tmux pane and asserts on capture-pane.
# The human half (does vim/htop/claude-code LOOK right) is still George's.
set -u

REPO=$(cd "$(dirname "$0")/.." && pwd)
SOCK=gm-spike
TA="-L $SOCK -f /dev/null"
BIN=$(mktemp -d)/termspike
FAIL=0

pass() { echo "PASS: $1"; }
fail() { echo "FAIL: $1"; FAIL=1; }

cleanup() { tmux $TA kill-server 2>/dev/null; }
trap cleanup EXIT

cd "$REPO" || exit 1
go build -o "$BIN" ./cmd/termspike || { echo "FAIL: build"; exit 1; }
pass "build termspike"

cleanup
# target: the session the widget attaches to (the "inner mux")
tmux $TA new-session -d -s target -x 80 -y 20
tmux $TA rename-window -t target:0 innerwin
# driver: hosts termspike itself, so we can capture-pane what it painted
tmux $TA new-session -d -s driver -x 100 -y 30
tmux $TA send-keys -t driver \
  "GHOSTMUX_TMUX_ARGS='$TA' $BIN target" Enter
sleep 2

cap()  { tmux $TA capture-pane -p -t driver; }
cape() { tmux $TA capture-pane -p -e -t driver; }
send() { tmux $TA send-keys -t driver "$@"; sleep "${SLEEP:-1.2}"; }

# 1. the frame is up and the child is alive
if cap | grep -q "termspike · ctrl+q quits"; then pass "frame: status line rendered"
else fail "frame: status line missing"; cap | tail -5; fi
if cap | grep -q "running:true"; then pass "child: pty child running"
else fail "child: not running"; fi

# 2. inner tmux chrome — the nested client's own status bar reaches the emulator
if cap | grep -q "innerwin"; then pass "chrome: inner tmux status bar visible (innerwin)"
else fail "chrome: inner status bar not rendered"; cap | tail -8; fi

# 3. keystrokes round-trip: outer stdin -> bubbletea -> vt encoder -> pty -> child
send "printf 'HELLOFIDELITY-%s\\n' ok" Enter
if cap | grep -q "HELLOFIDELITY-ok"; then pass "input: keys round-trip to child shell"
else fail "input: child never saw the keystrokes"; cap | tail -8; fi

# 4. truecolor SGR survives the emulator round-trip
send "printf '\\033[38;2;215;153;33mGOLDTEXT\\033[0m\\n'" Enter
if cape | grep -q "GOLDTEXT"; then
  if cape | grep -E "38;2;215;153;33|#d79921" | grep -q GOLDTEXT; then
    pass "color: truecolor SGR preserved through vt"
  else
    fail "color: GOLDTEXT rendered but color attribute lost"
    cape | grep -n GOLDTEXT | cat -v | head -3
  fi
else fail "color: GOLDTEXT never rendered"; fi

# 5. alt-screen with a tmux child is true from attach onward (tmux owns the
# alt screen for its whole client lifetime) — vim inside it must NOT toggle it.
if cap | grep -q "alt-screen:true"; then pass "alt-screen: true while attached to tmux (expected)"
else fail "alt-screen: tmux client did not enter alt screen"; fi

# 6. wide glyphs / UTF-8 do not corrupt the grid
send "printf '日本語WIDE\\n'" Enter
if cap | grep -q "日本語WIDE"; then pass "utf8: wide glyphs render"
else fail "utf8: wide glyphs mangled"; cap | tail -5; fi

# 7. resize mid-stream: outer pane shrinks, widget resizes, child survives
tmux $TA resize-window -t driver -x 70 -y 20
sleep 1.5
if cap | grep -q "running:true"; then pass "resize: child survives outer resize"
else fail "resize: child died on resize"; cap | tail -6; fi
SLEEP=1.5 send "printf 'AFTERRESIZE\\n'" Enter
if cap | grep -q "AFTERRESIZE"; then pass "resize: input still lands post-resize"
else fail "resize: input lost after resize"; cap | tail -6; fi

# 8. quit is clean — ctrl+q is the only key the frame steals
tmux $TA send-keys -t driver C-q
sleep 1
if cap | grep -q "termspike · ctrl+q"; then fail "quit: frame still painted after ctrl+q"
else pass "quit: ctrl+q exited the frame"; fi

# --- scenario 2: bare shell child, where alt-screen enter/exit is observable ---
tmux $TA kill-session -t driver 2>/dev/null
tmux $TA new-session -d -s driver -x 100 -y 30
tmux $TA send-keys -t driver "$BIN -- /bin/sh" Enter
sleep 1.5

if cap | grep -q "alt-screen:false"; then pass "alt-screen: false for a plain shell child"
else fail "alt-screen: plain shell should not be in alt screen"; cap | tail -4; fi
send "nvim -u NONE" Enter
sleep 1
if cap | grep -q "alt-screen:true"; then pass "alt-screen: entered on vim"
else fail "alt-screen: not detected on vim"; cap | tail -4; fi
send ":q" Enter
sleep 1
if cap | grep -q "alt-screen:false"; then pass "alt-screen: restored on :q"
else fail "alt-screen: stuck after :q"; cap | tail -4; fi
send "printf 'BACKINSHELL\\n'" Enter
if cap | grep -q "BACKINSHELL"; then pass "alt-screen: primary screen usable after exit"
else fail "alt-screen: primary screen broken after vim"; cap | tail -6; fi

# child exit is reported, not faked (evidence law: last real frame stays)
send "exit" Enter
sleep 1
if cap | grep -q "termspike · ctrl+q"; then fail "exit: frame outlived a dead child"
else pass "exit: child exit tore the frame down"; fi

# --- scenario 3: zellij as the child (the multi-backend claim) ---
if command -v zellij >/dev/null 2>&1; then
  ZS=gm-spike-zj
  zellij delete-session "$ZS" --force >/dev/null 2>&1
  zellij attach --create-background "$ZS" >/dev/null 2>&1
  sleep 1
  tmux $TA kill-session -t driver 2>/dev/null
  tmux $TA new-session -d -s driver -x 100 -y 30
  tmux $TA send-keys -t driver "$BIN -- zellij attach $ZS" Enter
  sleep 3
  if cap | grep -q "running:true"; then pass "zellij: child attached and running"
  else fail "zellij: child not running"; cap | tail -6; fi
  # zellij paints its own chrome (tab bar / status) into the emulator
  if cap | grep -Eq "Ctrl|TAB|Tab #1"; then pass "zellij: chrome rendered through vt"
  else fail "zellij: no chrome visible"; cap | tail -8; fi
  # Prove input reaches zellij via its OWN mode indicator, not a shell echo:
  # a fresh zellij session opens the "About Zellij" floating pane, which
  # swallows pane-bound keys. ctrl+g is handled by zellij itself.
  tmux $TA send-keys -t driver C-g; sleep 2
  if cap | grep -q "LOCK"; then pass "zellij: ctrl+g reached child (mode → LOCK)"
  else fail "zellij: keystrokes lost"; cap | tail -4; fi
  tmux $TA send-keys -t driver C-q; sleep 1
  zellij delete-session "$ZS" --force >/dev/null 2>&1
else
  echo "SKIP: zellij not on PATH"
fi

echo
[ $FAIL -eq 0 ] && echo "PRE-FLIGHT CLEAN" || echo "PRE-FLIGHT HAS FAILURES"
exit $FAIL
