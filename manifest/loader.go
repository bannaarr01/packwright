package manifest

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Load reads and strictly decodes the YAML manifest at path. Unknown YAML keys
// are rejected so authors learn about typos at load time rather than
// discovering silent omissions at run time.
//
// PR-05 extends Load with cross-field validation (Validate); for now it only
// performs structural parsing of the fields declared on Manifest.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading manifest %q: %w", path, err)
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parsing manifest %q: %w", path, err)
	}
	// Record where the manifest came from so callers can resolve its relative
	// template / script paths against the containing directory. yaml:"-" keeps
	// Source out of the decoded schema, so this is the only writer.
	m.Source = path
	return &m, nil
}
