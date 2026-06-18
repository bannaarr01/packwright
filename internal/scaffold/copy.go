package scaffold

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// CopyTemplate forks an existing manifest YAML into a new draft sibling
// (ADR-0047). It reads srcPath, rewrites the top-level `slash` field to
// newSlash, injects `_draft: true` and `_copied_from: <source-slash> @
// <srcPath>` at the top of the document, and atomically writes the result
// to dstPath.
//
// The destination's parent directory is created on demand. dstPath is
// expected to live under a `drafts/` directory next to a scope's
// `manifests/` (per ADR-0045 disk layout); CopyTemplate enforces that
// convention so misuse — landing a copy in `manifests/` directly — is
// caught at the call site rather than silently producing a deployable
// fork. CopyTemplate refuses to overwrite an existing file: the caller
// must pick a different slash or delete the collider first.
//
// The write is atomic: the new content lands in dstPath.tmp and is then
// renamed over dstPath, so a reader inspecting the destination at any
// instant sees either the previous content (full, valid YAML) or the new
// content (full, valid YAML) — never a half-written file. This matches
// the "valid YAML at every moment under inspection" guarantee the ADR
// makes for /promote-template.
func CopyTemplate(srcPath, dstPath, newSlash string) error {
	if srcPath == "" {
		return fmt.Errorf("scaffold: copy: srcPath is required")
	}
	if dstPath == "" {
		return fmt.Errorf("scaffold: copy: dstPath is required")
	}
	if err := checkSlash(newSlash); err != nil {
		return err
	}
	if err := requireDraftsDir(dstPath); err != nil {
		return err
	}

	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("scaffold: copy: read %s: %w", srcPath, err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("scaffold: copy: parse %s: %w", srcPath, err)
	}
	mapping, err := rootMappingFor(&root, srcPath)
	if err != nil {
		return err
	}

	srcSlash, err := readSlash(mapping)
	if err != nil {
		return fmt.Errorf("scaffold: copy: %s: %w", srcPath, err)
	}
	if srcSlash == newSlash {
		return fmt.Errorf("scaffold: copy: new slash %q matches source slash — pick a different name", newSlash)
	}

	rewriteSlash(mapping, newSlash)
	provenance := fmt.Sprintf("%s @ %s", srcSlash, srcPath)
	prependMetadata(mapping, provenance)

	if _, err := os.Stat(dstPath); err == nil {
		return fmt.Errorf("scaffold: copy: refusing to overwrite existing manifest: %s", dstPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("scaffold: copy: stat %s: %w", dstPath, err)
	}

	out, err := yaml.Marshal(&root)
	if err != nil {
		return fmt.Errorf("scaffold: copy: marshal %s: %w", dstPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return fmt.Errorf("scaffold: copy: mkdir %s: %w", filepath.Dir(dstPath), err)
	}
	return atomicWriteFile(dstPath, out, 0o644)
}

// PromoteTemplate atomically clears `_draft: true` from the manifest at
// path. CopiedFrom (the provenance line) is left in place so the deployed
// manifest still carries an audit trail of where it originated. The file
// must currently be a draft; a no-op on a non-draft would be silently
// confusing, so PromoteTemplate returns an error instead.
//
// Like CopyTemplate, the write is atomic — content lands in `<path>.tmp`
// first, then is renamed over `path`. Per ADR-0047, this keeps the file
// valid YAML at every observable moment, so the watcher reload can never
// race with a half-written promotion.
func PromoteTemplate(path string) error {
	if path == "" {
		return fmt.Errorf("scaffold: promote: path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("scaffold: promote: read %s: %w", path, err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("scaffold: promote: parse %s: %w", path, err)
	}
	mapping, err := rootMappingFor(&root, path)
	if err != nil {
		return err
	}

	cleaned := mapping.Content[:0]
	found := false
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "_draft" {
			found = true
			continue
		}
		cleaned = append(cleaned, mapping.Content[i], mapping.Content[i+1])
	}
	if !found {
		return fmt.Errorf("scaffold: promote: %s is not a draft (no _draft key)", path)
	}
	mapping.Content = cleaned

	out, err := yaml.Marshal(&root)
	if err != nil {
		return fmt.Errorf("scaffold: promote: marshal %s: %w", path, err)
	}
	return atomicWriteFile(path, out, 0o644)
}

// rootMappingFor returns the root mapping of a parsed YAML document or a
// path-aware error. It mirrors the helper in the manifest loader but is
// duplicated here so the scaffold layer doesn't depend on internal/manifest.
func rootMappingFor(root *yaml.Node, path string) (*yaml.Node, error) {
	if root.Kind == 0 || len(root.Content) == 0 {
		return nil, fmt.Errorf("scaffold: %s: empty document", path)
	}
	mapping := root.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("scaffold: %s: expected a YAML mapping at the document root", path)
	}
	return mapping, nil
}

// readSlash extracts the value of the top-level `slash:` field from a
// mapping. Returns an error if the field is missing or non-scalar; the
// /copy-template flow can't proceed without it because the provenance
// line names the source slash.
func readSlash(mapping *yaml.Node) (string, error) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "slash" {
			if mapping.Content[i+1].Kind != yaml.ScalarNode {
				return "", fmt.Errorf("slash: expected a scalar value")
			}
			return mapping.Content[i+1].Value, nil
		}
	}
	return "", fmt.Errorf("slash: not found")
}

