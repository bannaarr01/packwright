// Package errors implements Packwright's first-class error model: a
// hand-curated catalogue of CloudFormation failure patterns plus a matcher
// that turns a raw AWS error string into a structured, actionable AppError.
//
// The model is the contract from ADR-0016: every infra failure flows through
// a single AppError struct that both the TUI and GUI render as a card with
// collapsible sections. The catalogue ships as YAML files embedded into the
// binary via //go:embed, so a contributor can add a new pattern with a single
// PR — no code changes required.
//
// This package is deliberately a pure data model + matcher + auto-fetcher.
// It does not import any front-end package, and it does not pull in the
// aws-sdk-go-v2 service clients — the cfn_events auto-fetch path depends on
// the narrow StackEventsAPI interface so callers wire in either a live
// CloudFormation client or a test fake.
package errors

// AppError is the structured failure record rendered by both Packwright
// surfaces. It is the public output of Match and FromFailedStack and the
// public input to the TUI / GUI render adapters.
//
// Every field except Raw is best-effort: when the catalogue matches, the
// matched entry populates as many fields as it can; when nothing matches,
// only Raw is populated and consumers render a fallback card with the raw
// text and a "View stack events" link.
type AppError struct {
	// Title is a one-line human-readable headline, e.g. "Target group name
	// collision". Rendered as the card heading.
	Title string `json:"title,omitempty"`

	// Cause is the probable root cause expressed in the user's terms.
	// Catalogue entries template this with the matcher's extracted context
	// (regex named-groups + the manifest's last-submitted Inputs).
	Cause string `json:"cause,omitempty"`

	// AWSCode is the AWS API error code, e.g. "ValidationError" or
	// "DuplicateTargetGroupName". Stable across SDK versions; suitable for
	// matching.
	AWSCode string `json:"aws_code,omitempty"`

	// AWSService is the AWS service that emitted the failure, e.g.
	// "CloudFormation" or "ElasticLoadBalancingV2".
	AWSService string `json:"aws_service,omitempty"`

	// StackName is the CloudFormation stack the failure originated in, when
	// the caller passes one (always set for the auto-fetch path).
	StackName string `json:"stack_name,omitempty"`

	// Resource is the logical resource ID of the failing resource within
	// the stack, e.g. "MyTargetGroup".
	Resource string `json:"resource,omitempty"`

	// Suggested is one to three concrete next steps the user can take.
	// Each entry is short, imperative, and standalone so the renderer can
	// surface them as a bullet list.
	Suggested []string `json:"suggested,omitempty"`

	// ConsoleURL is a deep link to the failing resource in the AWS Console.
	// Catalogue entries template this with the matcher's context so links
	// land on the correct region / VPC / stack.
	ConsoleURL string `json:"console_url,omitempty"`

	// Retryable signals to the renderer that the failure is transient or
	// user-correctable (e.g. a name collision) and a Retry button is safe
	// to surface. Hard schema errors set this to false.
	Retryable bool `json:"retryable,omitempty"`

	// Raw is the verbatim error string from AWS. Always populated, even
	// when a catalogue entry matches, so the user can toggle ground truth
	// in the UI.
	Raw string `json:"raw,omitempty"`

	// MatchedID is the catalogue entry's id when one matched, or "" for
	// the unknown-error fallback path. Exposed so callers can attribute a
	// rendered explanation back to its catalogue source (useful for
	// debugging and for the contributor docs link).
	MatchedID string `json:"matched_id,omitempty"`
}
