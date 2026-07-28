package wiring

import (
	"fmt"
	"strings"
	"testing"

	"github.com/1broseidon/ghostmux/internal/state"
)

type leaseCheck struct {
	label  string
	pass   bool
	detail string
}

func leaseFixture(pid int) string {
	channel := fmt.Sprintf("ghostmux-refresh-v1-%d-000102030405060708090a0b0c0d0e0f", pid)
	command := "run-shell -b \"'tmux' 'wait-for' '-S' '" + channel + "'\""
	hooks := []string{
		"alert-bell", "alert-activity", "session-created", "session-closed",
		"window-linked", "window-unlinked", "window-renamed", "session-window-changed",
	}
	var lines []string
	for i, hook := range hooks {
		lines = append(lines, fmt.Sprintf("%s[%d] %s", hook, i+40, command))
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestReportRefreshLeasesUsesPIDOnlyForActiveStaleReporting(t *testing.T) {
	var checks []leaseCheck
	check := func(label string, pass bool, detail string) {
		checks = append(checks, leaseCheck{label: label, pass: pass, detail: detail})
	}
	reportRefreshLeases(leaseFixture(4242), func(pid int) bool { return pid == 4242 }, check)
	if len(checks) != 1 || !checks[0].pass || checks[0].label != "refresh lease" ||
		!strings.Contains(checks[0].detail, "PID 4242 currently active") ||
		!strings.Contains(checks[0].detail, "8/8 entries") {
		t.Fatalf("active report = %+v", checks)
	}

	checks = nil
	incomplete := strings.ReplaceAll(leaseFixture(4242),
		"window-renamed[46] run-shell -b \"'tmux' 'wait-for' '-S' 'ghostmux-refresh-v1-4242-000102030405060708090a0b0c0d0e0f'\"\n", "")
	reportRefreshLeases(incomplete, func(pid int) bool { return pid == 4242 }, check)
	if len(checks) != 1 || checks[0].pass || checks[0].label != "incomplete refresh lease" ||
		!strings.Contains(checks[0].detail, "7/8 entries") ||
		!strings.Contains(checks[0].detail, "missing window-renamed") {
		t.Fatalf("incomplete active report = %+v", checks)
	}

	checks = nil
	reportRefreshLeases(leaseFixture(4242), func(int) bool { return false }, check)
	if len(checks) != 1 || checks[0].pass || checks[0].label != "stale refresh lease" ||
		!strings.Contains(checks[0].detail, "inspect/unset alert-activity[41]") ||
		!strings.Contains(checks[0].detail, "window-unlinked[45]") {
		t.Fatalf("stale report did not name exact entries: %+v", checks)
	}
}

func TestReportRefreshLeasesIgnoresOccupiedUserIndex(t *testing.T) {
	var checks []leaseCheck
	reportRefreshLeases(
		"alert-bell[133] display-message user-hook\n",
		func(int) bool { return false },
		func(label string, pass bool, detail string) {
			checks = append(checks, leaseCheck{label: label, pass: pass, detail: detail})
		},
	)
	if len(checks) != 1 || !checks[0].pass || checks[0].detail != "none" {
		t.Fatalf("user hook was claimed as a lease: %+v", checks)
	}
}

// TestCmdReachRoundTrip (PROTOTYPE): declare, list, re-declare, remove.
func TestCmdReachRoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	if err := CmdReach([]string{"add", "beastie", "gd@beastie"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := CmdReach([]string{"add", "api", "gd@beastie", "api-work"}); err != nil {
		t.Fatalf("add with session: %v", err)
	}
	store, err := state.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	reach := store.Snapshot().Reach
	if len(reach) != 2 || reach[0].Session != "beastie" || reach[1].Session != "api-work" {
		t.Fatalf("declared targets = %+v", reach)
	}

	// Re-adding a name replaces it rather than duplicating.
	if err := CmdReach([]string{"add", "api", "other-host"}); err != nil {
		t.Fatal(err)
	}
	if err := CmdReach([]string{"rm", "beastie"}); err != nil {
		t.Fatalf("rm: %v", err)
	}
	store2, err := state.Open(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	reach = store2.Snapshot().Reach
	if len(reach) != 1 || reach[0].Name != "api" || reach[0].Host != "other-host" {
		t.Fatalf("after rm/replace = %+v", reach)
	}
	if err := CmdReach([]string{"rm", "ghost-target"}); err == nil {
		t.Fatal("rm of unknown target did not error")
	}
}
