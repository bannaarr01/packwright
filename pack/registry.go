package pack

import "github.com/bannaarr01/packwright/manifest"

// Registry is an in-memory index of manifests across one or more packs,
// keyed by slash command. A single slash may map to multiple manifests when
// packs collide; the data layer preserves the collision verbatim so the
// resolution UX (MVP-3 PR-04) can disambiguate.
type Registry struct {
	bySlash map[string][]*manifest.Manifest
	all     []*manifest.Manifest
}

// NewRegistry builds a Registry over the supplied packs. The order of packs
// is preserved: a manifest from an earlier pack precedes a colliding
// manifest from a later pack in Lookup's returned slice. Manifests within a
// single pack are inserted in the order Pack.Manifests stores them — i.e.
// lexical order of file name, as set by Discover.
//
// Callers wiring up MVP-1 typically prepend LoadUserScope's result so the
// user scope wins precedence:
//
//	user, _ := pack.LoadUserScope(home)
//	packs, _ := pack.Discover(home)
//	reg := pack.NewRegistry(append([]*pack.Pack{user}, packs...))
//
// A nil or empty packs slice yields an empty but usable Registry.
func NewRegistry(packs []*Pack) *Registry {
	r := &Registry{bySlash: make(map[string][]*manifest.Manifest)}
	for _, p := range packs {
		if p == nil {
			continue
		}
		for _, m := range p.Manifests {
			if m == nil || m.Slash == "" {
				continue
			}
			r.bySlash[m.Slash] = append(r.bySlash[m.Slash], m)
			r.all = append(r.all, m)
		}
	}
	return r
}

// Lookup returns every manifest registered for the given slash command, in
// the order they were inserted. The returned slice is shared with the
// Registry and must not be mutated by callers. A miss returns nil.
func (r *Registry) Lookup(slash string) []*manifest.Manifest {
	return r.bySlash[slash]
}

// List returns every manifest in the Registry in insertion order. The
// returned slice is shared with the Registry and must not be mutated by
// callers.
func (r *Registry) List() []*manifest.Manifest { return r.all }
