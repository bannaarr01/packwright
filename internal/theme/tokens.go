package theme

import (
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
)

// tokenFiles bundles the JSON token files and the schema that validates them
// into the binary. The Svelte/Vite build (PR-09) reads dark.json and light.json
// directly from disk; this package reads the same files via go:embed so the
// TUI cannot drift from the GUI palette.
//
//go:embed tokens/dark.json tokens/light.json tokens/schema.json
var tokenFiles embed.FS

// Tokens is the semantic palette every UI surface speaks. Field names are the
// Go-side spelling of the snake_case keys in the JSON files. The set is
// intentionally small: every token must have a clear semantic role.
type Tokens struct {
	BG          string `json:"bg"`
	FG          string `json:"fg"`
	Muted       string `json:"muted"`
	Accent      string `json:"accent"`
	AccentAlt   string `json:"accent_alt"`
	Warn        string `json:"warn"`
	Error       string `json:"error"`
	Success     string `json:"success"`
	Border      string `json:"border"`
	SelectionBG string `json:"selection_bg"`
	SelectionFG string `json:"selection_fg"`
}

// Load returns the palette for mode m. It must be a concrete mode (ModeDark or
// ModeLight) — callers should resolve ModeAuto first via Resolve. The returned
// Tokens have been validated against the embedded JSON schema; a non-nil error
// means a token file is missing, malformed, or omits a required key.
func Load(m Mode) (Tokens, error) {
	if !m.IsConcrete() {
		return Tokens{}, errUnknownMode(m)
	}
	path := "tokens/" + string(m) + ".json"
	raw, err := tokenFiles.ReadFile(path)
	if err != nil {
		return Tokens{}, fmt.Errorf("theme: reading %s: %w", path, err)
	}
	if err := validateTokens(raw); err != nil {
		return Tokens{}, fmt.Errorf("theme: validating %s: %w", path, err)
	}
	var t Tokens
	if err := json.Unmarshal(raw, &t); err != nil {
		return Tokens{}, fmt.Errorf("theme: decoding %s: %w", path, err)
	}
	return t, nil
}

// init validates the embedded token files at startup so an ill-formed palette
// fails fast at process start rather than the first time a screen renders.
// This mirrors the acceptance criterion in PR-07 ("JSON token files validate
// against an embedded JSON schema").
func init() {
	for _, m := range []Mode{ModeDark, ModeLight} {
		if _, err := Load(m); err != nil {
			panic(err)
		}
	}
}

// --- minimal JSON-schema-driven validator -----------------------------------
//
// We do not depend on a full JSON Schema engine (the project rule is "minimal
// dependencies"). Instead we read just the parts of the schema we use —
// `required` and per-property `pattern` — and apply them ourselves. That is
// enough to satisfy the acceptance criterion and keeps the rules in tokens/
// schema.json as the single source of truth shared with anyone validating
// these files from another language.

type schemaDoc struct {
	Required   []string                       `json:"required"`
	Properties map[string]schemaPropertyEntry `json:"properties"`
}

type schemaPropertyEntry struct {
	Type    string `json:"type"`
	Pattern string `json:"pattern"`
}

// compiledSchema is the schema parsed once at init time. validateTokens runs
// every property's pattern against the corresponding value in the token file.
type compiledSchema struct {
	required []string
	patterns map[string]*regexp.Regexp
}

var schema = mustCompileSchema()

func mustCompileSchema() compiledSchema {
	raw, err := tokenFiles.ReadFile("tokens/schema.json")
	if err != nil {
		panic(fmt.Errorf("theme: reading embedded schema: %w", err))
	}
	var doc schemaDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		panic(fmt.Errorf("theme: parsing embedded schema: %w", err))
	}
	patterns := make(map[string]*regexp.Regexp, len(doc.Properties))
	for key, entry := range doc.Properties {
		if entry.Pattern == "" {
			continue
		}
		re, err := regexp.Compile(entry.Pattern)
		if err != nil {
			panic(fmt.Errorf("theme: compiling pattern for %q: %w", key, err))
		}
		patterns[key] = re
	}
	return compiledSchema{required: doc.Required, patterns: patterns}
}

// validateTokens checks raw token JSON against the embedded schema. It enforces
// three rules in order, each producing a deterministic error message:
//
//  1. Every required key is present.
//  2. No extra keys appear (additionalProperties: false).
//  3. Every value matches the per-key pattern (a six-digit hex colour).
//
// Errors are sorted so that a file with multiple problems always reports them
// in the same order — important for stable diagnostics and test output.
func validateTokens(raw []byte) error {
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		return fmt.Errorf("not a JSON object: %w", err)
	}

	var missing []string
	for _, key := range schema.required {
		if _, ok := got[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("missing required keys: %v", missing)
	}

	var extras []string
	for key := range got {
		if _, ok := schema.patterns[key]; !ok {
			extras = append(extras, key)
		}
	}
	if len(extras) > 0 {
		sort.Strings(extras)
		return fmt.Errorf("unexpected keys (schema declares additionalProperties:false): %v", extras)
	}

	// Iterate schema.required (a slice) rather than schema.patterns (a map) so
	// the first reported pattern violation is deterministic across runs.
	for _, key := range schema.required {
		re, ok := schema.patterns[key]
		if !ok {
			continue
		}
		v := got[key]
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("key %q: expected string, got %T", key, v)
		}
		if !re.MatchString(s) {
			return fmt.Errorf("key %q: value %q does not match pattern %s", key, s, re.String())
		}
	}
	return nil
}
