package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config is the user-editable state stored at <Home>/config.yaml. Fields are
// tagged for gopkg.in/yaml.v3 and round-trip cleanly: marshalling a struct
// loaded from disk produces a byte-equivalent file (modulo key order, which
// yaml.v3 preserves from the struct).
//
// AI is intentionally opaque (map[string]any) for MVP 1 so the schema can
// evolve in later PRs without breaking config.yaml on existing installs.
type Config struct {
	// Profile is the AWS CLI profile name to use. Empty means "no profile
	// selected yet" — the first STS verification later in MVP 1 fills it in.
	Profile string `yaml:"profile"`
	// Region is the default AWS region. Overridden on first run by the
	// AWS_REGION environment variable; see defaults.go.
	Region string `yaml:"region"`
	// Theme is one of "dark", "light", or "auto".
	Theme string `yaml:"theme"`
	// LogLevel controls the logger introduced in PR-03 (one of
	// "debug"/"info"/"warn"/"error").
	LogLevel string `yaml:"log_level"`
	// Packs lists pack names enabled for this user.
	Packs []string `yaml:"packs"`
	// PinnedDefaults maps a slash-command name to a fully-qualified
	// "pack:<name>" entry, recording which pack should handle that command.
	PinnedDefaults map[string]string `yaml:"defaults"`
	// AI is reserved for AI-related settings; treated as opaque YAML until
	// later MVPs give it a schema.
	AI map[string]any `yaml:"ai"`
	// DisableUpdateCheck suppresses the launch-time GitHub Releases probe
	// (internal/update). When true, no outbound HTTP request is made.
	// PACKWRIGHT_NO_UPDATE_CHECK=1 has the same effect and is honoured
	// regardless of this field. See ADR-0030.
	DisableUpdateCheck bool `yaml:"disable_update_check"`
	// UpdateChannel selects whether the update check considers stable or
	// pre-release tags. Recognised values: "stable" (default), "prerelease".
	// An empty string is treated as "stable" so existing config.yaml files
	// keep working. See ADR-0030.
	UpdateChannel string `yaml:"update_channel"`
}

// Load reads <Home>/config.yaml and returns the parsed Config. When no file
// exists yet, Load returns the default Config without writing to disk — the
// caller can mutate it and persist with Save when ready. A malformed file
// surfaces as an error.
func Load() (*Config, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return defaultConfig(), nil
		}
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %q: %w", path, err)
	}
	return cfg, nil
}

// Save writes c to <Home>/config.yaml atomically: it marshals to YAML, writes
// the bytes to a sibling .tmp file, fsyncs that file, and then renames it
// over the destination. A crash between any two steps leaves either the old
// file intact or the new one fully written — never a half-written config.
func (c *Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	return writeAtomic(path, data)
}

// writeAtomic writes data to a sibling temp file, fsyncs, and renames over
// dest with mode 0o644. The temp file shares dest's directory so the rename
// stays on the same filesystem (atomic on POSIX; on Windows os.Rename
// replaces the target since Go 1.5).
//
// On any error the temp file is removed via a deferred cleanup. The defer
// also closes the file handle first — on Windows os.Remove fails while a
// handle is open, so closing before removing is mandatory there even when
// it looks redundant on POSIX.
func writeAtomic(dest string, data []byte) error {
	dir := filepath.Dir(dest)
	f, err := os.CreateTemp(dir, filepath.Base(dest)+".*.tmp")
	if err != nil {
		return fmt.Errorf("config: create temp in %q: %w", dir, err)
	}
	tmp := f.Name()
	success := false
	defer func() {
		// Always close the handle before attempting to remove. A second
		// Close after a successful one returns fs.ErrClosed, which we
		// intentionally discard.
		_ = f.Close()
		if !success {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("config: write temp %q: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("config: fsync temp %q: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("config: close temp %q: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o644); err != nil {
		return fmt.Errorf("config: chmod temp %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("config: rename %q to %q: %w", tmp, dest, err)
	}
	success = true
	return nil
}
