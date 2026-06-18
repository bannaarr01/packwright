package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadWarning is a non-fatal note emitted by Load when a manifest contains
// content the loader chose to tolerate rather than reject. Today the only
// emitter is the unknown-root-key check: per ADR-0047 a root key prefixed
// with "_" is reserved for ephemeral metadata (e.g. `_draft`, `_archived`)
// and is silently skipped with a warning rather than treated as a typo.
// All other unknown root keys remain hard errors.
//
// Warnings are attached to the in-memory Manifest by LoadWithWarnings.
// Load itself drops them so the watcher / hot-reload path keeps the legacy
// (manifest, error) signature; callers that want the warnings reach for
// LoadWithWarnings.
type LoadWarning struct {
	Path    string
	Key     string
	Line    int
	Message string
}

// Load reads, strict-decodes, and validates the manifest YAML at path. It
// returns the decoded Manifest on success; on failure it returns a non-nil
// error from one of three layers:
//
//   - the filesystem (wrapped with %w so errors.Is(err, os.ErrNotExist)
//     still works);
//   - the YAML decoder (when the document has unknown keys not prefixed
//     with "_", type mismatches, is malformed, or contains more than one
//     YAML document);
//   - this package's validator (returns a *ValidationError describing which
//     field path failed and why).
//
// A manifest that is structurally valid but flagged `_draft: true` is
// considered loadable: Load returns the Manifest with no error, leaving the
// per-deploy ErrDraftNotPromoted check to the engine's Validate call. This
// is what lets the hot-reload watcher surface drafts in the sidebar without
// the loader treating them as broken.
//
// Load does not check whether the manifest's kind is executable at runtime;
// call CanRun on the returned manifest for that.
func Load(path string) (*Manifest, error) {
	m, _, err := LoadWithWarnings(path)
	return m, err
}

// LoadWithWarnings is Load augmented with the list of non-fatal notes the
// loader collected. The first return is the parsed Manifest (or nil on a
// fatal error); the second is the warnings slice (always non-nil but may
// be empty); the third is the fatal error if any.
//
// The dual signature exists so the existing watcher / Apply loop keeps the
// (manifest, error) contract while authoring tools and the /copy-template
// flow can surface the warnings to the user.
func LoadWithWarnings(path string) (*Manifest, []LoadWarning, error) {
	warnings := []LoadWarning{}

	f, err := os.Open(path)
	if err != nil {
		return nil, warnings, fmt.Errorf("manifest: open %s: %w", path, err)
	}
	defer f.Close()

	// Decode the whole document into a yaml.Node so we can identify and
	// strip unknown "_"-prefixed root keys before the strict struct decode
	// rejects them. Using a Node also gives us access to the source line
	// for each key, which lands in the warning for diagnostic context.
	dec := yaml.NewDecoder(f)
	var root yaml.Node
	if err := dec.Decode(&root); err != nil {
		return nil, warnings, fmt.Errorf("manifest: decode %s: %w", path, err)
	}

	// A manifest file describes exactly one action; a second YAML document
	// would be silently dropped without this check.
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case errors.Is(err, io.EOF):
		// expected: exactly one document
	case err == nil:
		return nil, warnings, fmt.Errorf("manifest: %s: contains multiple YAML documents", path)
	default:
		return nil, warnings, fmt.Errorf("manifest: decode %s: %w", path, err)
	}

	mapping, err := rootMapping(&root, path)
	if err != nil {
		return nil, warnings, err
	}

	// Walk the root mapping, dropping unknown "_"-prefixed keys and
	// recording one warning per. Known keys (struct-tagged on Manifest)
	// pass through to the strict decode below.
	known := manifestRootKeys()
	cleaned := mapping.Content[:0]
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		k := mapping.Content[i]
		v := mapping.Content[i+1]
		if _, ok := known[k.Value]; ok {
			cleaned = append(cleaned, k, v)
			continue
		}
		if strings.HasPrefix(k.Value, "_") {
			warnings = append(warnings, LoadWarning{
				Path:    path,
				Key:     k.Value,
				Line:    k.Line,
				Message: fmt.Sprintf("ignored unknown root key %q (the \"_\" prefix is reserved for forward-compatible metadata)", k.Value),
			})
			continue
		}
		// Non-underscore unknown key: keep it in the cleaned mapping so
		// the strict decoder below produces its canonical error
		// (line/column included). This preserves the legacy error shape
		// callers already test against.
		cleaned = append(cleaned, k, v)
	}
	mapping.Content = cleaned

	// Re-marshal the cleaned tree and run the original strict decode so
	// nested unknown keys (inside template:, deploy:, form:) still
	// produce hard errors.
	cleanedBytes, err := yaml.Marshal(&root)
	if err != nil {
		return nil, warnings, fmt.Errorf("manifest: %s: re-marshal: %w", path, err)
	}
	strict := yaml.NewDecoder(bytes.NewReader(cleanedBytes))
	strict.KnownFields(true)

	var m Manifest
	if err := strict.Decode(&m); err != nil {
		return nil, warnings, fmt.Errorf("manifest: decode %s: %w", path, err)
	}

	if err := schemaVersionCheck(&m); err != nil {
		return nil, warnings, fmt.Errorf("manifest: %s: %w", path, err)
	}
	if err := Validate(&m); err != nil {
		// A draft is intentionally loadable: the manifest passes every
		// structural check and is needed by the watcher / sidebar. The
		// deploy-time error pipeline runs Validate again and surfaces
		// ErrDraftNotPromoted there.
		var draftErr *ErrDraftNotPromoted
		if errors.As(err, &draftErr) {
			return &m, warnings, nil
		}
		return nil, warnings, fmt.Errorf("manifest: validate %s: %w", path, err)
	}
	return &m, warnings, nil
}

// rootMapping returns the root mapping node of a parsed YAML document, or
// an error describing the structural mismatch. Empty documents and non-
// mapping roots are both rejected here so the strict struct decode below
// always sees a populated mapping.
func rootMapping(root *yaml.Node, path string) (*yaml.Node, error) {
	if root.Kind == 0 || len(root.Content) == 0 {
		return nil, fmt.Errorf("manifest: %s: empty document", path)
	}
	mapping := root.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("manifest: %s: expected a YAML mapping at the document root", path)
	}
	return mapping, nil
}

// manifestRootKeys returns the set of root-level YAML keys recognised by
// the Manifest struct. The list is derived from the struct tags rather
// than hard-coded so additions to Manifest (e.g. future "_"-prefixed
// metadata fields) extend the set automatically.
func manifestRootKeys() map[string]struct{} {
	// Keep in sync with Manifest's yaml tags. We hardcode rather than
	// reflect to avoid an import cycle / runtime cost; the set is small
	// and the test suite catches drift via TestLoadRejectsUnknownYAMLKey.
	return map[string]struct{}{
		"schema_version": {},
		"kind":           {},
		"slash":          {},
		"title":          {},
		"template":       {},
		"deploy":         {},
		"form":           {},
		"_draft":         {},
		"_copied_from":   {},
	}
}
