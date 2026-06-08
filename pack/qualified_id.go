package pack

import (
	"fmt"
	"strings"
)

// Qualified is a slash command optionally pinned to a specific source. The
// zero value is invalid; callers construct one with ParseQualified or by
// setting Slash directly.
//
// Slash carries its leading '/' so it round-trips against manifest.Slash and
// Registry.Lookup without rewriting. Pack is the explicit source named after
// the '@' separator in a qualified invocation, or empty when the user typed
// the bare slash. The reserved Pack value "user" addresses the user scope —
// the same string Tag emits as the synthetic pack name (see UserScopeName).
type Qualified struct {
	// Slash is the command's slash form, including the leading '/'.
	Slash string
	// Pack is the explicit source named after '@', or empty for the bare
	// slash. Use UserScopeName ("user") to address the user scope.
	Pack string
}

// ParseQualified parses a qualified-invocation string of the form "/slash" or
// "/slash@pack". The leading '/' is required; the slash portion must be
// non-empty after that '/'; the pack portion, if present, must be non-empty
// after the '@'. The first '@' is the separator — any subsequent '@' is
// retained as part of the pack name so pack identifiers are not silently
// truncated.
//
// The composite-manifest example from ADR-0023 ("run: /alb@acme-platform")
// motivates this format: a pack author writes the qualified form verbatim and
// expects ParseQualified to round-trip it via the String method below.
func ParseQualified(s string) (Qualified, error) {
	if s == "" {
		return Qualified{}, fmt.Errorf("qualified id: empty input")
	}
	if s[0] != '/' {
		return Qualified{}, fmt.Errorf("qualified id %q: missing leading '/'", s)
	}

	slashPart, packPart, hasAt := strings.Cut(s, "@")
	if len(slashPart) < 2 {
		// "/" or "/@..." — no slash command name.
		return Qualified{}, fmt.Errorf("qualified id %q: empty slash command", s)
	}
	q := Qualified{Slash: slashPart}
	if hasAt {
		if packPart == "" {
			return Qualified{}, fmt.Errorf("qualified id %q: empty pack name after '@'", s)
		}
		q.Pack = packPart
	}
	return q, nil
}

// String reverses ParseQualified: it renders the Qualified as "/slash" or
// "/slash@pack". The zero Qualified renders as the empty string; round-trip
// is preserved for any value produced by ParseQualified.
func (q Qualified) String() string {
	if q.Slash == "" {
		return ""
	}
	if q.Pack == "" {
		return q.Slash
	}
	return q.Slash + "@" + q.Pack
}
