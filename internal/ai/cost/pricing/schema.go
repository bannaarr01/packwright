package pricing

import (
	"encoding/json"
	"fmt"
)

// schema is the in-memory representation of the JSON-Schema subset we
// support. Only the fields documented in schema.json are honoured;
// anything else in a schema document is ignored, on the theory that
// silently widening our schema surface would be more surprising than
// strictness. The supported fields, mirroring schema.json:
//
//   - type: "object" | "string" | "number" | "integer" | "boolean"
//   - required: list of property names that must be present on objects
//   - additionalProperties: false to forbid keys outside properties
//   - properties: per-key sub-schemas
//   - minimum: numeric lower bound (inclusive)
//   - minLength: string lower bound (inclusive)
type schema struct {
	Type                 string             `json:"type"`
	Required             []string           `json:"required"`
	AdditionalProperties *bool              `json:"additionalProperties"`
	Properties           map[string]*schema `json:"properties"`
	Minimum              *float64           `json:"minimum"`
	MinLength            *int               `json:"minLength"`
}

// parseSchema decodes the embedded schema.json into a [schema] tree.
// A malformed schema is a programmer error — the file is hand-authored
// and embedded at build time — so callers fail-fast.
func parseSchema(raw []byte) (*schema, error) {
	var s schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("pricing: parse schema: %w", err)
	}
	return &s, nil
}

// validate checks doc against s and returns the first violation, with
// a path showing which field failed (e.g. "input_per_1k: below minimum
// 0"). doc is the decoded JSON value (the output of json.Unmarshal
// into any), not the raw bytes — the validator runs after a successful
// generic decode so it can reason about value types directly.
func (s *schema) validate(doc any) error {
	return s.validateAt(doc, "")
}

func (s *schema) validateAt(v any, path string) error {
	if err := checkType(s.Type, v, path); err != nil {
		return err
	}
	switch s.Type {
	case "object":
		obj, _ := v.(map[string]any)
		// Required keys.
		for _, key := range s.Required {
			if _, ok := obj[key]; !ok {
				return errAt(path, fmt.Sprintf("missing required field %q", key))
			}
		}
		// additionalProperties:false rejects keys outside Properties.
		if s.AdditionalProperties != nil && !*s.AdditionalProperties {
			for k := range obj {
				if _, ok := s.Properties[k]; !ok {
					return errAt(path, fmt.Sprintf("unexpected field %q", k))
				}
			}
		}
		// Validate each declared property that's present.
		for name, sub := range s.Properties {
			child, ok := obj[name]
			if !ok {
				continue
			}
			if err := sub.validateAt(child, joinPath(path, name)); err != nil {
				return err
			}
		}
	case "string":
		str, _ := v.(string)
		if s.MinLength != nil && len(str) < *s.MinLength {
			return errAt(path, fmt.Sprintf("string shorter than minLength %d", *s.MinLength))
		}
	case "number", "integer":
		// json.Unmarshal decodes any-typed numbers into float64.
		n, _ := v.(float64)
		if s.Minimum != nil && n < *s.Minimum {
			return errAt(path, fmt.Sprintf("below minimum %v", *s.Minimum))
		}
	}
	return nil
}

// checkType returns an error when v does not match the JSON-Schema
// type label. An empty type label means "no constraint" and matches
// every value.
func checkType(typ string, v any, path string) error {
	if typ == "" {
		return nil
	}
	switch typ {
	case "object":
		if _, ok := v.(map[string]any); !ok {
			return errAt(path, "expected object")
		}
	case "string":
		if _, ok := v.(string); !ok {
			return errAt(path, "expected string")
		}
	case "number":
		if _, ok := v.(float64); !ok {
			return errAt(path, "expected number")
		}
	case "integer":
		f, ok := v.(float64)
		if !ok || f != float64(int64(f)) {
			return errAt(path, "expected integer")
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return errAt(path, "expected boolean")
		}
	default:
		return errAt(path, fmt.Sprintf("unknown schema type %q", typ))
	}
	return nil
}

func errAt(path, msg string) error {
	if path == "" {
		return fmt.Errorf("pricing: schema: %s", msg)
	}
	return fmt.Errorf("pricing: schema: %s: %s", path, msg)
}

func joinPath(base, leaf string) string {
	if base == "" {
		return leaf
	}
	return base + "." + leaf
}
