package rail

import (
	"errors"
	"testing"

	"github.com/1broseidon/ghostmux/internal/tmux"
)

func activityWindow(session, id, index string, at int64) tmux.Window {
	return tmux.Window{
		Session: session, SessionID: "$1", WindowID: id, Index: index,
		Name: "shell", Active: true, ActivityAt: at,
	}
}

func TestActivityLedgerBaselineAdvanceAndAcknowledgement(t *testing.T) {
	vp := &fakeViewport{}
	m := railModel{vp: vp}

	baseline := []tmux.Window{activityWindow("alpha", "@7", "1", 100)}
	m.observeActivity(baseline)
	if baseline[0].Activity || m.activity["@7"].unread {
		t.Fatalf("baseline invented unread activity: window=%+v ledger=%+v", baseline[0], m.activity)
	}

	advanced := []tmux.Window{activityWindow("alpha", "@7", "1", 101)}
	m.observeActivity(advanced)
	if !advanced[0].Activity || !m.activity["@7"].unread {
		t.Fatalf("off-view advance was not marked: window=%+v ledger=%+v", advanced[0], m.activity)
	}

	vp.lock = ViewState{Sess: "alpha", Win: "1"}
	viewed := []tmux.Window{activityWindow("alpha", "@7", "1", 102)}
	m.observeActivity(viewed)
	if viewed[0].Activity || m.activity["@7"].unread {
		t.Fatalf("viewed window was not acknowledged: window=%+v ledger=%+v", viewed[0], m.activity)
	}
}

func TestActivityLedgerUsesStableIDsAcrossRenameIndexAndLinks(t *testing.T) {
	vp := &fakeViewport{}
	m := railModel{vp: vp}
	m.observeActivity([]tmux.Window{activityWindow("alpha", "@9", "1", 50)})

	// Session aliases and indices are presentation. The stable window ID carries
	// the high-water mark through either changing.
	renamed := []tmux.Window{activityWindow("renamed", "@9", "4", 51)}
	m.observeActivity(renamed)
	if !renamed[0].Activity {
		t.Fatal("stable ID lost unread state across session/index change")
	}

	// A linked/owned-view duplicate is one ledger observation. Viewing any
	// alias acknowledges the stable window and prevents duplicate artifacts.
	vp.lock = ViewState{Sess: "renamed", Win: "4"}
	linked := []tmux.Window{
		activityWindow("renamed", "@9", "4", 52),
		activityWindow("gm-view-owned", "@9", "1", 52),
	}
	linked[1].SessionID = "$2"
	m.observeActivity(linked)
	if linked[0].Activity || linked[1].Activity || m.activity["@9"].unread || len(m.activity) != 1 {
		t.Fatalf("linked duplicate was not acknowledged once: windows=%+v ledger=%+v", linked, m.activity)
	}
}

func TestActivityLedgerClearsVanishedIDsAndBaselinesReappearance(t *testing.T) {
	m := railModel{vp: &fakeViewport{}}
	m.observeActivity([]tmux.Window{
		activityWindow("alpha", "@1", "1", 10),
		activityWindow("alpha", "@2", "2", 20),
	})
	m.observeActivity([]tmux.Window{activityWindow("alpha", "@2", "2", 21)})
	if _, exists := m.activity["@1"]; exists || len(m.activity) != 1 {
		t.Fatalf("vanished stable ID retained: %+v", m.activity)
	}

	reappeared := []tmux.Window{activityWindow("beta", "@1", "7", 999)}
	m.observeActivity(reappeared)
	if reappeared[0].Activity {
		t.Fatal("newly observed/reappeared ID was not baselined")
	}
}

func TestActivityLedgerHonorsNativeFlag(t *testing.T) {
	vp := &fakeViewport{lock: ViewState{Sess: "alpha", Win: "1"}}
	m := railModel{vp: vp}
	window := activityWindow("alpha", "@3", "1", 10)
	window.Activity = true
	windows := []tmux.Window{window}
	m.observeActivity(windows)
	if !windows[0].Activity || m.activity["@3"].unread {
		t.Fatalf("native activity flag was cleared or became ledger unread: %+v %+v", windows[0], m.activity)
	}
}

func TestActivityLedgerOutageKeepsLastGoodStateStaleSafe(t *testing.T) {
	m := railModel{vp: &fakeViewport{}, collapsed: map[string]bool{}}
	baseline := []tmux.Window{activityWindow("alpha", "@4", "1", 10)}
	m.observeActivity(baseline)
	advanced := []tmux.Window{activityWindow("alpha", "@4", "1", 11)}
	m.observeActivity(advanced)
	m.tmuxCache = tmuxCache{
		enabled: true, hasSnapshot: true, lastErr: errors.New("transport down"),
		snapshot: tmux.Snapshot{
			Sessions: []tmux.Session{{Name: "alpha", SessionID: "$1"}},
			Windows:  advanced,
		},
	}

	before := m.activity["@4"]
	m.rebuildRows() // outage path deliberately does not call observeActivity
	if m.activity["@4"] != before {
		t.Fatalf("outage changed ledger: before=%+v after=%+v", before, m.activity["@4"])
	}
	if len(m.rows) != 1 || m.rows[0].validity != rowStale || m.rows[0].gutter() != "?" {
		t.Fatalf("last-good outage row was not stale-safe: %+v", m.rows)
	}

	recovered := []tmux.Window{activityWindow("alpha", "@4", "1", 12)}
	m.observeActivity(recovered)
	if !recovered[0].Activity {
		t.Fatal("recovery lost activity advanced across outage")
	}
}
