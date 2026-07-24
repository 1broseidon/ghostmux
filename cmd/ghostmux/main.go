// ghostmux: attach-anywhere mission control for your multiplexers.
//
// The hub is itself a tmux session: a persistent rail (session tree + live
// attention gutter) beside a viewport that renders whatever you select as a
// nested client. Attach it from any terminal on any machine — ghostty,
// iTerm2, an ssh box — and your cockpit is identical, because the whole
// control surface lives in the multiplexer, never in the terminal.
//
// Law of the rail: render evidence, never inference. Scope law: ship only
// what the multiplexer alone can't give you.
package main

import (
	"fmt"
	"os"

	"github.com/1broseidon/ghostmux/internal/rail"
	"github.com/1broseidon/ghostmux/internal/wiring"
)

func main() {
	// Bare `ghostmux` IS the product: log in, type it, you're in the hub.
	if len(os.Args) < 2 {
		exit(wiring.CmdHub(nil))
	}
	var err error
	switch os.Args[1] {
	case "hub":
		err = wiring.CmdHub(os.Args[2:])
	case "up":
		err = wiring.CmdUp(os.Args[2:])
	case "ls":
		err = wiring.CmdLs()
	case "doctor":
		err = wiring.CmdDoctor()
	case "rail":
		err = rail.CmdRail(os.Args[2:])
	case "ghostty":
		err = wiring.CmdGhostty(os.Args[2:])
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

Type ghostmux. You're in the hub: a persistent rail of every session with a
live attention gutter, beside a viewport that renders whatever you select.
The hub is itself a tmux session — ssh in from anywhere, same cockpit.

usage:
  ghostmux                  open the hub (create or attach)
  ghostmux up <name> [dir]  attach a named session in place (create on demand)
  ghostmux ls               list sessions
  ghostmux doctor           diagnose the environment
  ghostmux rail ...         rail internals (once/idle/help) — used by the hub

ghostty extras (optional, one terminal's integration):
  ghostmux ghostty install    wire the unified ctrl+h/j/k/l nav keymap
                              (ghostty splits -> tmux panes -> vim windows)
  ghostmux ghostty uninstall  remove the wiring

Keys inside the hub: press ? for the keymap.
`)
}
