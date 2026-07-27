package tmux

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestViewNamesAreUniquePerPanelAndAttach(t *testing.T) {
	a, b := NewViewNonce(), NewViewNonce()
	if a == "" || b == "" || a == b {
		t.Fatalf("panel nonces are not unique: %q %q", a, b)
	}

	seen := map[string]bool{}
	for _, identity := range []string{
		ViewIdentity(a, 1), ViewIdentity(a, 2), ViewIdentity(b, 1),
	} {
		name := ViewPrefix + identity
		if !strings.HasPrefix(name, "gm-view-") || seen[name] {
			t.Fatalf("invalid or repeated view name %q", name)
		}
		seen[name] = true
	}
}

func TestCreateViewUsesDetachedCreationAndStableSessionID(t *testing.T) {
	orig := Runner
	var calls [][]string
	Runner = func(args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		if args[0] == "new-session" {
			return "$42\n", nil
		}
		return "", nil
	}
	t.Cleanup(func() { Runner = orig })

	ref, err := CreateView("alpha", "7", "panel-1")
	if err != nil {
		t.Fatalf("CreateView: %v", err)
	}
	wantRef := ViewRef{Name: "gm-view-panel-1", SessionID: "$42", Owner: "v1:gm-view-panel-1"}
	if !reflect.DeepEqual(ref, wantRef) {
		t.Fatalf("CreateView ref = %+v, want %+v", ref, wantRef)
	}
	if len(calls) != 4 {
		t.Fatalf("CreateView calls = %v, want create/tag/status/select", calls)
	}
	create := strings.Join(calls[0], " ")
	for _, want := range []string{"new-session -d -P -F #{session_id}", "-s gm-view-panel-1", "-t =alpha"} {
		if !strings.Contains(create, want) {
			t.Errorf("creation missing %q: %q", want, create)
		}
	}
	if strings.Contains(create, " -A ") || strings.HasSuffix(create, " -A") {
		t.Fatalf("creation used forbidden -A: %q", create)
	}
	if got := strings.Join(calls[1], " "); got != "set-option -t $42 @ghostmux_view_owner v1:gm-view-panel-1" {
		t.Fatalf("owner was not the first stable-ID configuration: %q", got)
	}
	for _, call := range calls[1:] {
		joined := strings.Join(call, " ")
		if !strings.Contains(joined, "$42") {
			t.Errorf("post-create command did not target stable ID: %q", joined)
		}
		if strings.Contains(joined, "-t gm-view-panel-1") || strings.Contains(joined, "-t =gm-view-panel-1") {
			t.Errorf("post-create command targeted mutable name: %q", joined)
		}
	}

	argv := AttachViewArgv(ref)
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "if-shell -F -t $42 ") ||
		!strings.Contains(joined, "set-option -t '$42' destroy-unattached on ; attach-session -t '$42'") {
		t.Fatalf("attach argv is not one stable-ID conditional queue: %q", joined)
	}
	if !strings.Contains(joined, ownedViewPredicate(ref)) || strings.Contains(joined, " -A ") {
		t.Fatalf("attach argv lacks exact ownership authorization or uses -A: %q", joined)
	}
}

func TestCreateViewFailureCleanupStartsOnlyAfterTag(t *testing.T) {
	t.Run("owner write failure leaves untagged session", func(t *testing.T) {
		orig := Runner
		var calls []string
		Runner = func(args ...string) (string, error) {
			calls = append(calls, strings.Join(args, " "))
			if args[0] == "new-session" {
				return "$3", nil
			}
			if args[0] == "set-option" && len(args) > 3 && args[3] == viewOwnerOption {
				return "", errors.New("tag failed")
			}
			return "", nil
		}
		t.Cleanup(func() { Runner = orig })

		if _, err := CreateView("alpha", "", "panel-1"); err == nil {
			t.Fatal("CreateView succeeded despite owner write failure")
		}
		for _, call := range calls {
			if strings.HasPrefix(call, "if-shell ") || strings.HasPrefix(call, "kill-session ") {
				t.Fatalf("pre-tag failure attempted automatic cleanup: %v", calls)
			}
		}
	})

	t.Run("post-tag configuration failure returns cleanup capability", func(t *testing.T) {
		orig := Runner
		var calls []string
		Runner = func(args ...string) (string, error) {
			calls = append(calls, strings.Join(args, " "))
			if args[0] == "new-session" {
				return "$9", nil
			}
			if args[0] == "select-window" {
				return "", errors.New("window gone")
			}
			return "", nil
		}
		t.Cleanup(func() { Runner = orig })

		ref, err := CreateView("alpha", "7", "panel-2")
		if err == nil {
			t.Fatal("CreateView succeeded despite select failure")
		}
		want := ViewRef{Name: "gm-view-panel-2", SessionID: "$9", Owner: "v1:gm-view-panel-2"}
		if ref != want {
			t.Fatalf("post-tag failure ref = %+v, want %+v", ref, want)
		}
		for _, call := range calls {
			if strings.HasPrefix(call, "if-shell ") || strings.HasPrefix(call, "kill-session ") {
				t.Fatalf("CreateView consumed its caller's cleanup capability: %v", calls)
			}
		}
	})
}

