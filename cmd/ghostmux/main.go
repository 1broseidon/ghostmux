// ghostmux: attach-anywhere mission control for your multiplexers.
//
// Type `ghostmux` and you are in the panel: a persistent rail of every session
// across every multiplexer you have installed, with a live attention gutter,
// beside a viewport that renders whatever you select. ghostmux owns the outer
// frame — it draws the rail itself and runs the selected session as a child on
// a terminal it emulates in process. No outer tmux, no nesting, one status bar.
//
// The inner multiplexers keep everything worth keeping: persistence, session
// truth, their own keymaps. The panel holds no state of its own, so it can be
// killed and relaunched anywhere and rebuild the same cockpit from what the
// multiplexers report.
//
// Law of the rail: render evidence, never inference. Scope law: ship only what
// the multiplexer alone can't give you.
package main

import (
	"fmt"
	"os"

	"github.com/1broseidon/ghostmux/internal/app"
	"github.com/1broseidon/ghostmux/internal/rail"
	"github.com/1broseidon/ghostmux/internal/wiring"
)

func main() {
	// Bare `ghostmux` IS the product: log in, type it, you're in the panel.
	if len(os.Args) < 2 {
		exit(app.Run(nil))
	}
	var err error
	switch os.Args[1] {
	case "up":
		err = wiring.CmdUp(os.Args[2:])
	case "ls":
		err = wiring.CmdLs()
	case "doctor":
		err = wiring.CmdDoctor()
	case "rail":
		err = rail.CmdRail(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "ghostmux: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	exit(err)
}

func exit(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ghostmux:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func usage() {
	fmt.Print(`ghostmux — attach-anywhere mission control for your multiplexers

Type ghostmux. You're in the panel: every session from every multiplexer you
have installed, in one rail with a live attention gutter, beside a viewport
that renders whatever you select. ghostmux owns the frame; tmux and zellij
keep the sessions.

usage:
  ghostmux             open the panel
  ghostmux doctor      report what ghostmux can see
  ghostmux ls          list tmux sessions
  ghostmux up <name>   attach a named tmux session in place
  ghostmux rail once   print one frame of the rail and exit (debugging)

The panel holds no state: quit it, relaunch it anywhere, and it rebuilds the
same cockpit from what the multiplexers report. Sessions are never yours to
lose — ghostmux only ever draws them.

Keys: press ? in the panel for the keymap overlay (any key closes it), and ,
for settings — keys, rail width, agent detection, backends, state, about. Both
are the frame's own keys, so neither is taken from the program you are viewing.
The rail ⇄ viewport toggle is ctrl+\ or F12; two
keys because a desktop environment can grab a chord before the terminal sees
it, and a dead toggle reports no error. Override with GHOSTMUX_TOGGLE (a
comma-separated list). ? always shows the key that is actually bound.
`)
}
