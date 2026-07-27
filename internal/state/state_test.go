package state

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func statePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "ghostmux", "groups.json")
}

func writePrimary(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestMissingCreatesVersionOnePrimaryBackupAndPersistentLock(t *testing.T) {
	path := statePath(t)
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if info := store.Info(); info.Status != StatusMissing || info.Exists {
		t.Fatalf("missing info = %+v", info)
	}

	if err := store.Update(func(doc *Document) error {
		doc.Groups = []Group{{Name: "work", Members: []string{"tmux:api"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if info := store.Info(); info.Status != StatusValid || !info.Exists {
		t.Fatalf("post-write info = %+v", info)
	}
	primary := readFile(t, path)
	backup := readFile(t, path+".bak")
	if !bytes.Equal(primary, backup) {
		t.Fatalf("first backup differs from primary\nprimary: %s\nbackup: %s", primary, backup)
	}
	doc, legacy, err := decodeDocument(primary)
	if err != nil || legacy || doc.Version != CurrentVersion {
		t.Fatalf("new primary = %+v legacy=%v err=%v", doc, legacy, err)
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatalf("persistent lock missing: %v", err)
	}
}

func TestVersionOneLoadAndSnapshotAreDeepCopied(t *testing.T) {
	path := statePath(t)
	data := []byte(`{"version":1,"groups":[{"name":"work","members":["tmux:api"]}],"collapsed":["grp:work"],"dirs":{"tmux:api":"/srv"},"settings":{"toggle":["ctrl+j"],"rail_width":42,"agents":["bot"]}}`)
	writePrimary(t, path, data)
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if info := store.Info(); info.Status != StatusValid || info.Version != 1 {
		t.Fatalf("valid info = %+v", info)
	}
	first := store.Snapshot()
	first.Groups[0].Members[0] = "changed"
	first.Collapsed[0] = "changed"
	first.Dirs["tmux:api"] = "changed"
	first.Settings.Toggle[0] = "changed"
	second := store.Snapshot()
	if second.Groups[0].Members[0] != "tmux:api" || second.Collapsed[0] != "grp:work" ||
		second.Dirs["tmux:api"] != "/srv" || second.Settings.Toggle[0] != "ctrl+j" {
		t.Fatalf("Snapshot leaked mutable data: %+v", second)
	}
}

func TestLegacyLoadsAndMigratesLazilyWithExactBackup(t *testing.T) {
	path := statePath(t)
	legacy := []byte("{\n  \"groups\": [{\"name\":\"work\",\"members\":[\"tmux:api\"]}],\n  \"collapsed\": [\"grp:work\"],\n  \"dirs\": {\"tmux:api\":\"/srv\"},\n  \"settings\": {\"rail_width\": 41}\n}\n")
	writePrimary(t, path, legacy)
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if info := store.Info(); info.Status != StatusLegacy || !info.Legacy {
		t.Fatalf("legacy info = %+v", info)
	}
	if got := readFile(t, path); !bytes.Equal(got, legacy) {
		t.Fatal("Open rewrote legacy primary")
	}
	if _, err := os.Stat(path + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open created a backup: %v", err)
	}

	if err := store.Update(func(doc *Document) error {
		doc.Settings.RailWidth = 42
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path+".bak"); !bytes.Equal(got, legacy) {
		t.Fatalf("legacy backup was not byte-exact\nwant: %q\ngot:  %q", legacy, got)
	}
	if _, legacyNow, err := decodeDocument(readFile(t, path)); err != nil || legacyNow {
		t.Fatalf("migrated primary legacy=%v err=%v", legacyNow, err)
	}
}

func TestStrictLoadRefusesInvalidDocumentsWithoutMutation(t *testing.T) {
	tests := []struct {
		name, data, status string
	}{
		{"malformed", `{not json`, StatusCorrupt},
		{"null", `null`, StatusCorrupt},
		{"trailing", `{"groups":[]} {}`, StatusCorrupt},
		{"wrong top type", `[]`, StatusCorrupt},
		{"wrong groups type", `{"groups":{}}`, StatusCorrupt},
		{"wrong collapsed type", `{"groups":[],"collapsed":"x"}`, StatusCorrupt},
		{"wrong dirs type", `{"groups":[],"dirs":[]}`, StatusCorrupt},
		{"wrong settings type", `{"groups":[],"settings":[]}`, StatusCorrupt},
		{"unknown top field", `{"groups":[],"other":1}`, StatusCorrupt},
		{"unknown group field", `{"groups":[{"name":"x","members":[],"other":1}]}`, StatusCorrupt},
		{"unknown settings field", `{"groups":[],"settings":{"other":1}}`, StatusCorrupt},
		{"future version", `{"version":2,"groups":[]}`, StatusUnsupported},
		{"zero version", `{"version":0,"groups":[]}`, StatusUnsupported},
		{"null version", `{"version":null,"groups":[]}`, StatusUnsupported},
		{"string version", `{"version":"1","groups":[]}`, StatusCorrupt},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := statePath(t)
			original := []byte(test.data)
			writePrimary(t, path, original)
			store, err := Open(path)
			if err == nil {
				t.Fatal("Open accepted invalid state")
			}
			if store == nil {
				t.Fatal("Open returned a nil Store on load error")
			}
			if info := store.Info(); info.Status != test.status || !info.Exists {
				t.Fatalf("invalid info = %+v, want status %s", info, test.status)
			}
			called := false
			if updateErr := store.Update(func(*Document) error {
				called = true
				return nil
			}); updateErr == nil {
				t.Fatal("write-blocked Store accepted Update")
			}
			if called {
				t.Fatal("mutation ran against invalid primary")
			}
			if got := readFile(t, path); !bytes.Equal(got, original) {
				t.Fatalf("invalid primary was overwritten: %q", got)
			}
		})
	}
}

func TestDirectoryAndUnreadablePrimaryAreErrors(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		path := statePath(t)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		store, err := Open(path)
		if err == nil || store.Info().Status != StatusUnreadable {
			t.Fatalf("directory Open err=%v info=%+v", err, store.Info())
		}
		if err := store.Update(func(*Document) error { return nil }); err == nil {
			t.Fatal("directory primary was writable through Store")
		}
	})

	t.Run("permissions", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can read mode 000")
		}
		path := statePath(t)
		writePrimary(t, path, []byte(`{"version":1,"groups":[]}`))
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
		store, err := Open(path)
		if err == nil || store.Info().Status != StatusUnreadable {
			t.Fatalf("unreadable Open err=%v info=%+v", err, store.Info())
		}
	})
}

func TestBackupInitializesAndRotatesPreviousPrimary(t *testing.T) {
	path := statePath(t)
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(doc *Document) error {
		doc.Groups = []Group{{Name: "one"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first := readFile(t, path)
	if err := store.Update(func(doc *Document) error {
		doc.Groups = []Group{{Name: "two"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path+".bak"); !bytes.Equal(got, first) {
		t.Fatalf("rotated backup differs from previous primary\nwant: %s\ngot: %s", first, got)
	}
	if bytes.Equal(readFile(t, path), first) {
		t.Fatal("primary did not advance")
	}
}

func TestBackupFailureLeavesPrimaryAndCleansTemps(t *testing.T) {
	path := statePath(t)
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(doc *Document) error {
		doc.Groups = []Group{{Name: "one"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, path)
	if err := os.Remove(path + ".bak"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".bak", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(doc *Document) error {
		doc.Groups = []Group{{Name: "two"}}
		return nil
	}); err == nil {
		t.Fatal("backup path failure did not fail Update")
	}
	if got := readFile(t, path); !bytes.Equal(got, before) {
		t.Fatalf("primary changed after backup failure\nwant: %s\ngot: %s", before, got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("temporary file retained after failure: %s", entry.Name())
		}
	}
}

func TestExistingPrimaryPermissionsArePreserved(t *testing.T) {
	path := statePath(t)
	writePrimary(t, path, []byte(`{"version":1,"groups":[]}`))
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(doc *Document) error {
		doc.Groups = []Group{{Name: "work"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, path + ".bak"} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o640 {
			t.Errorf("%s mode = %o, want 640", candidate, got)
		}
	}
}

func TestTwoStoreConflictAdoptsDiskWithoutRunningMutation(t *testing.T) {
	path := statePath(t)
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Update(func(doc *Document) error {
		doc.Groups = []Group{{Name: "first"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	called := false
	err = stale.Update(func(doc *Document) error {
		called = true
		doc.Groups = []Group{{Name: "stale"}}
		return nil
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Update err=%v, want ErrConflict", err)
	}
	if called {
		t.Fatal("stale mutation ran")
	}
	if got := stale.Snapshot().Groups; len(got) != 1 || got[0].Name != "first" {
		t.Fatalf("stale Store did not adopt disk snapshot: %+v", got)
	}
	if err := stale.Update(func(doc *Document) error {
		doc.Groups = append(doc.Groups, Group{Name: "second"})
		return nil
	}); err != nil {
		t.Fatalf("Update after adoption: %v", err)
	}
}

func TestExactByteRevisionDetectsFormattingOnlyChange(t *testing.T) {
	path := statePath(t)
	original := []byte(`{"version":1,"groups":[]}`)
	writePrimary(t, path, original)
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	writePrimary(t, path, append(original, '\n'))
	called := false
	err = store.Update(func(*Document) error { called = true; return nil })
	if !errors.Is(err, ErrConflict) || called {
		t.Fatalf("format-only change err=%v called=%v", err, called)
	}
}

func TestFileLockTimeoutIsBoundedAndUpdateSucceedsAfterRelease(t *testing.T) {
	path := statePath(t)
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireFileLock(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}

	called := false
	start := time.Now()
	err = store.Update(func(*Document) error {
		called = true
		return nil
	})
	elapsed := time.Since(start)
	if !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("locked Update err=%v, want ErrLockTimeout", err)
	}
	if called {
		t.Fatal("mutation ran without acquiring the lock")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("lock timeout took %s, want at most 500ms", elapsed)
	}
	if !strings.Contains(err.Error(), "busy") {
		t.Fatalf("lock timeout is not recognizable as busy: %v", err)
	}

	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(doc *Document) error {
		doc.Groups = []Group{{Name: "after-lock"}}
		return nil
	}); err != nil {
		t.Fatalf("Update after lock release: %v", err)
	}
	if got := store.Snapshot().Groups; len(got) != 1 || got[0].Name != "after-lock" {
		t.Fatalf("Update after release was not committed: %+v", got)
	}
}

func TestConcurrentReadersAlwaysObserveValidJSON(t *testing.T) {
	path := statePath(t)
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(doc *Document) error { return nil }); err != nil {
		t.Fatal(err)
	}

	var stop atomic.Bool
	errs := make(chan error, 32)
	var readers sync.WaitGroup
	for range 8 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for !stop.Load() {
				data, err := os.ReadFile(path)
				if err != nil {
					errs <- err
					return
				}
				if _, legacy, err := decodeDocument(data); err != nil || legacy {
					errs <- fmt.Errorf("invalid concurrent read legacy=%v: %w", legacy, err)
					return
				}
			}
		}()
	}
	for i := 1; i <= 100; i++ {
		width := i
		if err := store.Update(func(doc *Document) error {
			doc.Settings = &Settings{RailWidth: width}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	stop.Store(true)
	readers.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestMissingPrimaryWithAnyBackupRequiresRecovery(t *testing.T) {
	tests := []struct {
		name         string
		backup       []byte
		backupStatus string
		backupDir    bool
	}{
		{name: "valid", backup: []byte("{\"version\":1,\"groups\":[]}\n"), backupStatus: StatusValid},
		{name: "corrupt", backup: []byte("{only backup"), backupStatus: StatusCorrupt},
		{name: "unsupported", backup: []byte("{\"version\":2,\"groups\":[]}"), backupStatus: StatusUnsupported},
		{name: "unreadable", backupStatus: StatusUnreadable, backupDir: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := statePath(t)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			backupPath := path + ".bak"
			if test.backupDir {
				if err := os.Mkdir(backupPath, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(backupPath, test.backup, 0o600); err != nil {
				t.Fatal(err)
			}

			store, err := Open(path)
			if !errors.Is(err, ErrRecoveryRequired) {
				t.Fatalf("Open err=%v, want ErrRecoveryRequired", err)
			}
			info := store.Info()
			if info.Status != StatusRecoveryRequired || info.Exists || !info.BackupExists || info.BackupStatus != test.backupStatus {
				t.Fatalf("recovery info = %+v", info)
			}
			if !strings.Contains(info.Error, "restore the backup to the primary path") || !strings.Contains(info.Error, "remove the backup deliberately") {
				t.Fatalf("recovery error is not actionable: %q", info.Error)
			}

			called := false
			if updateErr := store.Update(func(*Document) error {
				called = true
				return nil
			}); !errors.Is(updateErr, ErrRecoveryRequired) {
				t.Fatalf("blocked Update err=%v, want ErrRecoveryRequired", updateErr)
			}
			if called {
				t.Fatal("mutation ran while recovery was required")
			}
			if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("recovery created primary: %v", statErr)
			}
			if test.backupDir {
				if fi, statErr := os.Stat(backupPath); statErr != nil || !fi.IsDir() {
					t.Fatalf("unreadable backup path changed: info=%v err=%v", fi, statErr)
				}
			} else if got := readFile(t, backupPath); !bytes.Equal(got, test.backup) {
				t.Fatalf("backup changed during blocked recovery\nwant: %q\ngot:  %q", test.backup, got)
			}
		})
	}
}

func TestInvalidBackupIsReportedButNeverAutoRestored(t *testing.T) {
	path := statePath(t)
	primary := []byte(`{"version":1,"groups":[{"name":"primary","members":[]}]}`)
	writePrimary(t, path, primary)
	if err := os.WriteFile(path+".bak", []byte(`broken`), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if info := store.Info(); info.BackupStatus != StatusCorrupt {
		t.Fatalf("backup info = %+v", info)
	}
	if got := store.Snapshot().Groups[0].Name; got != "primary" {
		t.Fatalf("backup replaced primary snapshot: %q", got)
	}
}

func TestReloadAdoptsOnlyValidPrimaryAndCanClearBlockAfterRepair(t *testing.T) {
	path := statePath(t)
	writePrimary(t, path, []byte(`{"version":1,"groups":[{"name":"one","members":[]}]}`))
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	writePrimary(t, path, []byte(`{"version":1,"groups":[{"name":"two","members":[]}]}`))
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot().Groups[0].Name; got != "two" {
		t.Fatalf("Reload snapshot = %q, want two", got)
	}

	invalid := []byte("{broken")
	writePrimary(t, path, invalid)
	if err := store.Reload(); err == nil {
		t.Fatal("Reload accepted invalid primary")
	}
	if got := store.Snapshot().Groups[0].Name; got != "two" {
		t.Fatalf("failed Reload replaced prior snapshot: %q", got)
	}
	called := false
	if err := store.Update(func(*Document) error { called = true; return nil }); err == nil || called {
		t.Fatalf("blocked Update err=%v called=%v", err, called)
	}
	if got := readFile(t, path); !bytes.Equal(got, invalid) {
		t.Fatalf("blocked Update changed invalid primary: %q", got)
	}

	writePrimary(t, path, []byte(`{"version":1,"groups":[{"name":"repaired","members":[]}]}`))
	if err := store.Reload(); err != nil {
		t.Fatalf("Reload after external repair: %v", err)
	}
	if store.LoadError() != nil || store.Snapshot().Groups[0].Name != "repaired" {
		t.Fatalf("repair was not adopted: err=%v doc=%+v", store.LoadError(), store.Snapshot())
	}
}
