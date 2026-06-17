package delete

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Subdir is the directory inside the Packwright home that holds the
// deletion-workflow artefacts: tray.json (the staging tray) and
// deletions.jsonl (the audit log).
const Subdir = "audit"

// TrayFilename is the on-disk name of the persisted staging tray.
const TrayFilename = "tray.json"

// trayVersion is the on-disk schema marker. Bumped only on a
// breaking change so older Packwright builds can refuse to load a
// newer file rather than silently round-tripping unknown fields
// out.
const trayVersion = 1

// trayDoc is the on-disk JSON envelope. We do not write Rows at the
// top level because we want the version marker to be the first
// field a future migrator inspects.
type trayDoc struct {
	Version int   `json:"version"`
	Rows    []Row `json:"rows"`
}

// Tray is the persistent staging list of resources marked for
// deletion. It is safe for concurrent use within a process; the
// on-disk file is rewritten atomically (write to <name>.tmp then
// rename) so a crash mid-write cannot corrupt it.
//
// The zero value is not usable; construct a Tray with OpenTray.
type Tray struct {
	path string

	mu   sync.Mutex
	rows []Row
	// newID is replaced in tests to make IDs deterministic.
	newID func() string
	// now is replaced in tests so AddedAt is stable.
	now func() time.Time
}

// ErrRowNotFound is returned by Remove and Get when no row matches.
var ErrRowNotFound = errors.New("delete: tray row not found")

// OpenTray opens (and, if necessary, creates) the staging tray at
// <homeDir>/audit/tray.json. The audit/ subdirectory is created on
// first use — config.Home's default subdirs list does not include
// it.
//
// A missing or empty file is treated as an empty tray; a malformed
// file returns a wrapped error so the caller can surface the path
// to the user rather than silently dropping their staged work.
func OpenTray(homeDir string) (*Tray, error) {
	if homeDir == "" {
		return nil, errors.New("delete: OpenTray: homeDir is empty")
	}
	dir := filepath.Join(homeDir, Subdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("delete: create %q: %w", dir, err)
	}
	t := &Tray{
		path:  filepath.Join(dir, TrayFilename),
		newID: defaultNewID,
		now:   func() time.Time { return time.Now().UTC() },
	}
	if err := t.load(); err != nil {
		return nil, err
	}
	return t, nil
}

// load reads the tray file into t.rows. A missing file is treated
// as an empty tray.
func (t *Tray) load() error {
	data, err := os.ReadFile(t.path)
	if errors.Is(err, os.ErrNotExist) {
		t.rows = nil
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete: read %q: %w", t.path, err)
	}
	if len(data) == 0 {
		t.rows = nil
		return nil
	}
	var doc trayDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("delete: parse %q: %w", t.path, err)
	}
	if doc.Version > trayVersion {
		return fmt.Errorf("delete: tray %q has version %d, this build supports up to %d",
			t.path, doc.Version, trayVersion)
	}
	t.rows = doc.Rows
	return nil
}

// flush writes t.rows to disk atomically. The caller must hold t.mu.
func (t *Tray) flush() error {
	doc := trayDoc{Version: trayVersion, Rows: t.rows}
	buf, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("delete: encode tray: %w", err)
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, append(buf, '\n'), 0o644); err != nil {
		return fmt.Errorf("delete: write %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, t.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("delete: rename %q -> %q: %w", tmp, t.path, err)
	}
	return nil
}

// Path returns the on-disk path of the tray file.
func (t *Tray) Path() string { return t.path }

// Add stages res in the tray with the supplied note and returns the
// generated row. A row for an already-staged (Kind, Identifier) pair
// is updated in place rather than duplicated — the AddedAt and Note
// fields are refreshed, the ID is preserved.
//
// On flush failure the in-memory state is rolled back so a caller
// who saw the error never observes a partially-applied write.
func (t *Tray) Add(res Resource, note string) (Row, error) {
	if err := res.Validate(); err != nil {
		return Row{}, fmt.Errorf("delete: tray add: %w", err)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.rows {
		r := &t.rows[i]
		if r.Resource.Kind == res.Kind && r.Resource.Identifier == res.Identifier {
			old := *r
			r.Resource = res
			r.Note = note
			r.AddedAt = t.now()
			if err := t.flush(); err != nil {
				*r = old
				return Row{}, err
			}
			return *r, nil
		}
	}
	row := Row{
		ID:       t.newID(),
		Resource: res,
		AddedAt:  t.now(),
		Note:     note,
	}
	t.rows = append(t.rows, row)
	if err := t.flush(); err != nil {
		t.rows = t.rows[:len(t.rows)-1]
		return Row{}, err
	}
	return row, nil
}

// Remove deletes the row with id and persists the change.
// ErrRowNotFound is returned when no row matches. A flush failure
// rolls back the in-memory removal so the on-disk and in-memory
// state agree.
func (t *Tray) Remove(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, r := range t.rows {
		if r.ID == id {
			removed := r
			t.rows = append(t.rows[:i], t.rows[i+1:]...)
			if err := t.flush(); err != nil {
				t.rows = append(t.rows[:i:i], append([]Row{removed}, t.rows[i:]...)...)
				return err
			}
			return nil
		}
	}
	return ErrRowNotFound
}

// Get returns a copy of the row with id, or ErrRowNotFound.
func (t *Tray) Get(id string) (Row, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, r := range t.rows {
		if r.ID == id {
			return r, nil
		}
	}
	return Row{}, ErrRowNotFound
}

// List returns a copy of every row in the tray, sorted by AddedAt
// (oldest first). The returned slice is freshly allocated so the
// caller may sort or mutate it without affecting the tray.
func (t *Tray) List() []Row {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Row, len(t.rows))
	copy(out, t.rows)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].AddedAt.Before(out[j].AddedAt)
	})
	return out
}

// Len returns the number of staged rows.
func (t *Tray) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.rows)
}

// Clear empties the tray and persists the change. It is a no-op
// when the tray is already empty. A flush failure restores the
// previous rows.
func (t *Tray) Clear() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.rows) == 0 {
		return nil
	}
	saved := t.rows
	t.rows = nil
	if err := t.flush(); err != nil {
		t.rows = saved
		return err
	}
	return nil
}

// SetNote updates the note on an existing row. A flush failure
// restores the previous note.
func (t *Tray) SetNote(id, note string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.rows {
		if t.rows[i].ID == id {
			prev := t.rows[i].Note
			t.rows[i].Note = note
			if err := t.flush(); err != nil {
				t.rows[i].Note = prev
				return err
			}
			return nil
		}
	}
	return ErrRowNotFound
}

// defaultNewID returns 16 random bytes hex-encoded. Falls back to a
// time-derived id if crypto/rand fails so the tray remains usable.
func defaultNewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("row-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
