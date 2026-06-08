package pack

import "github.com/bannaarr01/packwright/manifest"

// Scope distinguishes user-private manifests from those shipped via a named
// pack. User-scope manifests live under <home>/commands and <home>/monitors;
// pack-scope manifests live under <home>/packs/<pack>/. The conflict resolver
// (MVP-3 PR-04) and the palette use the scope to render "user" or
// "pack: <name>" alongside each command.
type Scope string

// Recognised manifest scopes.
const (
	// ScopeUser identifies a manifest loaded from the user's private
	// collection at <home>/commands or <home>/monitors.
	ScopeUser Scope = "user"
	// ScopePack identifies a manifest loaded from a named pack under
	// <home>/packs/<pack>/.
	ScopePack Scope = "pack"
)

// UserScopeName is the synthetic Pack.Name LoadUserScope assigns to the
// user-private pack. Tag uses it to discriminate user-scope packs from
// pack-scope packs without needing a Scope field on Pack (Pack's shape is
// owned by an earlier PR).
const UserScopeName = "user"

// Tagged pairs a manifest with the scope and source pack it was loaded from.
// The conflict resolver and palette consume Tagged values so they can display
// each command's provenance without re-walking the pack list.
type Tagged struct {
	// Manifest is the parsed manifest. Never nil for entries Tag returns.
	Manifest *manifest.Manifest
	// Scope is ScopeUser when the manifest came from the user-scope pack,
	// ScopePack otherwise.
	Scope Scope
	// SourcePack is the name of the pack the manifest came from. Empty when
	// Scope is ScopeUser, since the user-scope pack is synthetic.
	SourcePack string
}

// Tag flattens packs into Tagged entries in the order the packs (and their
// manifests within each pack) were supplied — the same order NewRegistry
// observes. A pack whose Name equals UserScopeName is tagged with
// Scope=ScopeUser and SourcePack=""; all other packs produce Scope=ScopePack
// entries whose SourcePack is the pack's Name. Nil packs and nil manifests
// are skipped, matching NewRegistry's tolerance of partial discovery output.
func Tag(packs []*Pack) []Tagged {
	var out []Tagged
	for _, p := range packs {
		if p == nil {
			continue
		}
		scope := ScopePack
		source := p.Name
		if p.Name == UserScopeName {
			scope = ScopeUser
			source = ""
		}
		for _, m := range p.Manifests {
			if m == nil {
				continue
			}
			out = append(out, Tagged{Manifest: m, Scope: scope, SourcePack: source})
		}
	}
	return out
}
