package manifest

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"
)

// baseEnvAllow is the always-on whitelist for the `env` template function. A
// pack may extend this via Context.EnvAllow (sourced from pack.yaml's
// template_env_allow). The set is intentionally tiny — names every shell
// already lets the user inspect — so manifests cannot probe arbitrary
// environment variables (ADR-0026 § env whitelist).
var baseEnvAllow = map[string]struct{}{
	"USER":        {},
	"HOME":        {},
	"AWS_PROFILE": {},
	"AWS_REGION":  {},
}

// funcNames is the union of curated DSL functions and the safe text/template
// stdlib built-ins. ValidateTemplate uses it to reject identifier references
// outside the set. Keep in sync with funcMap below.
var funcNames = map[string]struct{}{
	// Curated DSL (ADR-0026).
	"upper":        {},
	"lower":        {},
	"default":      {},
	"replace":      {},
	"trim":         {},
	"trimL":        {},
	"trimR":        {},
	"slugify":      {},
	"env":          {},
	"pack":         {},
	"timestamp":    {},
	"requireField": {},
	// Safe text/template stdlib built-ins (no I/O, pure functions).
	"and":      {},
	"call":     {},
	"html":     {},
	"index":    {},
	"slice":    {},
	"js":       {},
	"len":      {},
	"not":      {},
	"or":       {},
	"print":    {},
	"printf":   {},
	"println":  {},
	"urlquery": {},
	"eq":       {},
	"ge":       {},
	"gt":       {},
	"le":       {},
	"lt":       {},
	"ne":       {},
}

// upperFn uppercases s.
func upperFn(s string) string { return strings.ToUpper(s) }

// lowerFn lowercases s.
func lowerFn(s string) string { return strings.ToLower(s) }

// trimFn strips leading and trailing whitespace from s.
func trimFn(s string) string { return strings.TrimSpace(s) }

// trimLFn strips the leading occurrences of any rune in cutset from s.
// Pipeline shape: {{ .X | trimL " /" }} (cutset first, value last).
func trimLFn(cutset, s string) string { return strings.TrimLeft(s, cutset) }

// trimRFn strips the trailing occurrences of any rune in cutset from s.
// Pipeline shape: {{ .X | trimR " /" }} (cutset first, value last).
func trimRFn(cutset, s string) string { return strings.TrimRight(s, cutset) }

// replaceFn replaces all occurrences of old with new in s. Pipeline shape:
// {{ .X | replace "a" "b" }} — value comes in last as Go templates require.
func replaceFn(old, new, s string) string { return strings.ReplaceAll(s, old, new) }

// defaultFn returns fallback when value is empty (nil, empty string,
// zero-length slice/map/array, or a nil pointer/interface), otherwise value.
// Pipeline shape: {{ .X | default "fallback" }} — value comes in last.
func defaultFn(fallback, value any) any {
	if isEmpty(value) {
		return fallback
	}
	return value
}

// isEmpty reports whether v should trigger the `default` fallback. Numeric
// zero and boolean false are *not* treated as empty — those are real values
// authors may want to keep.
func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String, reflect.Slice, reflect.Array, reflect.Map:
		return rv.Len() == 0
	case reflect.Ptr, reflect.Interface:
		return rv.IsNil()
	}
	return false
}

// slugifyFn lowercases s and collapses runs of non-[a-z0-9] characters into a
// single '-', then trims any leading or trailing dashes. Suitable for stack
// names, S3 keys, and other URL/resource-safe identifiers.
func slugifyFn(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	needDash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if needDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			needDash = false
			b.WriteRune(r)
			continue
		}
		needDash = true
	}
	return b.String()
}

// requireFieldFn fails the template render if value is empty, mentioning
// name so the user can tell which input was missing. Pipeline shape:
// {{ .X | requireField "X" }} — value comes in last.
func requireFieldFn(name string, value any) (any, error) {
	if isEmpty(value) {
		return nil, fmt.Errorf("requireField: field %q is empty", name)
	}
	return value, nil
}

// envFn returns a template function that reads os.Getenv only for names in
// allow. Names outside allow return an error: this is the single, audited
// surface through which manifest templates touch the process environment.
func envFn(allow map[string]struct{}) func(string) (string, error) {
	return func(name string) (string, error) {
		if _, ok := allow[name]; !ok {
			return "", fmt.Errorf("env: %q is not in the template env whitelist", name)
		}
		return os.Getenv(name), nil
	}
}

// packFn returns a template function that resolves a pack name to its
// absolute filesystem path via the provided lookup. Unknown names error
// rather than silently returning empty so author typos surface immediately.
func packFn(packs map[string]string) func(string) (string, error) {
	return func(name string) (string, error) {
		p, ok := packs[name]
		if !ok {
			return "", fmt.Errorf("pack: %q is not a known pack", name)
		}
		return p, nil
	}
}

// timestampFn returns a template function that formats now with the supplied
// layout — or defaultFormat, or time.RFC3339 as a last resort. A zero now
// falls back to time.Now().UTC() at call time; tests pin Now for
// determinism.
func timestampFn(now time.Time, defaultFormat string) func(...string) string {
	return func(format ...string) string {
		layout := defaultFormat
		if len(format) > 0 && format[0] != "" {
			layout = format[0]
		}
		if layout == "" {
			layout = time.RFC3339
		}
		t := now
		if t.IsZero() {
			t = time.Now().UTC()
		}
		return t.Format(layout)
	}
}
