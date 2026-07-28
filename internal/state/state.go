// Package state owns ghostmux's versioned on-disk state and its concurrency
// guarantees. Callers mutate a Store; they never write the document directly.
package state

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const CurrentVersion = 1

var (
	// ErrConflict is returned when the primary changed after a Store loaded it.
	// The Store adopts the current valid primary before returning this error.
	ErrConflict = errors.New("state revision conflict")

	// ErrUnsupportedVersion identifies an explicit schema version this build
	// cannot read. Such a primary is never replaced.
	ErrUnsupportedVersion = errors.New("unsupported state version")

	// ErrLockTimeout is returned when another process keeps the state lock busy
	// for the complete bounded acquisition window.
	ErrLockTimeout = errors.New("state lock timeout")

	// ErrLockUnsupported is returned on platforms where state writes cannot be
	// protected by the required process lock.
	ErrLockUnsupported = errors.New("state locking is unsupported on this platform")

	// ErrRecoveryRequired is returned when the primary is missing while a
	// backup path still exists. Writes stay blocked to protect that backup.
	ErrRecoveryRequired = errors.New("state recovery required")
)

const (
	StatusMissing          = "missing"
	StatusValid            = "valid"
	StatusLegacy           = "legacy"
	StatusCorrupt          = "corrupt"
	StatusUnreadable       = "unreadable"
	StatusUnsupported      = "unsupported"
	StatusRecoveryRequired = "recovery required"
)

// Group is an ordered named set of backend-qualified session keys.
type Group struct {
	Name    string   `json:"name"`
	Members []string `json:"members"`
}

// Settings contains only explicit user choices. Zero fields mean defaults.
type Settings struct {
	Toggle    []string `json:"toggle,omitempty"`
	RailWidth int      `json:"rail_width,omitempty"`
	Agents    []string `json:"agents,omitempty"`
	// GhostDir is which tmux path ghosts remember: "" (launch directory /
	// #{session_path}) or "last" (active pane #{pane_current_path}).
	GhostDir string `json:"ghost_dir,omitempty"`
	// CreateDir is where `n` starts a new tmux session: "" (home) or
	// "current" (the viewport session's active pane cwd).
	CreateDir string `json:"create_dir,omitempty"`
}

// Empty reports whether no setting has been explicitly saved.
func (s Settings) Empty() bool {
	return len(s.Toggle) == 0 && s.RailWidth == 0 && len(s.Agents) == 0 &&
		s.GhostDir == "" && s.CreateDir == ""
}

// Document is the complete state file. Version is set to CurrentVersion in
// memory even when an unversioned legacy document was loaded.
//
// Extra carries top-level keys this build does not know — typically written
// by a newer (or reverted-away) build. They are never interpreted and never
// discarded: a save writes them back exactly, so switching binaries cannot
// destroy another version's state. Unknown fields NESTED inside known keys
// are tolerated on load but not preserved; only malformed JSON or a known
// key with the wrong shape is corrupt.
type Document struct {
	Version   int               `json:"version"`
	Groups    []Group           `json:"groups"`
	Collapsed []string          `json:"collapsed,omitempty"`
	Dirs      map[string]string `json:"dirs,omitempty"`
	Settings  *Settings         `json:"settings,omitempty"`

	Extra map[string]json.RawMessage `json:"-"`
}

// MarshalJSON writes the known fields and then the preserved unknown keys,
// so a document loaded from a newer build's file round-trips losslessly.
func (d Document) MarshalJSON() ([]byte, error) {
	type known Document // no methods: avoids recursion
	base, err := json.Marshal(known(d))
	if err != nil {
		return nil, err
	}
	if len(d.Extra) == 0 {
		return base, nil
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	for key, raw := range d.Extra {
		if _, taken := merged[key]; !taken {
			merged[key] = raw
		}
	}
	return json.Marshal(merged)
}

// Info describes the exact primary snapshot held by a Store and the backup
// observed with it. Backups are never loaded in place of the primary; their
// presence can require explicit recovery when the primary is missing.
type Info struct {
	Path      string
	Status    string
	Exists    bool
	Legacy    bool
	Version   int
	Error     string
	Groups    int
	Members   int
	Dirs      int
	Collapsed int
	ModTime   time.Time

	BackupPath    string
	BackupStatus  string
	BackupExists  bool
	BackupLegacy  bool
	BackupVersion int
	BackupError   string
	BackupModTime time.Time
}

type revision struct {
	exists bool
	sum    [sha256.Size]byte
}

// Store holds one validated state snapshot and its exact-byte revision.
type Store struct {
	mu      sync.Mutex
	path    string
	doc     Document
	rev     revision
	info    Info
	blocked error
}

// DefaultPath returns $XDG_STATE_HOME/ghostmux/groups.json, or the existing
// $HOME/.local/state fallback. An empty path means no home could be resolved.
func DefaultPath() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "ghostmux", "groups.json")
}

