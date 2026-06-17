// Package pricing owns Packwright's embedded LLM pricing tables. Each
// table is a small JSON document, one per (provider, model) pair, that
// declares the input and output prices per 1000 tokens. The tables are
// validated at process start against an embedded JSON-Schema subset so
// a malformed entry — whether shipped by us or supplied by the user as
// an override — fails loudly instead of silently producing zero-cost
// estimates.
//
// Users can override or extend the embedded set via `ai.pricing.<provider>.<model>`
// in config.yaml (see ADR-0039 §"Pricing source"); overrides go through
// the same validator as the embedded files. The pricing package does
// not load config itself — the caller passes in already-parsed
// [Pricing] structs via [Table.Override].
package pricing

import "embed"

// embeddedFS holds the schema and every shipped pricing JSON.
//
//go:embed schema.json *.json
var embeddedFS embed.FS

// embeddedSchemaName is the on-disk filename of the JSON-Schema subset
// used to validate every pricing document, shipped and override alike.
const embeddedSchemaName = "schema.json"
