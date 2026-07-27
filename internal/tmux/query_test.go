package tmux

import (
	"errors"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type queryResult struct {
	out string
	err error
}

func useQueryResults(t *testing.T, results map[string]queryResult) *[]string {
	t.Helper()
	orig := Runner
	calls := []string{}
	Runner = func(args ...string) (string, error) {
		calls = append(calls, args[0])
		result, ok := results[args[0]]
		if !ok {
			return "", nil
		}
		return result.out, result.err
	}
	t.Cleanup(func() { Runner = orig })
	return &calls
}

func useQueryAttempts(t *testing.T, attempts ...map[string]queryResult) *[]string {
	t.Helper()
	orig := Runner
	calls := []string{}
	candidate := 0
	Runner = func(args ...string) (string, error) {
		command := args[0]
		calls = append(calls, command)
		if candidate >= len(attempts) {
			return "", errors.New("unexpected extra candidate")
		}
		result := attempts[candidate][command]
		if command == "list-panes" {
			candidate++
		}
		return result.out, result.err
	}
	t.Cleanup(func() { Runner = orig })
	return &calls
}

func validQueryResults() map[string]queryResult {
	return map[string]queryResult{
		"list-sessions": {out: "alpha\t$1\t1\t/tmp\t\n"},
		"list-clients":  {out: "alpha\t/dev/pts/7\n"},
		"list-windows":  {out: "alpha\t$1\t@1\t1\tzsh\t1\t0\t0\t100\t\t\n"},
		"list-panes":    {out: "alpha\t1\tzsh\n"},
	}
}

func cloneQueryResults(in map[string]queryResult) map[string]queryResult {
	out := make(map[string]queryResult, len(in))
	for command, result := range in {
		out[command] = result
	}
	return out
}

func TestQuerySnapshotRejectsEveryPartialCommandFailureWithoutRetry(t *testing.T) {
	for _, command := range []string{"list-sessions", "list-clients", "list-windows", "list-panes"} {
		t.Run(command, func(t *testing.T) {
			results := validQueryResults()
			results[command] = queryResult{err: errors.New("backend broke")}
			calls := useQueryResults(t, results)

			snapshot, err := QuerySnapshot()
			if err == nil || !strings.Contains(err.Error(), command) {
				t.Fatalf("QuerySnapshot error = %v, want %s failure", err, command)
			}
			if len(snapshot.Sessions) != 0 || len(snapshot.Windows) != 0 {
				t.Fatalf("partial candidate escaped: %+v", snapshot)
			}
			count := 0
			for _, call := range *calls {
				if call == "list-sessions" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("command failure retried candidate: %v", *calls)
			}
		})
	}
}

func TestQuerySnapshotRejectsMalformedNonemptyRowsWithoutRetry(t *testing.T) {
	cases := []struct {
		name, command, output string
	}{
		{"short session", "list-sessions", "alpha\t$1\t0\t/tmp\n"},
		{"bad session id", "list-sessions", "alpha\talpha\t0\t/tmp\t\n"},
		{"bad attached count", "list-sessions", "alpha\t$1\tmany\t/tmp\t\n"},
		{"short client", "list-clients", "alpha\n"},
		{"short window", "list-windows", "alpha\t$1\t@1\t1\tzsh\t1\t0\t0\t100\n"},
		{"bad session id", "list-windows", "alpha\talpha\t@1\t1\tzsh\t1\t0\t0\t100\t\n"},
		{"bad stable window id", "list-windows", "alpha\t$1\t1\t1\tzsh\t1\t0\t0\t100\t\n"},
		{"bad window index", "list-windows", "alpha\t$1\t@1\tmain\tzsh\t1\t0\t0\t100\t\n"},
		{"bad window flag", "list-windows", "alpha\t$1\t@1\t1\tzsh\tyes\t0\t0\t100\t\n"},
		{"bad activity timestamp", "list-windows", "alpha\t$1\t@1\t1\tzsh\t1\t0\t0\tsoon\t\n"},
		{"negative activity timestamp", "list-windows", "alpha\t$1\t@1\t1\tzsh\t1\t0\t0\t-1\t\n"},
		{"bad done flag", "list-windows", "alpha\t$1\t@1\t1\tzsh\t1\t0\t0\t100\ttrue\t\n"},
		{"short pane", "list-panes", "alpha\t1\n"},
		{"bad pane index", "list-panes", "alpha\tmain\tzsh\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results := validQueryResults()
			results[tc.command] = queryResult{out: tc.output}
			calls := useQueryResults(t, results)
			if _, err := QuerySnapshot(); err == nil || !strings.Contains(err.Error(), "malformed row") {
				t.Fatalf("QuerySnapshot error = %v, want malformed row", err)
			}
			count := 0
			for _, call := range *calls {
				if call == "list-sessions" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("parse failure retried candidate: %v", *calls)
			}
		})
	}
}

