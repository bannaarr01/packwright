package install

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// unpinPack strips any defaults entry in <homeDir>/config.yaml whose
// value points at the pack being removed. Per ADR-0023 a pin value
// for a pack-owned slash is "pack:<name>"; everything else (user
// scope, forward-compatible identifiers) is left alone.
//
// This implementation reads and rewrites config.yaml directly rather
// than importing the config package. Importing config would require
// the install package to call config.Home() — which always resolves
// from environment variables — and would couple every test to
// PACKWRIGHT_HOME mutation. The on-disk shape is small and stable, so
// duplicating the minimal yaml.Node round-trip here is the cleaner
// trade.
//
// A missing config.yaml is a no-op: nothing to unpin.
func unpinPack(homeDir, packName string) error {
	if packName == "" {
		return errors.New("install: unpin: empty pack name")
	}
	path := filepath.Join(homeDir, "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("install: read %q: %w", path, err)
	}

	// Decode into a flexible map so unknown keys round-trip untouched.
	// yaml.v3's *yaml.Node would also work but the install package
	// already deals in plain Go maps everywhere else.
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("install: parse %q: %w", path, err)
	}
	if raw == nil {
		return nil
	}

	defaults, ok := raw["defaults"].(map[string]any)
	if !ok {
		// Either absent or not a mapping — nothing to clean up. A
		// future config schema change might use a different shape;
		// don't error on shapes this package doesn't understand.
		return nil
	}

	target := "pack:" + packName
	changed := false
	for slash, value := range defaults {
		s, ok := value.(string)
		if !ok {
			continue
		}
		if s == target {
			delete(defaults, slash)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if len(defaults) == 0 {
		// Match config.Unpin's "drop empty map back to nil" rule so
		// the on-disk file looks like a fresh install once the last
		// pin is removed.
		delete(raw, "defaults")
	}

	out, err := yaml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("install: marshal config: %w", err)
	}
	if err := writeAtomicFile(path, out, 0o644); err != nil {
		return err
	}
	return nil
}

// writeAtomicFile is install's local copy of config's atomic-write
// helper, kept out of the public API surface. The same fsync-and-
// rename dance config.Save uses — we don't want a crash mid-rewrite
// to corrupt config.yaml just because an unrelated unpin happened to
// be in flight.
func writeAtomicFile(dest string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("install: ensure %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(dest)+".*.tmp")
	if err != nil {
		return fmt.Errorf("install: create temp in %q: %w", dir, err)
	}
	tmpName := tmp.Name()
	success := false
	defer func() {
		_ = tmp.Close()
		if !success {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("install: write temp %q: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("install: fsync temp %q: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("install: close temp %q: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("install: chmod temp %q: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("install: rename %q -> %q: %w", tmpName, dest, err)
	}
	success = true
	return nil
}
