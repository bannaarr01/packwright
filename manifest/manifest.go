// Package manifest defines the on-disk YAML format Packwright uses to describe
// actions (slash commands).
//
// This file currently carries only the subset of the manifest needed by the
// pack discovery and registry layer (PR-06). The full schema — field
// definitions, deploy/template specs, validators — is owned by PR-05 and will
// extend the types declared here without breaking the names used by importing
// packages.
package manifest

// Kind names the manifest variants Packwright supports. Only KindResource is
// executable in MVP 1; the other kinds are reserved so manifests for them
// parse cleanly but error at run time.
type Kind string

// The set of manifest kinds. See ADR-0007 for the rationale.
const (
	KindResource  Kind = "resource"
	KindShell     Kind = "shell"
	KindMonitor   Kind = "monitor"
	KindComposite Kind = "composite"
)

// Manifest is the parsed shape of a single action manifest YAML file. The
// fields here are the discovery-time identity of a manifest — the parts the
// pack registry needs to register and look up a slash command. PR-05 grows
// this struct with the form schema and per-kind specs.
type Manifest struct {
	// SchemaVersion is the manifest schema identifier (e.g.
	// "packwright.manifest.v1"). Used by future versioning logic; not
	// enforced in MVP 1.
	SchemaVersion string `yaml:"schema_version"`

	// Kind selects which manifest variant this file describes. See Kind.
	Kind Kind `yaml:"kind"`

	// Slash is the leading-slash command name a user types to invoke this
	// manifest (e.g. "/alb"). It is the registry's primary key.
	Slash string `yaml:"slash"`

	// Title is a short, human-readable label rendered in the TUI/GUI lists.
	Title string `yaml:"title"`
}
