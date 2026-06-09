// Package install implements pack distribution per ADR-0027 — git clone /
// pull / remove / list, plus a local-path "symlink into packs/" mode for
// pack authors developing against a working tree.
//
// Every install operation funnels through pack.RequestConsent (ADR-0025)
// before a pack becomes visible to the rest of the binary: a freshly cloned
// directory is scanned, hashed, and presented to the consent hook; if the
// hook returns Denied the directory is removed and no metadata is written.
// On Update, consent is only re-requested when the executable surface
// (pack.Scan output) actually changed — README-only edits roll forward
// silently, matching the ADR's "surface changed" trigger.
//
// The package shells out to the system git binary via os/exec rather than
// bundling go-git, per ADR-0027's "zero new dependencies" stance. The only
// outbound network traffic Packwright performs lives behind those exec
// calls. Local-path installs use a symlink on POSIX and fall back to a
// recursive copy on Windows where symlink creation requires elevation.
package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bannaarr01/packwright/internal/pack"
)

// metaSuffix is the extension applied to install-metadata files alongside
// each pack directory. The leading dot keeps the file out of the way for
// `ls`; the suffix means pack.Discover (which iterates only directories)
// never tries to parse the metadata as a pack.
const metaSuffix = ".install.json"

// ErrDenied is returned when pack.RequestConsent denied the install or
// update. It is a sentinel so callers can branch with errors.Is.
var ErrDenied = errors.New("install: consent denied")

// ErrNotInstalled is returned by Update and Remove when no pack by the
// supplied name exists under <homeDir>/packs.
var ErrNotInstalled = errors.New("install: pack not installed")

// ErrAlreadyInstalled is returned by Add when a pack directory with the
// derived name already exists. Callers must Remove the prior install
// first; we never silently overwrite a tree the user may have edited.
var ErrAlreadyInstalled = errors.New("install: pack already installed")

// Installed is the JSON-serialised record stored alongside each pack
// directory at <homeDir>/packs/<name>.install.json. It captures every
// fact the install package needs to update or display the pack later —
// the original source, the trusted hash, and the trusted surface.
//
// The surface is persisted so Update can decide whether the executable
// surface changed without re-cloning history; if it hasn't changed
// (README-only edit), we skip the consent prompt and just refresh the
// trusted hash. If it has, we call RequestConsent and pass the prior
// hash as the diff anchor per the ADR-0025 contract.
type Installed struct {
	// Name is the pack identifier — the directory name under
	// <homeDir>/packs/ and the metadata-file stem.
	Name string `json:"name"`

	// URL is the original git remote, when the pack was cloned. Empty
	// for local-path installs.
	URL string `json:"url,omitempty"`

	// Ref is the optional refspec the user pinned at install time
	// (the part after `#` in `<url>#<ref>`). Empty means "remote
	// HEAD".
	Ref string `json:"ref,omitempty"`

	// Local reports whether the pack was installed from a local
	// filesystem path rather than cloned from a remote. When true,
	// LocalSource carries the absolute path the symlink (or copied
	// tree) was sourced from.
	Local bool `json:"local,omitempty"`

	// LocalSource is the absolute source path for a Local install.
	// Empty when Local is false.
	LocalSource string `json:"local_source,omitempty"`

	// TrustedHash is pack.Hash at the moment the user last accepted
	// the consent prompt. Update compares freshly computed Hash
	// values against this field.
	TrustedHash string `json:"trusted_hash"`

	// Surface is the pack.Scan output at the trusted hash. Update
	// uses it to decide whether to re-prompt: equal surface ⇒ silent
	// refresh; different surface ⇒ RequestConsent.
	Surface pack.Surface `json:"surface"`

	// InstalledAt is the wall-clock time of the original Add.
	InstalledAt time.Time `json:"installed_at"`

	// UpdatedAt is the wall-clock time of the most recent Update.
	// Zero on a fresh install.
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// Dir returns the absolute path to the pack directory under homeDir.
// The metadata only records the name; the directory layout is fixed by
// ADR-0010 (<home>/packs/<name>) so the path is derived rather than
// stored to keep the JSON portable across moves of $PACKWRIGHT_HOME.
func (i Installed) Dir(homeDir string) string {
	return filepath.Join(homeDir, "packs", i.Name)
}

// metaPath returns the absolute path to the metadata file for a pack
// of the given name under homeDir.
func metaPath(homeDir, name string) string {
	return filepath.Join(homeDir, "packs", name+metaSuffix)
}

// readMeta loads the Installed record for name under homeDir. A missing
// file is reported as ErrNotInstalled so callers can distinguish it
// from a structurally broken metadata file.
func readMeta(homeDir, name string) (*Installed, error) {
	path := metaPath(homeDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotInstalled
		}
		return nil, fmt.Errorf("install: read metadata %q: %w", path, err)
	}
	var meta Installed
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("install: parse metadata %q: %w", path, err)
	}
	return &meta, nil
}

