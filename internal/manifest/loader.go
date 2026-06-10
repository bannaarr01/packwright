package manifest

import (
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// Load reads, strict-decodes, and validates the manifest YAML at path. It
// returns the decoded Manifest on success; on failure it returns a non-nil
// error from one of three layers:
//
//   - the filesystem (wrapped with %w so errors.Is(err, os.ErrNotExist)
//     still works);
//   - the YAML decoder (when the document has unknown keys, type mismatches,
//     is malformed, or contains more than one YAML document — KnownFields(true)
//     makes unknown keys an error);
//   - this package's validator (returns a *ValidationError describing which
//     field path failed and why).
//
// Load does not check whether the manifest's kind is executable at runtime;
// call CanRun on the returned manifest for that.
func Load(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("manifest: open %s: %w", path, err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)

	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: decode %s: %w", path, err)
	}

	// A manifest file describes exactly one action; a second YAML document
	// would be silently dropped without this check.
	var extra any
	switch err := dec.Decode(&extra); {
	case errors.Is(err, io.EOF):
		// expected: exactly one document
	case err == nil:
		return nil, fmt.Errorf("manifest: %s: contains multiple YAML documents", path)
	default:
		return nil, fmt.Errorf("manifest: decode %s: %w", path, err)
	}

	if err := schemaVersionCheck(&m); err != nil {
		return nil, fmt.Errorf("manifest: %s: %w", path, err)
	}
	if err := Validate(&m); err != nil {
		return nil, fmt.Errorf("manifest: validate %s: %w", path, err)
	}
	return &m, nil
}
