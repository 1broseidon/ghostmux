// ghostmux is the tmux fleet navigator.
package main

import (
	"fmt"
	"os"

	"github.com/1broseidon/ghostmux/internal/app"
	"github.com/1broseidon/ghostmux/internal/rail"
	"github.com/1broseidon/ghostmux/internal/wiring"
)

func main() {
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
	fmt.Print(`ghostmux — the tmux fleet navigator

usage:
  ghostmux             open the interactive panel
  ghostmux doctor      report the detected tmux and panel state
  ghostmux ls          list tmux sessions
  ghostmux up <name>   create or attach a named tmux session
  ghostmux rail once   print one rail frame and exit (debugging)

The panel lists tmux sessions in a rail beside an embedded terminal. tmux
owns session persistence. ghostmux persists groups, fold state, observed
group directories, and user settings.

With the rail focused, ? opens the keymap and , opens settings. The default
rail ⇄ viewport key is ctrl+alt+\ (reported as alt+ctrl+\). A comma-separated
GHOSTMUX_TOGGLE value overrides the saved toggle setting.
`)
}