// writeMeta atomically writes meta to <homeDir>/packs/<meta.Name>.install.json.
// The write is staged through a sibling temp file and renamed into place
// so a crash never leaves a half-written metadata file — the same
// posture config.Save uses for config.yaml.
func writeMeta(homeDir string, meta *Installed) error {
	path := metaPath(homeDir, meta.Name)
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("install: marshal metadata: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("install: ensure %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
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
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install: rename %q -> %q: %w", tmpName, path, err)
	}
	success = true
	return nil
}

// removeMeta deletes the metadata file for name. A missing file is not
// an error — Remove may be called against a partial install where the
// directory exists but the metadata write failed.
func removeMeta(homeDir, name string) error {
	path := metaPath(homeDir, name)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("install: remove metadata %q: %w", path, err)
	}
	return nil
}

// List enumerates packs installed under homeDir in lexical order of
// name. Packs whose metadata file is missing or malformed are skipped:
// the directory may exist from a partial install or a manual `git
// clone` the user performed by hand, and we deliberately do not invent
// metadata for those.
//
// List does no network or git work — it is a pure filesystem read so
// the GUI palette and TUI list can call it on every refresh.
func List(homeDir string) ([]Installed, error) {
	packsDir := filepath.Join(homeDir, "packs")
	entries, err := os.ReadDir(packsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("install: read %q: %w", packsDir, err)
	}

	var out []Installed
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		meta, err := readMeta(homeDir, e.Name())
		if err != nil {
			// A pack-shaped directory without metadata is a manual
			// install — skip it rather than fabricate a record. It still
			// appears via pack.Discover; install just doesn't manage it.
			continue
		}
		out = append(out, *meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Remove deletes the pack directory <homeDir>/packs/<name>, its
// install metadata, and any config.yaml pin pointing at it. A missing
// pack returns ErrNotInstalled; a missing metadata file (manual
// install) still allows the directory to be deleted so the operator
// has a single "make it go away" lever.
func Remove(homeDir, name string) error {
	if name == "" {
		return errors.New("install: remove: empty name")
	}
	packDir := filepath.Join(homeDir, "packs", name)
	stat, err := os.Lstat(packDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNotInstalled
		}
		return fmt.Errorf("install: stat %q: %w", packDir, err)
	}

	// A symlinked local install: RemoveAll on the symlink path itself
	// only removes the link, never the target — exactly what we want.
	// For regular directories it recursively deletes the tree.
	_ = stat
	if err := os.RemoveAll(packDir); err != nil {
		return fmt.Errorf("install: remove %q: %w", packDir, err)
	}
	if err := removeMeta(homeDir, name); err != nil {
		return err
	}
	if err := unpinPack(homeDir, name); err != nil {
		return err
	}
	return nil
}

// derivedName turns a git URL into the directory name we clone into.
// The rule (ADR-0027): the last path segment with any trailing `.git`
// stripped. Query strings and `#ref` suffixes are dropped upstream by
// parseSource, so we only need to handle the path component here.
//
// A URL whose last segment is empty (trailing slash) walks backward
// until it finds a non-empty segment; this matches how `git clone`
// itself derives the destination directory.
func derivedName(remote string) string {
	// Strip any URL fragment / query the caller forgot to remove and
	// any explicit ".git" suffix on the path.
	if i := strings.IndexAny(remote, "?#"); i >= 0 {
		remote = remote[:i]
	}
	// Trim trailing slashes so the last segment is meaningful.
	remote = strings.TrimRight(remote, "/")
	// Find the last segment separator. Both `/` and `:` (scp-like git
	// URLs: `git@host:owner/repo`) act as separators here.
	if i := strings.LastIndexAny(remote, "/:"); i >= 0 {
		remote = remote[i+1:]
	}
	remote = strings.TrimSuffix(remote, ".git")
	return remote
}

// sanitizeName validates and normalises a candidate pack name (either
// the URL-derived stem or a pack.yaml `name:` field). It enforces the
// invariants the install metadata-file naming relies on: no path
// separators, no leading dot (would be skipped by future tooling), no
// empty string.
func sanitizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("install: derived pack name is empty")
	}
	if strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("install: pack name %q must not contain path separators", name)
	}
	if name == "." || name == ".." {
		return "", fmt.Errorf("install: pack name %q is not allowed", name)
	}
	if strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("install: pack name %q must not start with '.'", name)
	}
	return name, nil
}
