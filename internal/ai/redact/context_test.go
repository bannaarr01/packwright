package redact

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/internal/errors"
)

// TestFromAppError confirms an AppError + stack-event slice round-trips
// through Apply: the resulting Redacted.Text is parseable JSON, the
// always-on AKIA pattern fired on a key buried in the reason field, and
// the typed fields survive verbatim so the AI sees the structure.
func TestFromAppError(t *testing.T) {
	e := errors.AppError{
		Title:      "Target group name collision",
		Cause:      "Two target groups in the same VPC collided.",
		AWSCode:    "DuplicateTargetGroupName",
		AWSService: "ElasticLoadBalancingV2",
		StackName:  "ProdStack",
		Resource:   "MyTargetGroup",
		Suggested:  []string{"Rename the target group", "Re-run `packwright apply`"},
		ConsoleURL: "https://us-east-1.console.aws.amazon.com/...",
		Retryable:  true,
		Raw:        "An error occurred: " + fakeAKIA,
	}
	events := []StackEvent{
		{
			Time:         "2026-06-01T12:00:00Z",
			LogicalID:    "MyTargetGroup",
			ResourceType: "AWS::ElasticLoadBalancingV2::TargetGroup",
			Status:       "CREATE_FAILED",
			Reason:       "Bearer " + fakeJWT + " was used; account 123456789012",
		},
	}
	r := FromAppError(e, events, DefaultOpts())

	// The fake AWS key buried in Raw must be scrubbed.
	if strings.Contains(r.Text, fakeAKIA) {
		t.Fatalf("AKIA leaked from AppError.Raw: %s", r.Text)
	}
	if r.Counts[HintAWSAccessKey] == 0 {
		t.Fatalf("expected AKIA hint count > 0; counts=%v", r.Counts)
	}
	// The 12-digit account ID in the stack event must be scrubbed.
	if strings.Contains(r.Text, "123456789012") {
		t.Fatalf("account id leaked: %s", r.Text)
	}
	// The Bearer header in the stack event must be scrubbed.
	if strings.Contains(r.Text, fakeJWT) {
		t.Fatalf("JWT leaked from stack-event reason: %s", r.Text)
	}
	// Structural fields survive — the AI must still see the title.
	if !strings.Contains(r.Text, "Target group name collision") {
		t.Fatalf("title was scrubbed unexpectedly: %s", r.Text)
	}
	// The output must be parseable JSON: that is the contract with the
	// UI's "Context sent" pane, which renders the JSON tree.
	var decoded map[string]any
	if err := json.Unmarshal([]byte(r.Text), &decoded); err != nil {
		t.Fatalf("FromAppError output is not valid JSON: %v\n%s", err, r.Text)
	}
	if decoded["kind"] != "app_error" {
		t.Fatalf("expected kind=app_error, got %v", decoded["kind"])
	}
}

// TestFromMonitorPanel covers both shapes (metric series, log lines)
// in a single snapshot to confirm the redactor accepts mixed-mode
// snapshots without complaint and scrubs secrets in any of them.
func TestFromMonitorPanel(t *testing.T) {
	snap := PanelSnapshot{
		Kind:  "cloudwatch/metric",
		Title: "API latency p95",
		Spec: map[string]any{
			"namespace": "AWS/ApplicationELB",
			"password":  "should-be-stripped",
		},
		Series: []PanelSeries{
			{
				Label:  "p95",
				Unit:   "Milliseconds",
				Points: []PanelPoint{{Time: "2026-06-01T12:00:00Z", Value: 123.4}},
			},
		},
		Logs: []PanelLog{
			{
				Time:    "2026-06-01T12:00:05Z",
				Stream:  "api/prod",
				Message: "got key " + fakeAKIA + " from header",
			},
		},
	}
	r := FromMonitorPanel(snap, DefaultOpts())

	if strings.Contains(r.Text, fakeAKIA) {
		t.Fatalf("AKIA leaked from log message: %s", r.Text)
	}
	if strings.Contains(r.Text, "should-be-stripped") {
		t.Fatalf("spec password leaked: %s", r.Text)
	}
	if !strings.Contains(r.Text, "API latency p95") {
		t.Fatalf("title was scrubbed unexpectedly: %s", r.Text)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(r.Text), &decoded); err != nil {
		t.Fatalf("FromMonitorPanel output is not valid JSON: %v\n%s", err, r.Text)
	}
	if decoded["kind"] != "monitor_panel" {
		t.Fatalf("expected kind=monitor_panel, got %v", decoded["kind"])
	}
}

// TestFromBlankChat confirms a baseline with profile + region + stacks
// survives redaction and remains valid JSON. The 12-digit account that
// shows up in the profile name must still be scrubbed by the default
// account-ID rule.
func TestFromBlankChat(t *testing.T) {
	b := BlankBaseline{
		Profile: "deploy-123456789012",
		Region:  "us-east-1",
		ActiveStacks: []StackSummary{
			{Name: "ProdStack", Status: "CREATE_COMPLETE"},
			{Name: "EdgeStack", Status: "UPDATE_IN_PROGRESS"},
		},
	}
	r := FromBlankChat(b, DefaultOpts())

	if strings.Contains(r.Text, "123456789012") {
		t.Fatalf("account id leaked from profile name: %s", r.Text)
	}
	if !strings.Contains(r.Text, "us-east-1") {
		t.Fatalf("region was scrubbed unexpectedly: %s", r.Text)
	}
	if !strings.Contains(r.Text, "ProdStack") {
		t.Fatalf("stack name was scrubbed unexpectedly: %s", r.Text)
	}

	var decoded map[string]any
	if err := json.Unmarshal([]byte(r.Text), &decoded); err != nil {
		t.Fatalf("FromBlankChat output is not valid JSON: %v\n%s", err, r.Text)
	}
	if decoded["kind"] != "blank_chat" {
		t.Fatalf("expected kind=blank_chat, got %v", decoded["kind"])
	}
}

// TestContextOptsPropagation confirms toggles on Opts reach the
// patterns invoked through the context helpers, not just the direct
// Apply path.
func TestContextOptsPropagation(t *testing.T) {
	b := BlankBaseline{Profile: "p-123456789012"}

	opts := DefaultOpts()
	opts.RedactAccountIDs = false
	r := FromBlankChat(b, opts)
	if !strings.Contains(r.Text, "123456789012") {
		t.Fatalf("account redaction fired despite RedactAccountIDs=false in helper: %s", r.Text)
	}
}