func TestQuerySnapshotPreservesTrailingEmptyFieldsAndInstanceIDs(t *testing.T) {
	results := validQueryResults()
	results["list-sessions"] = queryResult{out: "alpha\t$1\t0\t\t\nbeta\t$2\t2\t/srv/beta\tv1:beta\n"}
	results["list-clients"] = queryResult{out: "beta\t/dev/pts/9\nbeta\t/dev/pts/10\n"}
	results["list-windows"] = queryResult{out: "alpha\t$1\t@1\t1\tshell\t1\t0\t0\t101\t\t/tmp/alpha\n" +
		"beta\t$2\t@2\t3\tagent\t1\t0\t0\t202\t1\t/srv/beta/cwd\n"}
	results["list-panes"] = queryResult{out: "alpha\t1\t\n" + "beta\t3\tclaude\n"}
	useQueryResults(t, results)

	snapshot, err := QuerySnapshot()
	if err != nil {
		t.Fatal(err)
	}
	wantSessions := []Session{
		{Name: "alpha", SessionID: "$1", CurrentPath: "/tmp/alpha"},
		{Name: "beta", SessionID: "$2", Attached: true, Clients: 2, ClientTTY: "/dev/pts/9", Path: "/srv/beta", CurrentPath: "/srv/beta/cwd", ViewOwner: "v1:beta"},
	}
	if !reflect.DeepEqual(snapshot.Sessions, wantSessions) {
		t.Fatalf("sessions = %+v, want %+v", snapshot.Sessions, wantSessions)
	}
	if got := snapshot.Windows[0]; got.SessionID != "$1" || got.WindowID != "@1" || got.ActivityAt != 101 || got.Done || got.PanePath != "/tmp/alpha" || !reflect.DeepEqual(got.PaneCmds, []string{""}) {
		t.Fatalf("trailing done/pane fields or ID were lost: %+v", got)
	}
	if got := snapshot.Windows[1]; got.SessionID != "$2" || got.WindowID != "@2" || got.ActivityAt != 202 || !got.Done || got.PanePath != "/srv/beta/cwd" || !reflect.DeepEqual(got.PaneCmds, []string{"claude"}) {
		t.Fatalf("nonempty done/pane fields parsed incorrectly: %+v", got)
	}
}

