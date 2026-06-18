// Package manifest defines the typed data model for Packwright action
// manifests and the strict YAML loader that produces it.
//
// A manifest is a single YAML file (one per action) that declares a slash
// command, the form schema rendered to drive it, and — for the resource kind
// — the template + deploy wiring. The same manifest powers both the TUI and
// the GUI: there is no second source of truth for the form schema.
//
// MVP-1 fully supports the resource kind. The other kinds (shell, monitor,
// composite) are recognised so authors can land manifests for them, but the
// runtime refuses to execute them; callers gate execution on CanRun.
//
// This package is deliberately a pure data model + loader. It does not import
// awsx, pack, or any front-end package, and it does not evaluate the
// Go-template DSL embedded in manifest strings — that lands in PR-07.
package manifest

import "fmt"

// SchemaVersionV1 is the only schema_version value accepted by this loader.
// Future breaking changes will land alongside ADR-0028 with their own constant
// and a migration story; unknown values are a hard error.
const SchemaVersionV1 = "packwright.manifest.v1"

// Kind identifies the action category a manifest describes. See ADR-0013 for
// the full taxonomy; MVP-1 only runs KindResource at runtime.
type Kind string

// Recognised manifest kinds. Values match the YAML representation exactly.
const (
	KindResource  Kind = "resource"
	KindShell     Kind = "shell"
	KindMonitor   Kind = "monitor"
	KindComposite Kind = "composite"
)

// FieldType is the typed-string name of a form field's widget and validator
// family (see featureDetails.md §7.1). Unknown values are rejected by
// Validate so typos surface at load time rather than during a deploy.
type FieldType string

// Field types recognised in MVP-1. The set is intentionally small; ADR-0007
// allows additional aws/* types and template/<file> to land in later PRs
// without touching this loader.
const (
	FieldTypeString      FieldType = "string"
	FieldTypeInt         FieldType = "int"
	FieldTypeBool        FieldType = "bool"
	FieldTypeEnum        FieldType = "enum"
	FieldTypeMultistring FieldType = "multistring"
	FieldTypeSecret      FieldType = "secret"
	FieldTypeAWSVPCID    FieldType = "aws/vpc-id"
	FieldTypeAWSSubnetID FieldType = "aws/subnet-ids"
	FieldTypeAWSSGID     FieldType = "aws/sg-ids"
	FieldTypeAWSACMArn   FieldType = "aws/acm-arn"
)

// knownFieldTypes is the allow-list consulted by Validate. Kept as a private
// set so callers cannot mutate it; extend via a new exported FieldTypeXxx
// constant plus an entry here.
var knownFieldTypes = map[FieldType]struct{}{
	FieldTypeString:      {},
	FieldTypeInt:         {},
	FieldTypeBool:        {},
	FieldTypeEnum:        {},
	FieldTypeMultistring: {},
	FieldTypeSecret:      {},
	FieldTypeAWSVPCID:    {},
	FieldTypeAWSSubnetID: {},
	FieldTypeAWSSGID:     {},
	FieldTypeAWSACMArn:   {},
}

// Manifest is the top-level structure decoded from a YAML manifest file. The
// Template / Deploy / Form sections are kind-specific: only KindResource
// populates Template + Deploy in MVP-1; other kinds keep them nil.
//
// Draft and CopiedFrom are MVP-7 metadata (ADR-0047). They live under the
// "_"-prefixed root-key convention so any future "ephemeral metadata" key
// (e.g. _archived, _pinned) can land without revisiting this struct. Callers
// touch them through internal/manifest/draft.go helpers — IsDraft, MarkDraft,
// Promote, CopiedFrom — rather than the fields directly.
type Manifest struct {
	SchemaVersion string        `yaml:"schema_version"`
	Kind          Kind          `yaml:"kind"`
	Slash         string        `yaml:"slash"`
	Title         string        `yaml:"title"`
	Template      *TemplateSpec `yaml:"template,omitempty"`
	Deploy        *DeploySpec   `yaml:"deploy,omitempty"`
	Form          []Field       `yaml:"form,omitempty"`

	Draft      bool   `yaml:"_draft,omitempty"`
	CopiedFrom string `yaml:"_copied_from,omitempty"`
}

// TemplateSpec describes the infrastructure-as-code template that backs a
// resource action. Kind selects the template engine (cloudformation,
// terraform, cdk, sam); Path is the on-disk template; ParametersFile is the
// optional parameter file written before deploy.
type TemplateSpec struct {
	Kind           string `yaml:"kind"`
	Path           string `yaml:"path"`
	ParametersFile string `yaml:"parameters_file,omitempty"`
}

// DeploySpec describes how a resource action is applied. Driver selects the
// dispatch path (script or sdk); Script is the deploy script for the script
// driver; Env is the raw environment template map handed to the driver — the
// Go-template DSL inside these strings is resolved by PR-07, not here.
type DeploySpec struct {
	Driver string            `yaml:"driver"`
	Script string            `yaml:"script,omitempty"`
	Env    map[string]string `yaml:"env,omitempty"`
}

// Field is one form input. The shape mirrors featureDetails.md §7.1: a typed
// widget plus optional defaults, bounds, dependencies, and per-rule
// validators. The Default field is `any` because YAML scalars decode to
// strings, ints, bools, or sequences depending on the field's Type; the form
// engine (PR-08/09) does the conversion when it knows the target widget.
//
// Placeholder (ADR-0051) is display-only metadata: a per-field example shown
// in the input widget when the user has not yet typed a value. It is not a
// default and not part of validation — Validate intentionally ignores it. The
// resolver in hints/ combines this author override with the type-default
// catalogue into the final hint string consumed by the form layers.
type Field struct {
	ID          string          `yaml:"id"`
	Label       string          `yaml:"label"`
	Type        FieldType       `yaml:"type"`
	Placeholder string          `yaml:"placeholder,omitempty"`
	Required    bool            `yaml:"required,omitempty"`
	Default     any             `yaml:"default,omitempty"`
	Min         *int            `yaml:"min,omitempty"`
	Max         *int            `yaml:"max,omitempty"`
	Values      []string        `yaml:"values,omitempty"`
	DependsOn   []string        `yaml:"depends_on,omitempty"`
	Validate    []ValidatorSpec `yaml:"validate,omitempty"`
}

// ValidatorSpec is one cross-field or per-field validator entry. Rule names
// the validator (e.g. "distinct-az", "regex"); Message overrides the default
// error text; Params captures any rule-specific keys on the same YAML node so
// new rules can land without struct changes. The runtime validator registry
// lives in a later PR.
type ValidatorSpec struct {
	Rule    string         `yaml:"rule"`
	Message string         `yaml:"message,omitempty"`
	Params  map[string]any `yaml:",inline"`
}

// CanRun reports whether MVP-1 supports this manifest's kind at runtime. It
// returns nil for KindResource and a typed error for the other kinds so the
// TUI / GUI can surface "kind not yet supported" without re-parsing the file.
// Pass a Manifest already returned by Load — callers should not invent one.
func CanRun(m *Manifest) error {
	if m == nil {
		return fmt.Errorf("manifest: CanRun called with nil manifest")
	}
	switch m.Kind {
	case KindResource:
		return nil
	case KindShell, KindMonitor, KindComposite:
		return fmt.Errorf("manifest: kind %q not yet supported in MVP-1", m.Kind)
	default:
		return fmt.Errorf("manifest: unknown kind %q", m.Kind)
	}
}
