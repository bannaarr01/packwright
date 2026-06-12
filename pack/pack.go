// Package pack discovers Packwright packs on disk and exposes their
// manifests through an in-memory registry keyed by slash command.
//
// A "pack" is a directory laid out per ADR-0009:
//
//	<pack>/
//	  pack.yaml        # name, version, description, requires
//	  manifests/       # one YAML manifest per action
//	  templates/       # CFN templates referenced by manifests (not read here)
//	  commands/        # optional pack-scoped custom commands
//	  monitors/        # optional pack-scoped dashboards
//	  README.md
//
// Discovery walks <homeDir>/packs/*/ and is filesystem-driven — no network,
// no AWS calls. Pack installation and updates live in MVP-4 PR-01.
package pack

import "github.com/bannaarr01/packwright/manifest"

// Pack is a discovered pack on disk together with its parsed metadata and
// loaded manifests.
type Pack struct {
	// Name is the pack identity, taken from pack.yaml. It must match the
	// directory name; mismatches are reported as discovery errors.
	Name string

	// Version is the pack-author-supplied semver string from pack.yaml.
	// MVP 1 does not enforce semver compatibility (MVP-4 PR-02 does).
	Version string

	// Dir is the absolute path to the pack directory.
	Dir string

	// Meta is the full parsed contents of pack.yaml.
	Meta PackMeta

	// Manifests are the action manifests loaded from <Dir>/manifests/*.yaml.
	// Iteration order follows lexical order of file names so the registry's
	// behaviour is deterministic across operating systems.
	Manifests []*manifest.Manifest
}

// PackMeta is the parsed shape of a pack's pack.yaml. Authors hand-write this
// file; YAML decoding is strict so unknown keys surface as errors.
type PackMeta struct {
	// Name is the pack identifier. Required.
	Name string `yaml:"name"`

	// Version is the pack-author-supplied semver string. Required; not
	// validated as semver in MVP 1.
	Version string `yaml:"version"`

	// Description is a one-line human description. Optional.
	Description string `yaml:"description"`

	// Homepage is the canonical URL where the pack is published. Optional.
	Homepage string `yaml:"homepage"`

	// Author identifies the pack maintainer. Optional, free-form.
	Author string `yaml:"author"`

	// Requires maps a module name (e.g. "packwright" or
	// "packwright.manifest") to a constraint string. Enforced at load
	// time by internal/pack.Check via loadPack — a mismatch surfaces as a
	// *RequiresError wrapped with the pack path. See ADR-0028.
	Requires map[string]string `yaml:"requires"`
}