// OpenDefault opens the default state path. The returned Store is always
// non-nil, including on load failure, so the panel can run read-only.
func OpenDefault() (*Store, error) { return Open(DefaultPath()) }

// Open strictly loads path. Only a missing primary with no backup produces an
// empty writable Store. Every other read, decode, or recovery condition returns
// a write-blocked Store and the error; existing bytes are retained exactly.
func Open(path string) (*Store, error) {
	s := &Store{path: path, doc: emptyDocument()}
	if path == "" {
		err := fmt.Errorf("state path is unavailable")
		s.blocked = err
		s.info = Info{Path: path, Status: StatusUnreadable, Error: err.Error()}
		return s, err
	}

	doc, rev, info, err := loadPrimary(path)
	s.info = info
	if err != nil {
		s.blocked = err
		return s, err
	}
	s.doc, s.rev = doc, rev
	return s, nil
}

// Path returns the Store's primary path.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Snapshot returns a deep copy of the Store's validated document.
func (s *Store) Snapshot() Document {
	if s == nil {
		return emptyDocument()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneDocument(s.doc)
}

// Info returns a copy of the facts recorded with the current snapshot.
func (s *Store) Info() Info {
	if s == nil {
		return Info{Status: StatusUnreadable, Error: "state store is unavailable"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// LoadError returns the error currently preventing writes, if any.
func (s *Store) LoadError() error {
	if s == nil {
		return fmt.Errorf("state store is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blocked
}

// Reload replaces the snapshot only with a valid current primary. A failed
// reload retains the prior snapshot and blocks writes until a later successful
// explicit reload.
func (s *Store) Reload() error {
	if s == nil {
		return fmt.Errorf("state store is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		err := fmt.Errorf("state path is unavailable")
		s.blocked = err
		return err
	}

	doc, rev, info, err := loadPrimary(s.path)
	s.info = info
	if err != nil {
		s.blocked = err
		return err
	}
	s.doc, s.rev, s.blocked = doc, rev, nil
	return nil
}

// Update applies mutate to a deep-copy candidate and commits it atomically.
// Under the process lock it first validates and compares the exact primary
// bytes. On a stale revision, mutate is not called; the current valid primary
// is adopted and ErrConflict is returned.
func (s *Store) Update(mutate func(*Document) error) error {
	if s == nil {
		return fmt.Errorf("state store is unavailable")
	}
	if mutate == nil {
		return fmt.Errorf("state mutation is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blocked != nil {
		return s.blocked
	}
	if s.path == "" {
		return fmt.Errorf("state path is unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}

	lock, err := acquireFileLock(s.path + ".lock")
	if err != nil {
		return fmt.Errorf("lock state: %w", err)
	}
	defer lock.Close()

	current, currentRev, currentInfo, err := loadPrimary(s.path)
	if err != nil {
		// A primary that became invalid after Open must not be replaced.
		s.info, s.blocked = currentInfo, err
		return err
	}
	if currentRev != s.rev {
		s.doc, s.rev, s.info = current, currentRev, currentInfo
		return fmt.Errorf("%w: primary changed", ErrConflict)
	}

	candidate := cloneDocument(s.doc)
	if err := mutate(&candidate); err != nil {
		return err
	}
	candidate.Version = CurrentVersion
	if candidate.Settings != nil && candidate.Settings.Empty() {
		candidate.Settings = nil
	}
	encoded, err := json.MarshalIndent(candidate, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, _, err := decodeDocument(encoded); err != nil {
		return fmt.Errorf("validate state candidate: %w", err)
	}

	mode := os.FileMode(0o644)
	if currentRev.exists {
		if fi, statErr := os.Stat(s.path); statErr == nil {
			mode = fi.Mode().Perm()
		}
	}
	backupBytes := encoded
	if currentRev.exists {
		backupBytes, err = os.ReadFile(s.path)
		if err != nil {
			return fmt.Errorf("re-read state for backup: %w", err)
		}
		if got := makeRevision(backupBytes, true); got != currentRev {
			// A writer that ignores the lock must still be detected.
			doc, rev, info, loadErr := loadPrimary(s.path)
			if loadErr == nil {
				s.doc, s.rev, s.info = doc, rev, info
				return fmt.Errorf("%w: primary changed while locked", ErrConflict)
			}
			s.info, s.blocked = info, loadErr
			return loadErr
		}
	}

	backupPath := s.path + ".bak"
	if err := atomicWriteFile(backupPath, backupBytes, mode); err != nil {
		return fmt.Errorf("write state backup: %w", err)
	}
	if err := atomicWriteFile(s.path, encoded, mode); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	bestEffortSyncDir(filepath.Dir(s.path))

	s.doc = cloneDocument(candidate)
	s.rev = makeRevision(encoded, true)
	s.info = infoFromDocument(s.path, candidate, false, StatusValid, "")
	s.info.Exists = true
	if fi, statErr := os.Stat(s.path); statErr == nil {
		s.info.ModTime = fi.ModTime()
	}
	addBackupInfo(&s.info)
	return nil
}

func emptyDocument() Document { return Document{Version: CurrentVersion} }

func cloneDocument(in Document) Document {
	out := in
	out.Groups = make([]Group, len(in.Groups))
	for i, g := range in.Groups {
		out.Groups[i] = g
		out.Groups[i].Members = append([]string(nil), g.Members...)
	}
	out.Collapsed = append([]string(nil), in.Collapsed...)
	if in.Dirs != nil {
		out.Dirs = make(map[string]string, len(in.Dirs))
		for k, v := range in.Dirs {
			out.Dirs[k] = v
		}
	}
	if in.Settings != nil {
		settings := *in.Settings
		settings.Toggle = append([]string(nil), in.Settings.Toggle...)
		settings.Agents = append([]string(nil), in.Settings.Agents...)
		out.Settings = &settings
	}
	return out
}

func loadPrimary(path string) (Document, revision, Info, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			info := infoFromDocument(path, emptyDocument(), false, StatusMissing, "")
			addBackupInfo(&info)
			if info.BackupExists {
				recoveryErr := fmt.Errorf(
					"%w: primary %q is missing while backup %q exists; restore the backup to the primary path or remove the backup deliberately before saving",
					ErrRecoveryRequired, path, info.BackupPath,
				)
				info.Status, info.Error = StatusRecoveryRequired, recoveryErr.Error()
				return emptyDocument(), revision{}, info, recoveryErr
			}
			return emptyDocument(), revision{}, info, nil
		}
		info := infoFromDocument(path, emptyDocument(), false, StatusUnreadable, err.Error())
		if fi, statErr := os.Stat(path); statErr == nil {
			info.Exists, info.ModTime = true, fi.ModTime()
		}
		addBackupInfo(&info)
		return emptyDocument(), revision{}, info, fmt.Errorf("read state %q: %w", path, err)
	}

	doc, legacy, err := decodeDocument(b)
	if err != nil {
		status := StatusCorrupt
		if errors.Is(err, ErrUnsupportedVersion) {
			status = StatusUnsupported
		}
		info := infoFromDocument(path, emptyDocument(), false, status, err.Error())
		info.Exists = true
		if fi, statErr := os.Stat(path); statErr == nil {
			info.ModTime = fi.ModTime()
		}
		addBackupInfo(&info)
		return emptyDocument(), revision{}, info, fmt.Errorf("load state %q: %w", path, err)
	}
	status := StatusValid
	if legacy {
		status = StatusLegacy
	}
	info := infoFromDocument(path, doc, legacy, status, "")
	info.Exists = true
	if fi, statErr := os.Stat(path); statErr == nil {
		info.ModTime = fi.ModTime()
	}
	addBackupInfo(&info)
	return doc, makeRevision(b, true), info, nil
}

func decodeDocument(b []byte) (Document, bool, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	var fields map[string]json.RawMessage
	if err := dec.Decode(&fields); err != nil {
		return Document{}, false, fmt.Errorf("decode JSON: %w", err)
	}
	if fields == nil {
		return Document{}, false, fmt.Errorf("state document must be a JSON object")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Document{}, false, fmt.Errorf("trailing JSON data")
		}
		return Document{}, false, fmt.Errorf("trailing JSON data: %w", err)
	}

	known := map[string]bool{
		"version": true, "groups": true, "collapsed": true, "dirs": true, "settings": true,
	}
	var preserved map[string]json.RawMessage
	for key, raw := range fields {
		if !known[key] {
			// A key this build does not know is another build's state, not
			// corruption. Preserve it verbatim so a save cannot destroy it.
			if preserved == nil {
				preserved = map[string]json.RawMessage{}
			}
			preserved[key] = raw
		}
	}

	legacy := true
	version := CurrentVersion
	if raw, ok := fields["version"]; ok {
		legacy = false
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return Document{}, false, fmt.Errorf("%w: null", ErrUnsupportedVersion)
		}
		if err := json.Unmarshal(raw, &version); err != nil {
			return Document{}, false, fmt.Errorf("version must be an integer: %w", err)
		}
		if version != CurrentVersion {
			return Document{}, false, fmt.Errorf("%w: %d", ErrUnsupportedVersion, version)
		}
	}

	doc := Document{Version: CurrentVersion, Extra: preserved}
	if raw, ok := fields["groups"]; ok && !isJSONNull(raw) {
		if err := decodeStrict(raw, &doc.Groups); err != nil {
			return Document{}, false, fmt.Errorf("groups: %w", err)
		}
	}
	if raw, ok := fields["collapsed"]; ok && !isJSONNull(raw) {
		if err := decodeStrict(raw, &doc.Collapsed); err != nil {
			return Document{}, false, fmt.Errorf("collapsed: %w", err)
		}
	}
	if raw, ok := fields["dirs"]; ok && !isJSONNull(raw) {
		if err := decodeStrict(raw, &doc.Dirs); err != nil {
			return Document{}, false, fmt.Errorf("dirs: %w", err)
		}
	}
	if raw, ok := fields["settings"]; ok && !isJSONNull(raw) {
		var settings Settings
		if err := decodeStrict(raw, &settings); err != nil {
			return Document{}, false, fmt.Errorf("settings: %w", err)
		}
		doc.Settings = &settings
	}
	return doc, legacy, nil
}

// decodeStrict enforces shape, not vocabulary: a known key must decode into
// its expected type, but unknown NESTED fields (a newer build's addition to
// settings or groups) are tolerated rather than treated as corruption.
func decodeStrict(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing data")
	}
	return nil
}

func isJSONNull(raw []byte) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func makeRevision(b []byte, exists bool) revision {
	if !exists {
		return revision{}
	}
	return revision{exists: true, sum: sha256.Sum256(b)}
}

func infoFromDocument(path string, doc Document, legacy bool, status, errText string) Info {
	info := Info{
		Path: path, Status: status, Legacy: legacy, Version: doc.Version, Error: errText,
		BackupPath: path + ".bak", BackupStatus: StatusMissing,
	}
	if status == StatusMissing {
		info.Version = 0
	}
	info.Groups = len(doc.Groups)
	for _, group := range doc.Groups {
		info.Members += len(group.Members)
	}
	info.Dirs = len(doc.Dirs)
	info.Collapsed = len(doc.Collapsed)
	return info
}

func addBackupInfo(info *Info) {
	if info == nil || info.BackupPath == "" {
		return
	}
	fi, err := os.Lstat(info.BackupPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			info.BackupStatus = StatusMissing
			return
		}
		// Conservatively treat an indeterminate path as present. A permission
		// failure must not make a missing primary look safe to recreate.
		info.BackupExists = true
		info.BackupStatus, info.BackupError = StatusUnreadable, err.Error()
		return
	}
	info.BackupExists, info.BackupModTime = true, fi.ModTime()

	b, err := os.ReadFile(info.BackupPath)
	if err != nil {
		info.BackupStatus, info.BackupError = StatusUnreadable, err.Error()
		return
	}
	doc, legacy, decodeErr := decodeDocument(b)
	if decodeErr != nil {
		info.BackupStatus, info.BackupError = StatusCorrupt, decodeErr.Error()
		if errors.Is(decodeErr, ErrUnsupportedVersion) {
			info.BackupStatus = StatusUnsupported
		}
		return
	}
	info.BackupStatus, info.BackupLegacy, info.BackupVersion = StatusValid, legacy, doc.Version
	if legacy {
		info.BackupStatus = StatusLegacy
	}
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if err = tmp.Chmod(mode.Perm()); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpName, path); err != nil {
		return err
	}
	bestEffortSyncDir(dir)
	return nil
}

func bestEffortSyncDir(dir string) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = f.Sync()
	_ = f.Close()
}
