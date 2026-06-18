package record

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Store reads and writes StackRecord JSON files under a root directory.
//
// The layout mirrors ADR-0046: project-scoped stacks live at
//
//	<Root>/projects/<project>/<env>/stacks/<stack-name>.json
//
// and independent stacks (no project/env binding — Project="" Env="") at
//
//	<Root>/independent/stacks/<stack-name>.json
//
// Root is typically the Packwright home directory (see config.Home); tests
// pass t.TempDir().
type Store struct {
	Root string
}

// NewStore returns a Store rooted at root. The directory is not created
// eagerly — Write creates the per-stack parent directory on demand so a
// read-only Store (e.g. for List) never has a write side-effect.
func NewStore(root string) *Store { return &Store{Root: root} }

// Path returns the absolute on-disk path the record for (project, env,
// stackName) would live at. Does not check existence. Empty project / env
// route to the "independent" tree.
func (s *Store) Path(project, env, stackName string) string {
	if stackName == "" {
		return ""
	}
	if project == "" || env == "" {
		return filepath.Join(s.Root, "independent", "stacks", stackName+".json")
	}
	return filepath.Join(s.Root, "projects", project, env, "stacks", stackName+".json")
}

// Read deserialises the record at the canonical (project, env, stackName)
// path. Returns (nil, fs.ErrNotExist) when no record exists yet — callers use
// errors.Is to branch.
func (s *Store) Read(project, env, stackName string) (*StackRecord, error) {
	return s.ReadPath(s.Path(project, env, stackName))
}

// ReadPath is the path-keyed variant of Read used by callers that already
// know the file location (e.g. List).
func (s *Store) ReadPath(path string) (*StackRecord, error) {
	if path == "" {
		return nil, errors.New("record: empty path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rec StackRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("record: parse %q: %w", path, err)
	}
	if err := rec.checkSchemaVersion(); err != nil {
		return nil, fmt.Errorf("record: %q: %w", path, err)
	}
	return &rec, nil
}

// Write serialises rec to JSON and atomically writes it to its canonical
// path. The parent directory is created if missing; the file is written via a
// .tmp sibling that is fsync'd and renamed into place so a crash mid-write
// never leaves a half-written record.
func (s *Store) Write(rec *StackRecord) error {
	if rec == nil {
		return errors.New("record: Write: rec is nil")
	}
	if rec.StackName == "" {
		return errors.New("record: Write: StackName is empty")
	}
	if rec.SchemaVersion == "" {
		rec.SchemaVersion = SchemaVersion
	}

	path := s.Path(rec.Project, rec.Env, rec.StackName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("record: create dir for %q: %w", path, err)
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("record: marshal: %w", err)
	}
	// JSON files conventionally end with a newline so terminals and the
	// next `cat`'d field do not collide on the same line.
	data = append(data, '\n')

	return writeAtomic(path, data)
}

// List returns every record under (project, env). An empty project / env
// pair lists independent records. The returned slice is in directory-walk
// order; callers sort if they need a stable view.
func (s *Store) List(project, env string) ([]*StackRecord, error) {
	dir := filepath.Dir(s.Path(project, env, "x"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]*StackRecord, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		rec, err := s.ReadPath(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
}

// Find scans every project / env tree under Root and returns the first record
// whose StackName matches. Used by callers that know the stack name but not
// which project owns it (e.g. command-line `packwright stack show`).
//
// Returns (nil, fs.ErrNotExist) when no record matches.
func (s *Store) Find(stackName string) (*StackRecord, error) {
	if stackName == "" {
		return nil, errors.New("record: Find: stackName is empty")
	}
	target := stackName + ".json"
	var found *StackRecord

	walk := func(root string) error {
		return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() || d.Name() != target {
				return nil
			}
			rec, readErr := s.ReadPath(path)
			if readErr != nil {
				return readErr
			}
			found = rec
			return filepath.SkipAll
		})
	}

	for _, sub := range []string{"projects", "independent"} {
		if err := walk(filepath.Join(s.Root, sub)); err != nil {
			return nil, err
		}
		if found != nil {
			return found, nil
		}
	}
	return nil, fs.ErrNotExist
}

// checkSchemaVersion rejects records whose major schema version this binary
// does not know how to read. v1 is the only known major today.
func (r *StackRecord) checkSchemaVersion() error {
	if r.SchemaVersion == "" {
		return errors.New("schema_version is empty")
	}
	if r.SchemaVersion == SchemaVersion {
		return nil
	}
	// Accept anything else with the same "packwright.stack-record.v1"
	// prefix; later major versions are rejected outright.
	if strings.HasPrefix(r.SchemaVersion, "packwright.stack-record.v1") {
		return nil
	}
	return fmt.Errorf("unknown schema_version %q (this binary speaks %q)", r.SchemaVersion, SchemaVersion)
}

// writeAtomic writes data to a sibling .tmp file, fsyncs it, and renames it
// over dest with mode 0o644. Mirrors config/config.go writeAtomic and
// internal/pack/install/pins.go writeAtomicFile — the project's pattern is
// to keep a small private copy in each consumer package rather than create a
// shared dependency back through config/. See config.writeAtomic for the
// Windows close-before-remove rationale; keep all three copies in sync if
// the platform behaviour changes.
func writeAtomic(dest string, data []byte) error {
	dir := filepath.Dir(dest)
	f, err := os.CreateTemp(dir, filepath.Base(dest)+".*.tmp")
	if err != nil {
		return fmt.Errorf("record: create temp in %q: %w", dir, err)
	}
	tmp := f.Name()
	success := false
	defer func() {
		_ = f.Close()
		if !success {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("record: write temp %q: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("record: fsync temp %q: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("record: close temp %q: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return fmt.Errorf("record: chmod temp %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("record: rename %q -> %q: %w", tmp, dest, err)
	}
	success = true
	return nil
}
