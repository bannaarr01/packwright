package pricing

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Pricing is the cost of one (provider, model) pair, expressed as USD
// per 1000 input and output tokens. The fields match schema.json
// verbatim so a JSON document and a Go struct can round-trip without
// rename gymnastics. A zero rate is valid (Ollama runs locally and has
// no per-token cost) and is preserved through the Table.
type Pricing struct {
	Provider    string  `json:"provider"`
	Model       string  `json:"model"`
	InputPer1K  float64 `json:"input_per_1k"`
	OutputPer1K float64 `json:"output_per_1k"`
	Currency    string  `json:"currency,omitempty"`
}

// Key is the canonical "<provider>/<model>" lookup key used by the
// runtime. Producing it through this helper rather than letting each
// caller assemble the string in-line keeps the separator and casing
// rules in one place.
func Key(provider, model string) string {
	return strings.ToLower(provider + "/" + model)
}

// LookupKey returns the canonical key for p.
func (p Pricing) LookupKey() string { return Key(p.Provider, p.Model) }

// EstimateUSD returns the cost in USD of a call with the given input
// and (budgeted) output token counts. Negative token counts are
// treated as zero so callers can pass an unbudgeted output as 0
// without producing negative estimates.
func (p Pricing) EstimateUSD(tokensIn, tokensOut int) float64 {
	if tokensIn < 0 {
		tokensIn = 0
	}
	if tokensOut < 0 {
		tokensOut = 0
	}
	return float64(tokensIn)/1000*p.InputPer1K + float64(tokensOut)/1000*p.OutputPer1K
}

// ErrUnknownModel is returned by [Table.Lookup] when no entry matches
// the requested (provider, model) pair.
var ErrUnknownModel = errors.New("pricing: unknown provider/model")

// Table is an in-memory pricing catalogue keyed by canonical
// "<provider>/<model>" strings. Overrides supplied by the user (via
// the ai.pricing config block) replace the embedded value for the
// same key — same data path, same validation, same semantics.
//
// Table is safe for concurrent reads after construction; Override
// must not race with Lookup.
type Table struct {
	entries map[string]Pricing
	schema  *schema
}

// LoadEmbedded parses every shipped pricing JSON, validates it against
// the embedded schema, and returns a populated [Table]. A malformed or
// schema-violating shipped file fails the call — these are checked in
// at build time, so any problem is a release-time bug, not a runtime
// surprise.
func LoadEmbedded() (*Table, error) {
	rawSchema, err := embeddedFS.ReadFile(embeddedSchemaName)
	if err != nil {
		return nil, fmt.Errorf("pricing: load schema: %w", err)
	}
	sch, err := parseSchema(rawSchema)
	if err != nil {
		return nil, err
	}
	t := &Table{
		entries: make(map[string]Pricing),
		schema:  sch,
	}
	entries, err := fs.ReadDir(embeddedFS, ".")
	if err != nil {
		return nil, fmt.Errorf("pricing: enumerate embedded: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == embeddedSchemaName || !strings.HasSuffix(name, ".json") {
			continue
		}
		raw, err := embeddedFS.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("pricing: read %s: %w", name, err)
		}
		p, err := t.decodeAndValidate(raw)
		if err != nil {
			return nil, fmt.Errorf("pricing: %s: %w", name, err)
		}
		t.entries[p.LookupKey()] = p
	}
	return t, nil
}

// decodeAndValidate decodes raw, runs it through the schema validator,
// and returns the typed Pricing. Both passes are required: a JSON
// document can be syntactically valid yet semantically wrong (e.g.
// negative price, missing model), and a Go-typed unmarshal silently
// zeroes missing fields.
func (t *Table) decodeAndValidate(raw []byte) (Pricing, error) {
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return Pricing{}, fmt.Errorf("decode: %w", err)
	}
	if err := t.schema.validate(generic); err != nil {
		return Pricing{}, err
	}
	var p Pricing
	if err := json.Unmarshal(raw, &p); err != nil {
		return Pricing{}, fmt.Errorf("decode typed: %w", err)
	}
	return p, nil
}

// Lookup returns the pricing for (provider, model). The match is
// case-insensitive on both keys so config files can use whichever
// casing the upstream documentation prefers.
func (t *Table) Lookup(provider, model string) (Pricing, error) {
	if p, ok := t.entries[Key(provider, model)]; ok {
		return p, nil
	}
	return Pricing{}, fmt.Errorf("%w: %s/%s", ErrUnknownModel, provider, model)
}

// Override installs p in the table, replacing any existing entry with
// the same canonical key. p is validated against the schema before it
// is installed — so a user-supplied override that omits a required
// field, uses a negative price, or otherwise violates the schema is
// rejected with a descriptive error rather than silently corrupting
// the table.
func (t *Table) Override(p Pricing) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("pricing: marshal override: %w", err)
	}
	validated, err := t.decodeAndValidate(raw)
	if err != nil {
		return err
	}
	t.entries[validated.LookupKey()] = validated
	return nil
}

// Keys returns every canonical key in the table, sorted lexically.
// Useful for the /ai setup wizard's model picker and for diagnostics.
func (t *Table) Keys() []string {
	out := make([]string, 0, len(t.entries))
	for k := range t.entries {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
