// Package scaffold implements the /new-command and /new-pack wizards (ADR-0022).
//
// Generate(spec) emits canonical YAML for a single action manifest, suitable
// for dropping into a pack's manifests/ directory. NewPack(parent, name)
// builds the full pack directory tree. Both functions are content-only — the
// multi-step form UX that drives them is owned by the form-state engine
// (ADR-0007 form); this package only supplies the manifest data and the
// dispatcher runners that consume the collected inputs.
//
// The generation templates live under templates/ and are embedded via
// //go:embed so the binary ships self-contained: there is no run-time
// dependency on a checked-out repo or installed pack.
package scaffold

import "github.com/bannaarr01/packwright/manifest"

// Spec is the typed input to Generate. Only the fields appropriate to the
// kind need to be set; sections that do not apply to the kind are silently
// ignored (e.g. Template/Deploy on a shell manifest). Validate is called
// once at the top of Generate, so callers do not need to pre-validate.
type Spec struct {
	// Kind selects which manifest template renders. Must be one of
	// manifest.KindResource, KindShell, KindMonitor, KindComposite.
	Kind manifest.Kind

	// Slash is the leading-slash command name (e.g. "/restart-api"). The
	// generated manifest's slash field receives this value verbatim, so it
	// must already be in its final form.
	Slash string

	// Title is the human-readable label shown in the palette. It is
	// YAML-quoted on output so embedded colons or hashes are safe.
	Title string

	// Form is the ordered list of input fields the action collects before
	// running. Empty Form is allowed (e.g. parameterless shell commands).
	Form []FieldSpec

	// Template populates the top-level `template:` section. Required when
	// Kind == KindResource; must be nil for every other kind.
	Template *TemplateSpec

	// Deploy populates the top-level `deploy:` section. Required when
	// Kind == KindResource; must be nil for every other kind.
	Deploy *DeploySpec
}

// FieldSpec mirrors manifest.Field one-for-one but uses a string Default so
// the wizard front-end can pass a single textual value through every widget.
// The generator emits a typed YAML default (int, bool, string) based on the
// field's Type; see command.go for the conversion rules.
type FieldSpec struct {
	ID        string
	Label     string
	Type      manifest.FieldType
	Required  bool
	Default   string
	Values    []string
	Min       *int
	Max       *int
	DependsOn []string
}

// TemplateSpec mirrors manifest.TemplateSpec. It is duplicated here rather
// than re-used so the scaffold input layer is independent of any future
// changes to the manifest shim.
type TemplateSpec struct {
	Kind           string
	Path           string
	ParametersFile string
}

// DeploySpec mirrors manifest.DeploySpec.
type DeploySpec struct {
	Driver string
	Script string
	Env    map[string]string
}

// PackSpec is NewPack's input. Name is required; Description and Author are
// optional metadata that land in pack.yaml.
type PackSpec struct {
	Name        string
	Description string
	Author      string
	Homepage    string
}
