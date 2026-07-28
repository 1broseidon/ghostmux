package wiring

import (
	"fmt"
	"strings"

	"github.com/1broseidon/ghostmux/internal/state"
)

// CmdReach manages declared remote workspaces (PROTOTYPE): tmux sessions on
// hosts reached over ssh, listed in the rail as summonable reach rows. CLI
// only for now — the rail renders and summons them, but declaring happens
// here, the same way the state file is the source of truth for groups.
func CmdReach(args []string) error {
	if len(args) == 0 {
		return reachUsage()
	}
	store, err := state.OpenDefault()
	if err != nil {
		return err
	}
	switch args[0] {
	case "add":
		if len(args) < 3 {
			return reachUsage()
		}
		name, host := strings.TrimSpace(args[1]), strings.TrimSpace(args[2])
		session := name
		if len(args) > 3 {
			session = strings.TrimSpace(args[3])
		}
		if name == "" || host == "" || session == "" {
			return reachUsage()
		}
		return store.Update(func(doc *state.Document) error {
			for i, r := range doc.Reach {
				if r.Name == name {
					doc.Reach[i] = state.ReachTarget{Name: name, Host: host, Session: session}
					return nil
				}
			}
			doc.Reach = append(doc.Reach, state.ReachTarget{Name: name, Host: host, Session: session})
			return nil
		})
	case "rm":
		if len(args) != 2 {
			return reachUsage()
		}
		name := strings.TrimSpace(args[1])
		return store.Update(func(doc *state.Document) error {
			for i, r := range doc.Reach {
				if r.Name == name {
					doc.Reach = append(doc.Reach[:i], doc.Reach[i+1:]...)
					return nil
				}
			}
			return fmt.Errorf("no reach target named %q", name)
		})
	case "ls":
		for _, r := range store.Snapshot().Reach {
			fmt.Printf("%s\t%s\t%s\n", r.Name, r.Host, r.Session)
		}
		return nil
	}
	return reachUsage()
}

func reachUsage() error {
	return fmt.Errorf("usage: ghostmux reach add <name> <host> [session] | rm <name> | ls")
}
