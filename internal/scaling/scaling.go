// Package scaling implements the pure data model and parameter-merging logic
// behind ADR-0049 (scaling = scoped parameter-override re-deploy).
//
// Per the ADR, scaling is *a parameter-only update via the ADR-0048 change-set
// flow* — Packwright does not add per-resource SDK mutators. This package owns
// only the parts of that flow that need no AWS calls: clamping a user-supplied
// delta against the manifest's scaling bounds, applying per-environment guards
// (ADR-0045), and merging the result into the stack's current parameter map.
// The actual change-set lifecycle lives in PR-06 (internal/update); the /scale
// slash command in cmd is the integration point that wires this package's
// output into that coordinator.
//
// Everything here is pure: no AWS SDK calls, no filesystem reads, no logging.
// Clamps are returned as ClampEvent values so the caller (cmd_scale) can log
// them with the program's slog handler — ADR-0049 requires that env-guard
// clamps be explicitly surfaced, not silently accepted.
package scaling

// Kind discriminates the widget the scaling UI renders for a target and the
// validation/clamp pipeline BuildParams runs for it. The set is intentionally
// small per ADR-0049 and matches the manifest YAML literal values.
type Kind string

// Recognised scaling kinds.
const (
	KindInteger Kind = "integer"
	KindEnum    Kind = "enum"
	KindString  Kind = "string"
)

// Spec is the runtime form of one manifest scaling entry. It mirrors the
// manifest's ScalingSpec but carries no YAML tags — the manifest packages
// declare their own typed structs (with tags) and the /scale command
// converts a slice of those into a []Spec via FromManifest helpers in the
// cmd layer. Keeping the YAML representation in the manifest packages keeps
// internal/scaling free of any decoder dependency.
//
// Param refers to a form[].id in the same manifest — Validate (in
// internal/manifest) enforces that linkage at manifest-load time, so this
// package treats Param as opaque.
type Spec struct {
	// Param is the manifest form field id this scaling entry targets. It
	// is also the key BuildParams writes into the resulting parameter map.
	Param string
	// Label is the short human label rendered next to the widget. Empty is
	// allowed; the UI falls back to Param.
	Label string
	// Kind selects the widget and the clamp/validation pipeline.
	Kind Kind
	// Min / Max bound integer kinds. Nil means "no bound on this side".
	Min, Max *int
	// Step is a UI-only hint for integer sliders. Ignored by BuildParams.
	Step *int
	// Values is the closed set of acceptable values for enum kinds.
	// Ignored by other kinds.
	Values []string
	// EnvGuards overlays per-environment bounds and a require_confirmation
	// flag. Keys are env names (e.g. "dev", "stg", "prd"); the active env
	// is supplied at BuildParams time. Missing entries are equivalent to a
	// zero-value EnvGuard (no overrides, no consent).
	EnvGuards map[string]EnvGuard
}

// EnvGuard is the per-environment overlay on a Spec. Nil-pointer fields keep
// the Spec's own Min/Max; non-nil values override (intentionally — per
// ADR-0049 the env guard is the tighter authority for that env).
type EnvGuard struct {
	// Min / Max override the Spec's bounds for this env when non-nil.
	Min, Max *int
	// RequireConfirmation marks any /scale invocation that mutates this
	// param on this env as needing an ADR-0036 consent gate before
	// ExecuteChangeSet. The reason string ("scale on <env> env") is
	// composed by BuildParams.
	RequireConfirmation bool
}

// Target couples one Spec with the stack's current value for that parameter.
// /scale renders one Target per Spec; the user submits a new value for each
// (or leaves it at the current value).
type Target struct {
	Spec    Spec
	Current string
}

// Form is the per-stack collection of scaling targets rendered when /scale
// fires. It bundles enough context (stack name + env) for the front-end to
// title the form correctly.
type Form struct {
	StackName string
	Env       string
	Targets   []Target
}

// ClampEvent records that BuildParams clamped a user-supplied value because
// it crossed an env guard bound. ADR-0049: "env_guards do not silently clamp
// values; they clamp explicitly and log the clamp." The caller of BuildParams
// is expected to emit one log line per ClampEvent.
type ClampEvent struct {
	// Param is the manifest form-field id that was clamped.
	Param string
	// Env is the active environment whose guard applied the clamp.
	Env string
	// Requested is the user-supplied value, formatted as it would appear
	// in the parameter map (e.g. "30").
	Requested string
	// Effective is the clamped value actually written to the parameter
	// map.
	Effective string
	// Bound names which side of the guard fired: "min" or "max".
	Bound string
	// Limit is the numeric bound value that was hit. Same as the bound
	// numeric in the env guard (or Spec.Min/Max when the env guard does
	// not override that side).
	Limit int
}

// Result is the output of BuildParams. Params is the full, post-clamp
// parameter map the /scale flow hands to PR-06's update coordinator;
// Clamps is the list of guard-applied clamps (each one must be logged by
// the caller); RequireConsent is true when any mutated parameter's active
// env guard sets require_confirmation, and ConsentReason carries the
// human-readable string the ADR-0036 consent modal renders.
type Result struct {
	Params         map[string]string
	Clamps         []ClampEvent
	RequireConsent bool
	ConsentReason  string
}