func TestQuerySnapshotRetriesCompleteCandidateOnRelationshipInconsistency(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]queryResult)
	}{
		{"duplicate session name", func(r map[string]queryResult) {
			r["list-sessions"] = queryResult{out: "alpha\t$1\t1\t/tmp\t\nalpha\t$2\t0\t/tmp\t\n"}
		}},
		{"duplicate session id", func(r map[string]queryResult) {
			r["list-sessions"] = queryResult{out: "alpha\t$1\t1\t/tmp\t\nbeta\t$1\t0\t/tmp\t\n"}
		}},
		{"unknown client session", func(r map[string]queryResult) {
			r["list-clients"] = queryResult{out: "gone\t/dev/pts/7\n"}
		}},
		{"missing expected client", func(r map[string]queryResult) {
			r["list-clients"] = queryResult{}
		}},
		{"unknown window session", func(r map[string]queryResult) {
			r["list-windows"] = queryResult{out: "gone\t$9\t@9\t1\tzsh\t1\t0\t0\t100\t\t\n"}
		}},
		{"session replacement between rows", func(r map[string]queryResult) {
			r["list-windows"] = queryResult{out: "alpha\t$2\t@1\t1\tzsh\t1\t0\t0\t100\t\t\n"}
		}},
		{"session missing windows", func(r map[string]queryResult) {
			r["list-windows"] = queryResult{}
		}},
		{"duplicate window key", func(r map[string]queryResult) {
			r["list-windows"] = queryResult{out: "alpha\t$1\t@1\t1\tzsh\t1\t0\t0\t100\t\t\nalpha\t$1\t@1\t1\tvim\t0\t0\t0\t100\t\t\n"}
		}},
		{"pane references unknown window", func(r map[string]queryResult) {
			r["list-panes"] = queryResult{out: "alpha\t9\tzsh\n"}
		}},
		{"window missing panes", func(r map[string]queryResult) {
			r["list-panes"] = queryResult{}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := cloneQueryResults(validQueryResults())
			tc.mutate(first)
			calls := useQueryAttempts(t, first, validQueryResults())
			snapshot, err := QuerySnapshot()
			if err != nil || len(snapshot.Sessions) != 1 || snapshot.Sessions[0].SessionID != "$1" {
				t.Fatalf("retry result = %+v, %v", snapshot, err)
			}
			want := []string{
				"list-sessions", "list-clients", "list-windows", "list-panes",
				"list-sessions", "list-clients", "list-windows", "list-panes",
			}
			if !reflect.DeepEqual(*calls, want) {
				t.Fatalf("retry calls = %v, want %v", *calls, want)
			}
		})
	}
}

func TestQuerySnapshotRejectsSecondInconsistentCandidate(t *testing.T) {
	bad := validQueryResults()
	bad["list-windows"] = queryResult{out: "alpha\t$2\t@1\t1\tzsh\t1\t0\t0\t100\t\t\n"}
	calls := useQueryAttempts(t, bad, bad)
	snapshot, err := QuerySnapshot()
	if err == nil || !strings.Contains(err.Error(), "snapshot inconsistent") || len(snapshot.Sessions) != 0 {
		t.Fatalf("second inconsistent result = %+v, %v", snapshot, err)
	}
	if len(*calls) != 8 {
		t.Fatalf("inconsistent retry count = %v", *calls)
	}
}

func TestQuerySnapshotValidEmptyAndNoServer(t *testing.T) {
	t.Run("running empty backend", func(t *testing.T) {
		calls := useQueryResults(t, map[string]queryResult{})
		snapshot, err := QuerySnapshot()
		if err != nil || len(snapshot.Sessions) != 0 || len(snapshot.Windows) != 0 {
			t.Fatalf("empty snapshot = %+v, err=%v", snapshot, err)
		}
		want := []string{"list-sessions", "list-clients", "list-windows", "list-panes"}
		if !reflect.DeepEqual(*calls, want) {
			t.Fatalf("empty query calls = %v, want %v", *calls, want)
		}
	})

	for _, diagnostic := range []string{
		"no server running on /tmp/tmux-1000/default",
		"error connecting to /tmp/tmux-1000/test (No such file or directory)",
	} {
		t.Run(diagnostic, func(t *testing.T) {
			calls := useQueryResults(t, map[string]queryResult{
				"list-sessions": {err: errors.New(diagnostic)},
			})
			snapshot, err := QuerySnapshot()
			if err != nil || len(snapshot.Sessions) != 0 || len(snapshot.Windows) != 0 {
				t.Fatalf("no-server snapshot = %+v, err=%v", snapshot, err)
			}
			if !reflect.DeepEqual(*calls, []string{"list-sessions"}) {
				t.Fatalf("no-server query continued into partial calls: %v", *calls)
			}
		})
	}
}

func TestQuerySnapshotDoesNotTreatUnknownErrorAsEmpty(t *testing.T) {
	useQueryResults(t, map[string]queryResult{
		"list-sessions": {err: errors.New("permission denied")},
	})
	if _, err := QuerySnapshot(); err == nil {
		t.Fatal("unknown list-sessions error became an empty snapshot")
	}
}

