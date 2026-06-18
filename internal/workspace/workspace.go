// Package workspace owns Packwright's three-level Project / Environment / Stack
// workspace data model, as specified by ADR-0045. It is the single source of
// truth for:
//
//   - the on-disk layout under <Home>/projects/<project>/<env>/...
//   - the slug regex and case-insensitive collision rule that every input
//     boundary (slash commands, YAML decode, config setters) must enforce
//   - the path-based scope inference used to tag a manifest as project-,
//     pack-, user-, or draft-scoped
//
// The package is pure data + filesystem. It has no UI imports and no AWS
// imports; the TUI sidebar / GUI tree (PR-09 / PR-10) and the stack-record
// flow (PR-02 / PR-06) sit on top of these primitives without modifying them.
package workspace

// Project is a top-level workspace entry — a folder under
// <Home>/projects/<Slug>/ that groups one or more environments together. The
// YAML tags match the schema described in ADR-0045 so config.yaml round-trips
// cleanly. The directory name is the authority for Slug; the YAML's slug
// field is read for compatibility but the loader overwrites it with the
// directory basename, so a rename on disk is always observed.
type Project struct {
	// Slug is the lowercase kebab-case identifier (^[a-z0-9][a-z0-9-]{0,38}$).
	Slug string `yaml:"slug"`
	// Name is the human-readable label shown in the UI.
	Name string `yaml:"name"`
	// Description is an optional free-text blurb persisted in project.yaml.
	Description string `yaml:"description,omitempty"`
	// Profile is the default AWS profile inherited by envs that do not
	// override it.
	Profile string `yaml:"profile,omitempty"`
	// Region is the default AWS region inherited by envs that do not
	// override it.
	Region string `yaml:"region,omitempty"`
	// Envs lists the project's environments in slug order.
	Envs []Env `yaml:"envs"`
}

// Env is a child of a Project — a folder under
// <Home>/projects/<project-slug>/<Slug>/ that owns its own manifests,
// drafts, and stack records.
type Env struct {
	// Slug is the lowercase kebab-case identifier
	// (^[a-z0-9][a-z0-9-]{0,38}$). Unique within the parent project.
	Slug string `yaml:"slug"`
	// Name is the human-readable label shown in the UI.
	Name string `yaml:"name"`
	// Profile overrides the parent project's AWS profile when non-empty.
	Profile string `yaml:"profile,omitempty"`
	// Region overrides the parent project's AWS region when non-empty.
	Region string `yaml:"region,omitempty"`
}

// FindProject returns a pointer to the project in projects whose Slug matches
// candidate case-insensitively, or nil when none does. Callers may mutate the
// returned project; the slice is not re-sorted.
func FindProject(projects []Project, candidate string) *Project {
	cand := NormalizeSlug(candidate)
	for i := range projects {
		if NormalizeSlug(projects[i].Slug) == cand {
			return &projects[i]
		}
	}
	return nil
}

// FindEnv returns a pointer to the env in p whose Slug matches candidate
// case-insensitively, or nil when none does.
func (p *Project) FindEnv(candidate string) *Env {
	if p == nil {
		return nil
	}
	cand := NormalizeSlug(candidate)
	for i := range p.Envs {
		if NormalizeSlug(p.Envs[i].Slug) == cand {
			return &p.Envs[i]
		}
	}
	return nil
}