// rewriteSlash replaces the value of the top-level `slash:` field. The
// scalar style is reset so YAML round-tripping picks the canonical form
// for the new value (plain or quoted as appropriate).
func rewriteSlash(mapping *yaml.Node, newSlash string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "slash" {
			mapping.Content[i+1].Value = newSlash
			mapping.Content[i+1].Tag = "!!str"
			mapping.Content[i+1].Style = 0
		}
	}
}

// prependMetadata strips any prior `_draft` / `_copied_from` entries and
// re-inserts them at the top of the mapping, so the metadata is visible
// in the first few lines of the file when a user opens it in an editor.
// The order — _draft first, _copied_from second — matches the ADR
// example and the /copy-template golden output.
func prependMetadata(mapping *yaml.Node, provenance string) {
	cleaned := mapping.Content[:0]
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		k := mapping.Content[i]
		if k.Value == "_draft" || k.Value == "_copied_from" {
			continue
		}
		cleaned = append(cleaned, mapping.Content[i], mapping.Content[i+1])
	}
	mapping.Content = append([]*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "_draft"},
		{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "true"},
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "_copied_from"},
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: provenance},
	}, cleaned...)
}

// checkSlash validates that s looks like a slash command: leading "/",
// no whitespace, non-empty stem. Looser than manifest.Validate's check
// because the manifest validator will run the strict pass on Load.
func checkSlash(s string) error {
	if s == "" {
		return fmt.Errorf("scaffold: copy: newSlash is required")
	}
	if !strings.HasPrefix(s, "/") {
		return fmt.Errorf("scaffold: copy: newSlash must start with %q (got %q)", "/", s)
	}
	if strings.ContainsAny(s, " \t\n") {
		return fmt.Errorf("scaffold: copy: newSlash must not contain whitespace")
	}
	if strings.TrimPrefix(s, "/") == "" {
		return fmt.Errorf("scaffold: copy: newSlash must have a non-empty stem")
	}
	return nil
}

// requireDraftsDir verifies that dstPath's immediate parent directory is
// named "drafts". ADR-0047 puts every fork under a drafts/ folder so a
// half-edited copy can never be confused with a deployable manifest under
// manifests/. Catching this at the call site is cheaper than catching it
// later when the watcher categorises the file.
func requireDraftsDir(dstPath string) error {
	parent := filepath.Base(filepath.Dir(dstPath))
	if parent != "drafts" {
		return fmt.Errorf("scaffold: copy: destination must live under a drafts/ directory (got parent %q)", parent)
	}
	return nil
}

// atomicWriteFile writes data to a temp file in the destination's
// directory and then renames it over path. On every supported platform
// (POSIX + Windows on NTFS) the rename is atomic, so a concurrent reader
// — including the hot-reload watcher — observes either the previous
// content or the new content but never a half-written file.
func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".pw-write-*")
	if err != nil {
		return fmt.Errorf("scaffold: create temp under %s: %w", dir, err)
	}
	tmpPath := tmp.Name()

	// Ensure the temp file is removed if we exit before the rename.
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("scaffold: write %s: %w", tmpPath, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("scaffold: chmod %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("scaffold: fsync %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("scaffold: close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("scaffold: rename %s -> %s: %w", tmpPath, path, err)
	}
	return nil
}
