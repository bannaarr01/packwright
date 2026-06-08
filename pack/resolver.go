package pack

import (
	"strings"

	"github.com/bannaarr01/packwright/manifest"
)

// Resolution pairs one matching manifest with its provenance for the conflict
// resolver. The first element of Resolve's return slice is the default — the
// command that would run if the user typed the bare slash. Subsequent entries
// give the palette enough information to render the suffix UI described in
// ADR-0023 ("/alb (acme-platform)", "/alb (reference-pack)", etc.).
type Resolution struct {
	// Manifest is the matched manifest. Never nil for entries Resolve returns.
	Manifest *manifest.Manifest
	// Scope is ScopeUser for user-scope matches, ScopePack otherwise.
	Scope Scope
	// SourcePack is the pack name a ScopePack match was loaded from. Empty
	// for ScopeUser entries, mirroring Tagged.SourcePack.
	SourcePack string
}

// Resolve returns every manifest in packs whose Slash equals q.Slash, ordered
// per ADR-0023:
//
//  1. If q.Pack is non-empty, the result is restricted to that source —
//     UserScopeName for the user scope, otherwise the pack with that Name.
//     Within the filter, matches are returned in the order they appear in
//     packs (the same order the registry observes).
//  2. Otherwise, if pin names a source that has a match, that match leads.
//     Pin format is "user" or "pack:<name>"; malformed pins are tolerated as
//     "unpinned" so a stale entry from a removed pack does not break Resolve.
//  3. User-scope matches follow, in input order.
//  4. Pack-scope matches follow in reverse input order — most-recently-added
//     first. The Discover stage sorts packs lexically, so this corresponds to
//     a deterministic "last alphabetically wins" tiebreak; pack installation
//     UX (MVP-4) will reorder the input slice to reflect install time so the
//     ADR's "most-recently-added pack" rule applies in production.
//
// An empty q.Slash, a nil packs slice, or no matches all yield nil. The
// returned slice is freshly allocated — callers may mutate it.
func Resolve(packs []*Pack, q Qualified, pin string) []Resolution {
	if q.Slash == "" {
		return nil
	}

	var (
		userMatches []Resolution
		byPack      = make(map[string][]Resolution)
		packOrder   []string
		seen        = make(map[string]bool)
	)
	for _, p := range packs {
		if p == nil {
			continue
		}
		scope := ScopePack
		sourceName := p.Name
		if p.Name == UserScopeName {
			scope = ScopeUser
			sourceName = ""
		}
		for _, m := range p.Manifests {
			if m == nil || m.Slash != q.Slash {
				continue
			}
			r := Resolution{Manifest: m, Scope: scope, SourcePack: sourceName}
			if scope == ScopeUser {
				userMatches = append(userMatches, r)
				continue
			}
			if !seen[p.Name] {
				seen[p.Name] = true
				packOrder = append(packOrder, p.Name)
			}
			byPack[p.Name] = append(byPack[p.Name], r)
		}
	}

	// Qualified invocation short-circuits the priority logic: ADR-0023 treats
	// "/alb@<pack>" as an explicit request, so pins and tiebreaks do not apply.
	if q.Pack != "" {
		if q.Pack == UserScopeName {
			return cloneResolutions(userMatches)
		}
		return cloneResolutions(byPack[q.Pack])
	}

	var out []Resolution

	// Pin promotion. A pin that points at a source with no current match is a
	// no-op — that branch keeps Resolve correct after `packs remove` even when
	// the caller has not yet cleaned the pin from disk.
	pinScope, pinName, pinOK := parsePin(pin)
	if pinOK {
		switch pinScope {
		case ScopeUser:
			if len(userMatches) > 0 {
				out = append(out, userMatches[0])
				userMatches = userMatches[1:]
			}
		case ScopePack:
			if matches := byPack[pinName]; len(matches) > 0 {
				out = append(out, matches[0])
				byPack[pinName] = matches[1:]
			}
		}
	}

	out = append(out, userMatches...)

	// Pack-scope in reverse insertion order — ADR-0023's "most-recently-added
	// pack > older packs" tiebreak.
	for i := len(packOrder) - 1; i >= 0; i-- {
		out = append(out, byPack[packOrder[i]]...)
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// parsePin interprets a value from Config.PinnedDefaults:
//
//   - "user"        → (ScopeUser, "", true)
//   - "pack:<name>" → (ScopePack, <name>, true) for non-empty <name>
//   - anything else → (_, _, false)
//
// Resolve treats an ok=false pin as unpinned so a malformed config entry
// degrades gracefully instead of routing every collision to "no winner".
func parsePin(value string) (Scope, string, bool) {
	if value == string(ScopeUser) {
		return ScopeUser, "", true
	}
	if rest, ok := strings.CutPrefix(value, string(ScopePack)+":"); ok && rest != "" {
		return ScopePack, rest, true
	}
	return "", "", false
}

func cloneResolutions(s []Resolution) []Resolution {
	if len(s) == 0 {
		return nil
	}
	out := make([]Resolution, len(s))
	copy(out, s)
	return out
}
