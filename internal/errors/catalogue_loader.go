package errors

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// catalogueFS embeds every YAML file under catalogue/ into the binary at
// build time. Files outside the catalogue directory are not embedded; a new
// pattern is added by dropping a single .yaml file in there.
//
//go:embed catalogue/*.yaml
var catalogueFS embed.FS

// catalogueOnce guards the package-level catalogue cache. The catalogue is
// embedded data — it never changes at runtime — so we load and compile it
// once on first use and reuse the slice for every Match call.
var (
	catalogueOnce sync.Once
	catalogue     []*Entry
	catalogueErr  error
)

// LoadCatalogue parses every embedded catalogue YAML file, compiles each
// entry, and returns them sorted in Match order (descending priority,
// ascending id). It is safe to call multiple times — the result is memoised
// after the first successful call.
//
// LoadCatalogue is exported so tests and authoring tools can walk the
// catalogue without going through Match; production code should call Match
// or FromFailedStack instead.
func LoadCatalogue() ([]*Entry, error) {
	catalogueOnce.Do(func() {
		catalogue, catalogueErr = parseEmbeddedCatalogue(catalogueFS, "catalogue")
	})
	return catalogue, catalogueErr
}

// loadedCatalogue is the internal hot-path accessor used by Match. It hides
// the error from callers — a corrupt catalogue is a developer bug that the
// build pipeline catches via the tests, not a runtime concern; if it
// somehow makes it to production we degrade to an empty catalogue, which
// turns Match into the fallback path on every call.
func loadedCatalogue() []*Entry {
	entries, err := LoadCatalogue()
	if err != nil {
		return nil
	}
	return entries
}

// parseEmbeddedCatalogue walks dir on fsys, decoding every *.yaml file into
// an entrySpec and compiling it. It returns entries already sorted in Match
// order. Errors include the offending filename and are surfaced verbatim:
// the first malformed entry stops the load so the contributor sees one
// error at a time instead of a cascade.
func parseEmbeddedCatalogue(fsys fs.FS, dir string) ([]*Entry, error) {
	dents, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("errors: reading catalogue dir %q: %w", dir, err)
	}

	var out []*Entry
	seenID := map[string]string{}

	for _, d := range dents {
		if d.IsDir() {
			continue
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		full := path.Join(dir, name)

		body, err := fs.ReadFile(fsys, full)
		if err != nil {
			return nil, fmt.Errorf("errors: reading %s: %w", full, err)
		}

		var spec entrySpec
		dec := yaml.NewDecoder(bytes.NewReader(body))
		dec.KnownFields(true)
		if err := dec.Decode(&spec); err != nil {
			return nil, fmt.Errorf("errors: decoding %s: %w", full, err)
		}
		spec.source = full

		entry, err := compileEntry(spec)
		if err != nil {
			return nil, err
		}

		if prev, dup := seenID[entry.ID]; dup {
			return nil, fmt.Errorf("errors: %s: duplicate id %q (also defined in %s)", full, entry.ID, prev)
		}
		seenID[entry.ID] = full
		out = append(out, entry)
	}

	sortEntries(out)
	return out, nil
}
