// This file implements the Packwright logging redactor per ADR-0018. The
// redactor scans the bytes a slog handler produces and replaces matches of
// well-known secret shapes (AWS access keys, JWTs, Bearer headers, secret-
// key / session-token contexts) and sensitive-named JSON / text-format
// fields with `<redacted:HINT>` markers. It is wired into the log package
// via the package-level Redact hook declared in handler.go.

package log

import (
	"regexp"
	"strings"
	"sync"
)

// Redactor scrubs sensitive substrings from log output bytes before they
// reach disk. The built-in pattern set (see patterns.go) is frozen at
// construction time; the user-marked field set is mutable via
// MarkSecretField and is safe for concurrent use.
type Redactor struct {
	builtins []pattern // immutable after construction

	mu     sync.RWMutex
	fields []pattern           // patterns built from MarkSecretField calls
	marked map[string]struct{} // lowercase field names already registered
}

// NewDefaultRedactor returns a Redactor seeded with the ADR-0018 pattern
// set. Most callers want this rather than building a custom Redactor.
func NewDefaultRedactor() *Redactor {
	return &Redactor{
		builtins: defaultPatterns(),
		marked:   make(map[string]struct{}),
	}
}

// Apply returns p with every built-in and user-marked pattern redacted.
// The return value may share storage with p when no pattern matched. Apply
// is safe for concurrent use and never panics on invalid UTF-8 — the
// patterns operate on bytes, not runes, and Go's regexp engine treats
// unmatched bytes as literal one-byte characters.
func (r *Redactor) Apply(p []byte) []byte {
	out := p
	for i := range r.builtins {
		out = r.builtins[i].apply(out)
	}
	r.mu.RLock()
	for i := range r.fields {
		out = r.fields[i].apply(out)
	}
	r.mu.RUnlock()
	return out
}

// MarkSecretField registers field as a name whose values must be redacted
// in every subsequent log write. Names are matched case-insensitively as
// exact JSON keys and as slog text-format keys (`key=value`); the value is
// replaced with `<redacted:form_secret>` while the key itself is preserved
// so log readers can still see which attribute was scrubbed.
//
// Empty and duplicate registrations are silently ignored. Safe for
// concurrent use.
func (r *Redactor) MarkSecretField(field string) {
	if field == "" {
		return
	}
	name := strings.ToLower(field)

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.marked[name]; ok {
		return
	}
	r.marked[name] = struct{}{}

	quoted := regexp.QuoteMeta(name)
	jsonRE := regexp.MustCompile(
		`((?i)"` + quoted + `"\s*:\s*)` +
			`(?:"(?:[^"\\]|\\.)*"|true|false|null|-?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?)`,
	)
	textRE := regexp.MustCompile(
		`((?i)\b` + quoted + `=)` +
			`("(?:[^"\\]|\\.)*"|\S+)`,
	)
	r.fields = append(r.fields,
		fieldRepl(jsonRE, "form_secret", true),
		fieldRepl(textRE, "form_secret", false),
	)
}

// defaultRedactor is the package-level redactor wired into log.Redact by
// init below. It exists so MarkSecretField (the package-level function)
// has a stable instance to extend, separate from any redactor a caller
// builds for testing.
var defaultRedactor = NewDefaultRedactor()

// MarkSecretField extends the package-default redactor with a runtime
// field name. It forwards to defaultRedactor.MarkSecretField; see that
// method for details.
func MarkSecretField(field string) {
	defaultRedactor.MarkSecretField(field)
}

func init() {
	Redact = defaultRedactor.Apply
}
