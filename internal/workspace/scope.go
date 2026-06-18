package workspace

import (
	"path/filepath"
	"strings"
)

// ScopeKind enumerates the recognized provenance categories for a manifest.
// The zero value is ScopeUnknown so a default-constructed Scope is harmless
// (it routes nowhere). Per ADR-0045 the scope is derived entirely from the
// file's path; manifests never declare it in YAML.
type ScopeKind int

const (
	// ScopeUnknown is returned when the path does not fall under any of the
	// recognized roots (projects/, packs/, commands/, monitors/).
	ScopeUnknown ScopeKind = iota
	// ScopeProject means the manifest lives under
	// projects/<project>/<env>/manifests/ — bound to a single (project, env).
	ScopeProject
	// ScopePack means the manifest lives under packs/<pack>/manifests/ —
	// shared, owned by an installed pack.
	ScopePack
	// ScopeUser means the manifest lives under commands/ or monitors/ at the
	// top of the home directory — independent of any project.
	ScopeUser
	// ScopeDraft means the manifest lives under projects/<project>/<env>/drafts/
	// — a copy-template-into-project staging area introduced by ADR-0047.
	ScopeDraft
)

// String returns a stable lowercase tag for logging.
func (k ScopeKind) String() string {
	switch k {
	case ScopeProject:
		return "project"
	case ScopePack:
		return "pack"
	case ScopeUser:
		return "user"
	case ScopeDraft:
		return "draft"
	default:
		return "unknown"
	}
}

// Scope is the path-derived provenance for a manifest file. Project / Env
// are populated for ScopeProject and ScopeDraft; Pack is populated for
// ScopePack; ScopeUser and ScopeUnknown carry no further attribution.
type Scope struct {
	Kind    ScopeKind
	Project string
	Env     string
	Pack    string
}

// ScopeOf infers the scope from a manifest's file path. The path may be
// absolute or relative to the Packwright home — only the named root segment
// ("projects", "packs", "commands", "monitors") and the segments that follow
// it are consulted, so callers do not need to canonicalize against home
// first.
//
// Mapping:
//
//	projects/<p>/<e>/manifests/...   → ScopeProject{Project: p, Env: e}
//	projects/<p>/<e>/drafts/...      → ScopeDraft{Project: p, Env: e}
//	packs/<p>/manifests/...          → ScopePack{Pack: p}
//	commands/...                     → ScopeUser
//	monitors/...                     → ScopeUser
//	anything else                    → ScopeUnknown
//
// A path that names a known root but has malformed slugs (e.g.
// projects/Acme/dev/manifests/foo.yaml — uppercase project slug) returns
// ScopeUnknown rather than a partially-populated Scope, so downstream code
// never silently inherits an invalid identifier.
func ScopeOf(path string) Scope {
	if path == "" {
		return Scope{Kind: ScopeUnknown}
	}
	// filepath.ToSlash normalizes Windows separators so the segment scan
	// below works the same on every OS; filepath.Clean folds ".." and
	// duplicate separators before we split.
	cleaned := filepath.ToSlash(filepath.Clean(path))
	segs := strings.Split(cleaned, "/")
	for i, s := range segs {
		switch s {
		case "projects":
			// Need <project>/<env>/<kind>/<file>; the file itself can be
			// missing for directory paths but the kind segment is required
			// to distinguish manifests/ from drafts/.
			if i+3 >= len(segs) {
				return Scope{Kind: ScopeUnknown}
			}
			proj, env, kind := segs[i+1], segs[i+2], segs[i+3]
			if ValidateSlug(proj) != nil || ValidateSlug(env) != nil {
				return Scope{Kind: ScopeUnknown}
			}
			switch kind {
			case "manifests":
				return Scope{Kind: ScopeProject, Project: proj, Env: env}
			case "drafts":
				return Scope{Kind: ScopeDraft, Project: proj, Env: env}
			default:
				return Scope{Kind: ScopeUnknown}
			}
		case "packs":
			// Need <pack>/<kind>/<file>.
			if i+2 >= len(segs) {
				return Scope{Kind: ScopeUnknown}
			}
			return Scope{Kind: ScopePack, Pack: segs[i+1]}
		case "commands", "monitors":
			// Need at least <file> below the root.
			if i+1 >= len(segs) {
				return Scope{Kind: ScopeUnknown}
			}
			return Scope{Kind: ScopeUser}
		}
	}
	return Scope{Kind: ScopeUnknown}
}
