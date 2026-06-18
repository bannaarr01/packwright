// Package manifest is a minimal shim of the action-manifest data model that the
// resource engine consumes.
//
// The full implementation — YAML loader, strict validation, support for all
// command kinds — is the responsibility of PR-05. This file declares only the
// types the engine references at compile time so PR-10 can land independently
// of PR-05's merge order. Once PR-05 lands the shim is replaced wholesale; no
// engine code needs to change.
//
// TODO(PR-05): replace this shim with the canonical manifest package.
package manifest

// Kind identifies which command-runtime services a manifest expects. MVP 1
// implements only KindResource; the others are placeholders so manifests for
// later command kinds can be parsed without crashing.
type Kind string

// Recognised manifest kinds.
const (
	KindResource  Kind = "resource"
	KindShell     Kind = "shell"
	KindMonitor   Kind = "monitor"
	KindComposite Kind = "composite"
)

// FieldType is the widget / picker the front-end renders for a form field.
type FieldType string

// Field types supported by MVP 1. The aws/* picker types are populated by the
// front-end before Execute is called; the engine treats their values opaquely.
const (
	TypeString       FieldType = "string"
	TypeInt          FieldType = "int"
	TypeBool         FieldType = "bool"
	TypeEnum         FieldType = "enum"
	TypeMultistring  FieldType = "multistring"
	TypeSecret       FieldType = "secret"
	TypeAWSVpcID     FieldType = "aws/vpc-id"
	TypeAWSSubnetIDs FieldType = "aws/subnet-ids"
	TypeAWSSGIDs     FieldType = "aws/sg-ids"
	TypeAWSACMArn    FieldType = "aws/acm-arn"
)

// Manifest is the parsed contents of a single action manifest file.
type Manifest struct {
	SchemaVersion string        `yaml:"schema_version"`
	ID            string        `yaml:"id"`
	Kind          Kind          `yaml:"kind"`
	Slash         string        `yaml:"slash"`
	Title         string        `yaml:"title"`
	Template      *TemplateSpec `yaml:"template,omitempty"`
	Deploy        *DeploySpec   `yaml:"deploy,omitempty"`
	Form          []Field       `yaml:"form,omitempty"`
	Scaling       []ScalingSpec `yaml:"scaling,omitempty"`
}

// TemplateSpec describes where the underlying infrastructure template lives
// and where the engine should write the generated parameters file.
//
// Path and ParametersFile are interpreted relative to the manifest's
// containing directory; callers pass that base directory to the engine
// explicitly (see resource.WithBaseDir).
type TemplateSpec struct {
	Kind           string `yaml:"kind"`            // currently always "cloudformation"
	Path           string `yaml:"path"`            // template file (e.g. alb-template.yaml)
	ParametersFile string `yaml:"parameters_file"` // generated parameters.json
}

// DeploySpec describes how the engine drives the deploy. MVP 1 supports only
// the "script" driver (ADR-0008); the "sdk" driver lands in MVP 2/3.
type DeploySpec struct {
	Driver string            `yaml:"driver"`        // "script" | "sdk"
	Script string            `yaml:"script"`        // path to deploy.sh, relative to the manifest
	Env    map[string]string `yaml:"env,omitempty"` // env-var name -> Go template string
}

// Field is one entry in a manifest's form schema.
//
// Placeholder is display-only metadata (ADR-0051): a per-field example shown
// in the input widget when the user has not yet typed a value. It is not a
// default, not validation, and not consulted at runtime — the resolver in
// internal/manifest/hints turns this plus the type-default catalogue into a
// single string for the TUI / GUI form layers.
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

// ValidatorSpec is a manifest-declared validator. The engine recognises a
// fixed set of rule names (distinct-az, length, ...); unknown rules are
// reported as configuration errors at validate time.
type ValidatorSpec struct {
	Rule    string         `yaml:"rule"`
	Message string         `yaml:"message,omitempty"`
	Params  map[string]any `yaml:",inline,omitempty"`
}

// ScalingSpec declares one parameter the /scale slash command can mutate
// against a deployed stack (ADR-0049). The YAML tags here MUST match the
// canonical internal/manifest.ScalingSpec exactly so a manifest decoded
// through either package sees the same field set. This shim type carries
// the same fields so callers wired against the shim engine see the
// scaling block as well.
type ScalingSpec struct {
	Param     string                     `yaml:"param"`
	Label     string                     `yaml:"label,omitempty"`
	Kind      string                     `yaml:"kind"`
	Min       *int                       `yaml:"min,omitempty"`
	Max       *int                       `yaml:"max,omitempty"`
	Step      *int                       `yaml:"step,omitempty"`
	Values    []string                   `yaml:"values,omitempty"`
	EnvGuards map[string]ScalingEnvGuard `yaml:"env_guards,omitempty"`
}

// ScalingEnvGuard is one per-environment overlay on a ScalingSpec. Mirrors
// internal/manifest.ScalingEnvGuard byte-for-byte; the comment there is the
// authoritative description.
type ScalingEnvGuard struct {
	Min                 *int `yaml:"min,omitempty"`
	Max                 *int `yaml:"max,omitempty"`
	RequireConfirmation bool `yaml:"require_confirmation,omitempty"`
}