func TestKillSessionIfInstanceIsAtomicAndReportsMismatch(t *testing.T) {
	orig := Runner
	t.Cleanup(func() { Runner = orig })

	t.Run("matching predicate kills stable id", func(t *testing.T) {
		var got []string
		Runner = func(args ...string) (string, error) {
			got = append([]string(nil), args...)
			return "", nil
		}
		killed, err := KillSessionIfInstance("api", "$7")
		if err != nil || !killed {
			t.Fatalf("KillSessionIfInstance = (%v, %v)", killed, err)
		}
		joined := strings.Join(got, " ")
		if len(got) < 7 || got[0] != "if-shell" || got[3] != "$7" ||
			!strings.Contains(joined, "#{session_name},api") ||
			!strings.Contains(joined, "#{session_id},$7") ||
			!strings.Contains(joined, "kill-session -t '$7'") {
			t.Fatalf("atomic kill command = %v", got)
		}
	})

	t.Run("false predicate cancels", func(t *testing.T) {
		Runner = func(args ...string) (string, error) {
			return instanceMismatchMarker + "\n", nil
		}
		killed, err := KillSessionIfInstance("api", "$7")
		if err != nil || killed {
			t.Fatalf("mismatch = (%v, %v)", killed, err)
		}
	})
}

func TestScratchServerKillSessionIfInstance(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	orig := Runner
	Runner = orig
	t.Cleanup(func() { Runner = orig })
	socket := "gm-instance-kill-test-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	t.Setenv("GHOSTMUX_TMUX_ARGS", "-L "+socket+" -f /dev/null")
	t.Cleanup(func() { _ = Run("kill-server") })

	if err := Run("new-session", "-d", "-s", "api"); err != nil {
		t.Fatalf("create scratch session: %v", err)
	}
	present, id, err := ProbeSessionInstance("api")
	if err != nil || !present || id == "" {
		t.Fatalf("probe scratch instance = (%v, %q, %v)", present, id, err)
	}
	if err := Run("rename-session", "-t", id, "replacement"); err != nil {
		t.Fatal(err)
	}
	killed, err := KillSessionIfInstance("api", id)
	if err != nil || killed {
		t.Fatalf("renamed instance mismatch = (%v, %v)", killed, err)
	}
	if err := Run("has-session", "-t", id); err != nil {
		t.Fatalf("mismatch killed stable instance: %v", err)
	}

	if err := Run("rename-session", "-t", id, "api"); err != nil {
		t.Fatal(err)
	}
	killed, err = KillSessionIfInstance("api", id)
	if err != nil || !killed {
		t.Fatalf("matching scratch kill = (%v, %v)", killed, err)
	}
	if err := Run("has-session", "-t", id); err == nil {
		t.Fatal("matching instance survived kill")
	}
}

func TestProbeSessionInstanceIsExactAndReturnsStableID(t *testing.T) {
	t.Run("present exact", func(t *testing.T) {
		results := validQueryResults()
		results["list-sessions"] = queryResult{out: "alpha\t$1\t1\t/tmp\t\nalpha-long\t$2\t0\t/tmp\t\n"}
		results["list-windows"] = queryResult{out: "alpha\t$1\t@1\t1\tzsh\t1\t0\t0\t100\t\t\nalpha-long\t$2\t@2\t1\tzsh\t1\t0\t0\t100\t\t\n"}
		results["list-panes"] = queryResult{out: "alpha\t1\tzsh\nalpha-long\t1\tzsh\n"}
		useQueryResults(t, results)
		present, id, err := ProbeSessionInstance("alpha")
		if err != nil || !present || id != "$1" {
			t.Fatalf("ProbeSessionInstance = (%v, %q, %v)", present, id, err)
		}
	})

	t.Run("authoritatively absent", func(t *testing.T) {
		useQueryResults(t, map[string]queryResult{})
		present, id, err := ProbeSessionInstance("alpha")
		if err != nil || present || id != "" {
			t.Fatalf("ProbeSessionInstance absent = (%v, %q, %v)", present, id, err)
		}
	})

	t.Run("backend failure", func(t *testing.T) {
		results := validQueryResults()
		results["list-clients"] = queryResult{err: errors.New("backend broke")}
		useQueryResults(t, results)
		present, id, err := ProbeSessionInstance("alpha")
		if err == nil || present || id != "" {
			t.Fatalf("ProbeSessionInstance outage = (%v, %q, %v)", present, id, err)
		}
	})
}
