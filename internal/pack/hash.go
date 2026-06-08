// Package pack implements the trust primitives used when a Packwright user
// installs or updates a pack — a content hash of the pack tree, a scan that
// extracts every shell-execution surface declared by the pack's manifests,
// and a UI-agnostic consent contract that front-ends override per ADR-0025.
//
// Nothing in this package opens a network connection, shells out, or touches
// AWS credentials. It is pure filesystem + YAML parsing. The TUI/GUI consent
// screen that calls RequestConsent lives in a follow-up PR; the default
// implementation here denies, so non-interactive contexts are safe.
package pack

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// hashPrefix tags the returned digest so callers can tell at a glance which
// algorithm produced it. Matches the form ADR-0025 uses in its consent-screen
// mockup (e.g. "sha256:c7a9..."). A future migration to a different algorithm
// can land alongside a new prefix without ambiguity.
const hashPrefix = "sha256:"

// excludedDirs is the set of directory names skipped during a tree walk.
// .git is excluded per ADR-0025 because a pack's checkout state (HEAD, pack
// files, etc.) changes from clone to clone even when the pack contents are
// identical, which would defeat the stable-hash guarantee.
var excludedDirs = map[string]struct{}{
	".git": {},
}

// Hash returns the prefixed sha256 digest of packDir's contents. The digest
// is stable across runs: for the same on-disk tree it returns the same
// string regardless of walk order, OS, or transient filesystem metadata.
//
// The algorithm is the one described in feature/mvp3/plan/06-pack-trust.md:
//
//  1. Recursively walk packDir, skipping any directory named ".git".
//  2. For every regular file, record (relative-path, sha256(content)).
//  3. Sort the records by relative path (lexical, forward-slash form).
//  4. Concatenate each record as "<relpath>\0<hex-digest>\n".
//  5. Return "sha256:" + hex(sha256(concatenation)).
//
// Symlinks are skipped (they do not contribute to the tree's executable
// surface and following them could escape packDir). Empty directories
// contribute nothing to the hash, matching git's behaviour and keeping
// rename-empty-dir diffs out of the consent re-prompt.
func Hash(packDir string) (string, error) {
	root, err := filepath.Abs(packDir)
	if err != nil {
		return "", fmt.Errorf("pack: hash: resolve %q: %w", packDir, err)
	}

	type entry struct {
		rel  string
		hash string
	}
	var entries []entry

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if _, skip := excludedDirs[d.Name()]; skip && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			// Skip symlinks, devices, sockets, etc. Their content is not a
			// stable, reproducible property of the pack tree.
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("pack: hash: relpath %q: %w", path, err)
		}
		sum, err := hashFile(path)
		if err != nil {
			return err
		}
		entries = append(entries, entry{
			rel:  filepath.ToSlash(rel),
			hash: sum,
		})
		return nil
	})
	if walkErr != nil {
		return "", walkErr
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	h := sha256.New()
	for _, e := range entries {
		// "\0" + "\n" framing makes the encoding unambiguous even if a path
		// somehow contains a newline: the NUL separator pins the boundary
		// between path and digest, and the digest itself is fixed-width hex.
		_, _ = io.WriteString(h, e.rel)
		_, _ = h.Write([]byte{0})
		_, _ = io.WriteString(h, e.hash)
		_, _ = h.Write([]byte{'\n'})
	}
	return hashPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

// hashFile streams path through sha256 and returns the lowercase hex digest.
// Streaming (rather than ReadFile) keeps memory bounded for packs with large
// template artifacts — manifests are small but adjacent CloudFormation /
// monitor fixtures can be megabytes.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("pack: hash: open %q: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("pack: hash: read %q: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hasHashPrefix reports whether s is in the canonical "sha256:..." form
// produced by Hash. Exported callers do not normally need this — the
// follow-up consent-screen PR uses it to recognise legacy stored hashes
// that lack the prefix.
func hasHashPrefix(s string) bool { return strings.HasPrefix(s, hashPrefix) }
