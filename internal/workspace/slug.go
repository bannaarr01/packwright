package workspace

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// slugPattern is the lowercase kebab-case regex from ADR-0045. The leading
// character is constrained to [a-z0-9] so a slug always sorts predictably and
// is safe to use as a directory name on every supported filesystem. The
// upper bound (39 characters) keeps full paths well under the macOS / Linux
// component limits even after the project + env + manifest tail is appended.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,38}$`)

// ErrSlugInvalid is returned by ValidateSlug when a candidate does not match
// the slug pattern. Callers may wrap it with the offending string for the
// user-visible message.
var ErrSlugInvalid = errors.New("workspace: invalid slug (must match ^[a-z0-9][a-z0-9-]{0,38}$)")

// ErrSlugDuplicate is returned when a candidate slug collides with an
// existing slug after case-folding (the macOS-safe collision rule).
var ErrSlugDuplicate = errors.New("workspace: duplicate slug")

// NormalizeSlug lowercases s and trims surrounding whitespace. It does not
// substitute or strip invalid characters — "Acme" normalizes to "acme" (and
// passes ValidateSlug), whereas "Acme!" normalizes to "acme!" (and still
// fails). Use this as the first step at every input boundary, then
// ValidateSlug for the final guard.
func NormalizeSlug(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ValidateSlug returns nil iff s matches the slug regex. The returned error
// wraps ErrSlugInvalid so callers can match it with errors.Is.
func ValidateSlug(s string) error {
	if !slugPattern.MatchString(s) {
		return fmt.Errorf("%w: %q", ErrSlugInvalid, s)
	}
	return nil
}

// SlugExists reports whether candidate collides with any entry in existing
// after both sides are lowercased. Used by CreateProject / CreateEnv and by
// the slash-command layer to reject duplicates before any disk write.
func SlugExists(existing []string, candidate string) bool {
	cand := NormalizeSlug(candidate)
	for _, s := range existing {
		if NormalizeSlug(s) == cand {
			return true
		}
	}
	return false
}
