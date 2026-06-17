package redact

import (
	"github.com/bannaarr01/packwright/internal/errors"
)

// StackEvent is one CloudFormation stack event slice for the
// AppError context block. The fields mirror the parts of an AWS SDK
// StackEvent that are useful to the AI without forcing this package
// to import aws-sdk-go-v2. The caller (the TUI / GUI error card layer)
// translates a sdk-native event into this shape before invoking
// FromAppError.
type StackEvent struct {
	// Time is the event timestamp formatted by the caller (typically
	// RFC3339). Stored as a string so the redactor never has to worry
	// about time-zone conversion or formatting drift.
	Time string `json:"time,omitempty"`
	// LogicalID is the resource's logical id within the stack.
	LogicalID string `json:"logical_id,omitempty"`
	// ResourceType is the CloudFormation type name, e.g.
	// "AWS::ElasticLoadBalancingV2::TargetGroup".
	ResourceType string `json:"resource_type,omitempty"`
	// Status is the resource status, e.g. "CREATE_FAILED".
	Status string `json:"status,omitempty"`
	// Reason is the human-readable status reason. Often contains
	// AWS-emitted error text — exactly the surface the redactor needs
	// to scrub before it leaves the machine.
	Reason string `json:"reason,omitempty"`
}

// PanelSnapshot is what the monitor-panel "Ask AI" entry point hands
// to the redactor: the panel's identity, its YAML spec, and whichever
// of (metric samples, log lines) the panel produces. The redactor
// does not care which kind of panel this is — it just folds whatever
// fields are set into a single typed context block.
type PanelSnapshot struct {
	// Kind is the registered panel kind, e.g. "cloudwatch/metric".
	Kind string `json:"kind,omitempty"`
	// Title is the human-readable panel title from the manifest.
	Title string `json:"title,omitempty"`
	// Spec is the panel's YAML spec as a generic map. The redactor
	// will scrub any password-shaped values inside.
	Spec map[string]any `json:"spec,omitempty"`
	// Series carries the recent metric samples for time-series panels.
	Series []PanelSeries `json:"series,omitempty"`
	// Logs carries the recent log lines for log-tail panels.
	Logs []PanelLog `json:"logs,omitempty"`
}

// PanelSeries is one labelled line of a time-series panel snapshot.
type PanelSeries struct {
	Label  string       `json:"label,omitempty"`
	Unit   string       `json:"unit,omitempty"`
	Points []PanelPoint `json:"points,omitempty"`
}

// PanelPoint is one (timestamp, value) sample. Time is formatted by
// the caller; PanelPoint stays string-typed for the same reason as
// StackEvent.Time.
type PanelPoint struct {
	Time  string  `json:"t,omitempty"`
	Value float64 `json:"v"`
}

// PanelLog is one log line in a log-tail panel snapshot.
type PanelLog struct {
	Time    string `json:"time,omitempty"`
	Stream  string `json:"stream,omitempty"`
	Message string `json:"message,omitempty"`
}

// BlankBaseline is the metadata the /ai blank entry point ships as
// the initial context: profile, region, and a counts-only summary of
// active stacks. Resource IDs are deliberately absent (ADR-0037) and
// are added later if and only if the user mentions them by name.
type BlankBaseline struct {
	Profile      string         `json:"profile,omitempty"`
	Region       string         `json:"region,omitempty"`
	ActiveStacks []StackSummary `json:"active_stacks,omitempty"`
}

// StackSummary is one stack listed in BlankBaseline. Counts only —
// no event detail, no resource IDs.
type StackSummary struct {
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
}

// appErrorContext is the wire shape of FromAppError's output. Kept
// unexported so the only public face is the Redacted struct: callers
// inspect the rendered text, not the intermediate Go type.
type appErrorContext struct {
	Kind   string          `json:"kind"`
	Error  errors.AppError `json:"error"`
	Events []StackEvent    `json:"stack_events,omitempty"`
}

// panelContext is the wire shape of FromMonitorPanel's output.
type panelContext struct {
	Kind  string        `json:"kind"`
	Panel PanelSnapshot `json:"panel"`
}

// blankContext is the wire shape of FromBlankChat's output.
type blankContext struct {
	Kind     string        `json:"kind"`
	Baseline BlankBaseline `json:"baseline"`
}

// FromAppError builds the initial AI context for "Ask AI" on an error
// card: the structured AppError plus up to the most recent stack
// events. The caller is expected to have already trimmed events to
// the ADR's 50-event window — this helper does not enforce that
// limit because it doesn't know the user's preference (some users
// want fewer events to save tokens; some want all 50). The caller
// also frees this function from having to talk to CloudFormation:
// events come in pre-fetched.
func FromAppError(e errors.AppError, events []StackEvent, opts Opts) Redacted {
	return Apply(appErrorContext{
		Kind:   "app_error",
		Error:  e,
		Events: events,
	}, opts)
}

// FromMonitorPanel builds the initial AI context for "Ask AI" on a
// monitor dashboard panel. The caller assembles the snapshot from
// whatever it has on hand — series points for metric panels, log
// lines for log-tail panels — and this helper folds it into the
// redacted context block. The redactor does not care which fields
// are populated; an empty Series and empty Logs simply produce a
// context block that contains only the panel identity and spec.
func FromMonitorPanel(snap PanelSnapshot, opts Opts) Redacted {
	return Apply(panelContext{
		Kind:  "monitor_panel",
		Panel: snap,
	}, opts)
}

// FromBlankChat builds the baseline context for the /ai blank entry
// point. Per ADR-0037, this context is intentionally minimal —
// profile, region, and stack counts only. Resource IDs are not
// included; they enter the conversation only when the user types
// them. This helper still runs the result through the redactor so a
// profile name that happens to contain an account ID is scrubbed
// before send.
func FromBlankChat(b BlankBaseline, opts Opts) Redacted {
	return Apply(blankContext{
		Kind:     "blank_chat",
		Baseline: b,
	}, opts)
}
