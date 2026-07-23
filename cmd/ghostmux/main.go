// ghostmux: the coordination layer for the ghostty/tmux boundary.
//
// The problem: ghostty and tmux each stop dead at the other's boundary. tmux
// can't reach up (spawn terminal windows, yield keys, see splits); ghostty
// can't reach down (sessions, persistence, remote). Every feature here must
// span that boundary — anything tmux or ghostty could do alone is out of
// scope, demoted to a convenience at best.
//
// Boundary commands:
//   - install/uninstall: matched-pair config for both sides of the seam
//     (one nav keymap across ghostty splits, tmux panes, vim windows)
//   - restore: reopen a ghostty window for every orphaned tmux session
//   - up -w:   ghostty window attached to a named session (create on demand)
//   - hub:     the rail+viewport coordination surface for a fleet of sessions
//   - doctor:  diagnose the seam (terminfo, wiring, versions)
//
// Conveniences (fail the purist test, cost nothing):
//   - up (in place), ls
package main

import (
	"fmt"
	"os"

	"github.com/1broseidon/ghostmux/internal/rail"
	"github.com/1broseidon/ghostmux/internal/wiring"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "install":
		err = wiring.CmdInstall()
	case "uninstall":
		err = wiring.CmdUninstall()
	case "up":
		err = wiring.CmdUp(os.Args[2:])
	case "restore":
		err = wiring.CmdRestore()
	case "rail":
		err = rail.CmdRail(os.Args[2:])
	case "hub":
		err = wiring.CmdHub(os.Args[2:])
	case "shell":
		err = wiring.CmdShell()
	case "ambient":
		err = wiring.CmdAmbient(os.Args[2:])
	case "ls":
		err = wiring.CmdLs()
	case "doctor":
		err = wiring.CmdDoctor()
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "ghostmux: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ghostmux:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`ghostmux — the coordination layer for the ghostty/tmux boundary

Your terminal and your multiplexer each stop dead at the other's boundary;
your workflow doesn't. Everything here spans that boundary.

ghostmux hub is the entry point: run it and stay there. It creates (or
attaches to) the dedicated hub session — a persistent rail pane (session/
window tree, live attention gutter) beside a viewport pane that jumps to
whatever you select.

boundary:
  ghostmux hub              persistent rail+viewport hub — start here
  ghostmux ambient on|off   every ghostty surface becomes a
                            persistent tmux session; reopening ghostty
                            unfolds your whole workspace, zero typing
  ghostmux install          matched-pair nav config: one keymap across
                            ghostty splits, tmux panes, and vim windows
  ghostmux uninstall        remove the wiring (snippets stay in ~/.config/ghostmux)
  ghostmux restore          reopen a ghostty window for every orphaned
                            (unattached) tmux session
  ghostmux up -w <name>     ghostty window attached to session <name>,
                            created on demand
  ghostmux doctor           diagnose the seam: terminfo, wiring, hub layout
  ghostmux shell            what ambient mode runs per surface (internal)

convenience:
  ghostmux up <name> [dir]  attach in place (switches client inside tmux)
  ghostmux ls               list tmux sessions

internals (used by the hub; not for direct use):
  ghostmux rail              run the rail TUI in the current pane
  ghostmux rail once         print one frame and exit (debugging / agents)
  ghostmux rail idle         render the viewport idle placeholder
  ghostmux rail help         print the keymap (used by the ? popup)
`)
}