func TestIsOwnedViewRequiresExactNameBoundMarker(t *testing.T) {
	name := "gm-view-panel-1"
	cases := []struct {
		name    string
		session Session
		want    bool
	}{
		{"valid", Session{Name: name, ViewOwner: "v1:" + name}, true},
		{"untagged legacy", Session{Name: name}, false},
		{"wrong name in marker", Session{Name: name, ViewOwner: "v1:gm-view-other-1"}, false},
		{"wrong version", Session{Name: name, ViewOwner: "v2:" + name}, false},
		{"empty suffix", Session{Name: ViewPrefix, ViewOwner: "v1:" + ViewPrefix}, false},
		{"marker on ordinary session", Session{Name: "alpha", ViewOwner: "v1:alpha"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsOwnedView(tc.session); got != tc.want {
				t.Fatalf("IsOwnedView(%+v) = %v, want %v", tc.session, got, tc.want)
			}
		})
	}
}

func TestOwnedViewCommandsShareOneExactPredicate(t *testing.T) {
	orig := Runner
	var calls [][]string
	Runner = func(args ...string) (string, error) {
		calls = append(calls, append([]string(nil), args...))
		return "", nil
	}
	t.Cleanup(func() { Runner = orig })

	ref := ViewRef{Name: "gm-view-panel-1", SessionID: "$7", Owner: "v1:gm-view-panel-1"}
	attach := AttachViewArgv(ref)
	if err := KillViewIfOwned(ref); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || len(calls[0]) != 7 || !reflect.DeepEqual(calls[0][:4], []string{"if-shell", "-F", "-t", "$7"}) {
		t.Fatalf("cleanup calls = %v, want one if-shell", calls)
	}
	predicate := ownedViewPredicate(ref)
	if calls[0][4] != predicate || !strings.Contains(predicate, "session_name") ||
		!strings.Contains(predicate, ref.Name) || !strings.Contains(predicate, ref.Owner) {
		t.Fatalf("cleanup predicate = %q, want exact name and owner", calls[0][4])
	}
	attachIf := -1
	for i, arg := range attach {
		if arg == "if-shell" {
			attachIf = i
			break
		}
	}
	if attachIf < 0 || len(attach) < attachIf+7 || attach[attachIf+4] != predicate {
		t.Fatalf("attach does not use cleanup's exact predicate: %v", attach)
	}
	if !strings.Contains(attach[attachIf+5], "set-option -t '$7' destroy-unattached on ; attach-session -t '$7'") ||
		attach[attachIf+6] != "" {
		t.Fatalf("attach true/false branches are unsafe: %v", attach)
	}

	calls = nil
	if err := KillViewIfOwned(ViewRef{Name: ref.Name, SessionID: ref.SessionID, Owner: "wrong"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("malformed capability reached tmux: %v", calls)
	}
}

func TestSessionsQueriesViewOwner(t *testing.T) {
	orig := Runner
	Runner = func(args ...string) (string, error) {
		switch args[0] {
		case "list-clients":
			return "gm-view-panel-1\t/dev/pts/1\n", nil
		case "list-sessions":
			return "alpha\t$1\t0\t/tmp\t\n" +
				"gm-view-panel-1\t$2\t1\t/tmp\tv1:gm-view-panel-1\n", nil
		case "list-windows":
			return "alpha\t$1\t@1\t1\tzsh\t1\t0\t0\t100\t\t\n" +
				"gm-view-panel-1\t$2\t@1\t1\tzsh\t1\t0\t0\t100\t\t\n", nil
		case "list-panes":
			return "alpha\t1\tzsh\ngm-view-panel-1\t1\tzsh\n", nil
		default:
			return "", nil
		}
	}
	t.Cleanup(func() { Runner = orig })

	got := Sessions()
	if len(got) != 2 || got[0].ViewOwner != "" || got[1].ViewOwner != "v1:gm-view-panel-1" {
		t.Fatalf("Sessions owner parsing = %+v", got)
	}
}

func TestScratchServerAttachAuthorizationRejectsMutatedViews(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	orig := Runner
	Runner = orig
	t.Cleanup(func() { Runner = orig })
	socket := "gm-view-attach-test-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	t.Setenv("GHOSTMUX_TMUX_ARGS", "-L "+socket+" -f /dev/null")
	t.Cleanup(func() { _ = Run("kill-server") })

	if err := Run("new-session", "-d", "-s", "target"); err != nil {
		t.Fatalf("create scratch target: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(ViewRef) error
	}{
		{
			name: "owner changed",
			mutate: func(ref ViewRef) error {
				return Run("set-option", "-t", ref.SessionID, viewOwnerOption, "v1:someone-else")
			},
		},
		{
			name: "session renamed",
			mutate: func(ref ViewRef) error {
				return Run("rename-session", "-t", ref.SessionID, "renamed-scratch-view")
			},
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref, err := CreateView("target", "", "attach-scratch-"+strconv.Itoa(i))
			if err != nil {
				t.Fatalf("CreateView: %v", err)
			}
			t.Cleanup(func() { _ = Run("kill-session", "-t", ref.SessionID) })
			if err := Run("set-option", "-t", ref.SessionID, "destroy-unattached", "off"); err != nil {
				t.Fatal(err)
			}
			if err := tc.mutate(ref); err != nil {
				t.Fatalf("mutate view: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			argv := AttachViewArgv(ref)
			out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("unauthorized attach blocked instead of exiting as a no-op: %v (%s)", ctx.Err(), out)
			}
			if err != nil {
				t.Fatalf("unauthorized attach no-op: %v (%s)", err, out)
			}
			if err := Run("has-session", "-t", ref.SessionID); err != nil {
				t.Fatal("unauthorized attach destroyed the mutated session")
			}
			setting, err := Runner("show-options", "-v", "-t", ref.SessionID, "destroy-unattached")
			if err != nil {
				t.Fatalf("read destroy-unattached: %v", err)
			}
			if got := strings.TrimSpace(setting); got != "off" {
				t.Fatalf("unauthorized attach changed destroy-unattached to %q", got)
			}
		})
	}
}

func TestScratchServerOwnershipMismatchSurvives(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	orig := Runner
	// Use the real package runner, but isolate every invocation on one scratch
	// server. Package tests do not run in parallel.
	Runner = orig
	t.Cleanup(func() { Runner = orig })
	socket := "gm-view-go-test-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	t.Setenv("GHOSTMUX_TMUX_ARGS", "-L "+socket+" -f /dev/null")
	t.Cleanup(func() { _ = Run("kill-server") })

	if err := Run("new-session", "-d", "-s", "target"); err != nil {
		t.Fatalf("create scratch target: %v", err)
	}
	ref, err := CreateView("target", "", "scratch-1")
	if err != nil {
		t.Fatalf("CreateView on scratch server: %v", err)
	}
	if err := Run("set-option", "-t", ref.SessionID, viewOwnerOption, "mismatch"); err != nil {
		t.Fatal(err)
	}
	if err := KillViewIfOwned(ref); err != nil {
		t.Fatalf("mismatch cleanup command: %v", err)
	}
	if err := Run("has-session", "-t", ref.SessionID); err != nil {
		t.Fatal("ownership mismatch killed the scratch session")
	}

	legacyOut, err := Runner("new-session", "-d", "-P", "-F", "#{session_id}", "-s", "gm-view-legacy")
	if err != nil {
		t.Fatal(err)
	}
	legacy := ViewRef{Name: "gm-view-legacy", SessionID: strings.TrimSpace(legacyOut), Owner: "v1:gm-view-legacy"}
	if err := KillViewIfOwned(legacy); err != nil {
		t.Fatalf("untagged cleanup command: %v", err)
	}
	if err := Run("has-session", "-t", legacy.SessionID); err != nil {
		t.Fatal("untagged legacy session was automatically killed")
	}
}
