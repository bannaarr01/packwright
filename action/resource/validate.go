package resource

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bannaarr01/packwright/manifest"
)

// AZLookup returns the availability zone a subnet lives in. The distinct-az
// validator depends on this; production callers pass awsx.Client.SubnetAZ,
// tests pass an in-memory map.
type AZLookup func(ctx context.Context, subnetID string) (string, error)

// FieldError is a single validation failure attached to a form field.
type FieldError struct {
	Field   string
	Message string
}

// ValidationErrors is the aggregate validation result. It implements error so
// it can be returned from Execute, and Map() lets the TUI/GUI render
// per-field messages.
type ValidationErrors []FieldError

// Error renders the aggregate failure. Order is stable (sorted by field ID)
// so messages are reproducible across runs.
func (v ValidationErrors) Error() string {
	if len(v) == 0 {
		return ""
	}
	sorted := append(ValidationErrors(nil), v...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Field < sorted[j].Field })
	parts := make([]string, len(sorted))
	for i, e := range sorted {
		parts[i] = fmt.Sprintf("%s: %s", e.Field, e.Message)
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// Map returns the errors keyed by field ID. Multiple errors per field are
// joined with "; ".
func (v ValidationErrors) Map() map[string]string {
	out := make(map[string]string)
	for _, e := range v {
		if prev, ok := out[e.Field]; ok {
			out[e.Field] = prev + "; " + e.Message
		} else {
			out[e.Field] = e.Message
		}
	}
	return out
}

// Validate runs every validator declared by the manifest against inputs and
// returns the aggregate failure. A nil return means inputs are valid.
//
// The lookup may be nil; in that case the distinct-az validator is skipped
// rather than erroring, which keeps the engine usable in headless unit tests
// that don't exercise AZ-aware validation.
func Validate(
	ctx context.Context,
	m *manifest.Manifest,
	inputs Inputs,
	lookup AZLookup,
) ValidationErrors {
	if m == nil {
		return nil
	}
	var errs ValidationErrors
	for _, f := range m.Form {
		v, present := inputs[f.ID]
		if f.Required && !present {
			errs = append(errs, FieldError{f.ID, "is required"})
			continue
		}
		if !present {
			continue
		}
		errs = append(errs, validateField(ctx, f, v, lookup)...)
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

func validateField(ctx context.Context, f manifest.Field, value any, lookup AZLookup) []FieldError {
	var errs []FieldError

	if err := checkType(f, value); err != nil {
		return []FieldError{{f.ID, err.Error()}}
	}

	if err := checkEnum(f, value); err != nil {
		errs = append(errs, FieldError{f.ID, err.Error()})
	}
	if err := checkLength(f, value); err != nil {
		errs = append(errs, FieldError{f.ID, err.Error()})
	}

	for _, spec := range f.Validate {
		if err := runRule(ctx, f, value, spec, lookup); err != nil {
			msg := spec.Message
			if msg == "" {
				msg = err.Error()
			}
			errs = append(errs, FieldError{f.ID, msg})
		}
	}
	return errs
}

// checkType verifies the input's Go type matches the manifest-declared field
// type. The engine accepts the natural types each picker / widget produces
// (string for text inputs, []string for chip lists, etc.) — front-ends that
// pass through a mistyped value get a clear error rather than a downstream
// JSON-marshalling surprise.
func checkType(f manifest.Field, v any) error {
	switch f.Type {
	case manifest.TypeString, manifest.TypeSecret,
		manifest.TypeAWSVpcID, manifest.TypeAWSACMArn,
		manifest.TypeEnum:
		if _, ok := v.(string); !ok {
			return fmt.Errorf("expected string, got %T", v)
		}
	case manifest.TypeInt:
		switch v.(type) {
		case int, int32, int64:
		default:
			return fmt.Errorf("expected int, got %T", v)
		}
	case manifest.TypeBool:
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("expected bool, got %T", v)
		}
	case manifest.TypeMultistring, manifest.TypeAWSSubnetIDs, manifest.TypeAWSSGIDs:
		switch v.(type) {
		case []string, []any:
		default:
			return fmt.Errorf("expected []string, got %T", v)
		}
	default:
		// Unknown field type — let the manifest pass through; PR-05 owns
		// stricter schema validation at load time.
	}
	return nil
}

func checkEnum(f manifest.Field, v any) error {
	if f.Type != manifest.TypeEnum {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return nil // handled by checkType
	}
	if len(f.Values) == 0 {
		return fmt.Errorf("manifest error: enum field %q declares no values", f.ID)
	}
	for _, allowed := range f.Values {
		if s == allowed {
			return nil
		}
	}
	return fmt.Errorf("must be one of [%s]", strings.Join(f.Values, ", "))
}

// checkLength enforces Min / Max as length bounds for strings and counts for
// arrays. Numeric range-checking on int fields is a TODO until a numeric
// field is actually used by a manifest.
func checkLength(f manifest.Field, v any) error {
	if f.Min == nil && f.Max == nil {
		return nil
	}
	count := -1
	switch x := v.(type) {
	case string:
		count = len(x)
	case []string:
		count = len(x)
	case []any:
		count = len(x)
	}
	if count < 0 {
		return nil
	}
	entries := func(n int) string {
		if n == 1 {
			return "entry"
		}
		return "entries"
	}
	if f.Min != nil && count < *f.Min {
		return fmt.Errorf("must have at least %d %s", *f.Min, entries(*f.Min))
	}
	if f.Max != nil && count > *f.Max {
		return fmt.Errorf("must have at most %d %s", *f.Max, entries(*f.Max))
	}
	return nil
}

func runRule(
	ctx context.Context,
	f manifest.Field,
	value any,
	spec manifest.ValidatorSpec,
	lookup AZLookup,
) error {
	switch spec.Rule {
	case "distinct-az":
		ids, err := asStringSlice(value)
		if err != nil {
			return err
		}
		return distinctAZ(ctx, ids, lookup)
	default:
		// Unknown rule: surface as a configuration error so manifest
		// authors find typos quickly.
		return fmt.Errorf("unknown validator rule %q on field %q", spec.Rule, f.ID)
	}
}

// distinctAZ verifies that the subnets span at least two distinct
// availability zones. When lookup is nil the rule is skipped (no failure) —
// callers that want strict behaviour pass a non-nil lookup.
func distinctAZ(ctx context.Context, ids []string, lookup AZLookup) error {
	if lookup == nil || len(ids) == 0 {
		return nil
	}
	azs := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		az, err := lookup(ctx, id)
		if err != nil {
			return fmt.Errorf("looking up AZ for %s: %w", id, err)
		}
		azs[az] = struct{}{}
	}
	if len(azs) < 2 {
		return errors.New("subnets must span at least two availability zones")
	}
	return nil
}

func asStringSlice(v any) ([]string, error) {
	switch x := v.(type) {
	case []string:
		return x, nil
	case []any:
		out := make([]string, len(x))
		for i, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("element %d is %T, want string", i, e)
			}
			out[i] = s
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected []string, got %T", v)
	}
}
