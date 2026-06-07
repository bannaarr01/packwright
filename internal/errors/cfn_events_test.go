package errors_test

import (
	"context"
	stderrors "errors"
	"fmt"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/errors"
)

// fakeStackEvents is a deterministic StackEventsAPI fake. It returns the
// canned events on every DescribeStackEvents call and records the last
// stackName passed in for assertions.
type fakeStackEvents struct {
	events   []errors.StackEvent
	err      error
	lastName string
}

func (f *fakeStackEvents) DescribeStackEvents(_ context.Context, stackName string) ([]errors.StackEvent, error) {
	f.lastName = stackName
	if f.err != nil {
		return nil, f.err
	}
	return f.events, nil
}

// TestFromFailedStackHappyPath wires a fake CFN client that returns a
// CREATE_FAILED row whose Reason matches the tg-name-collision pattern;
// FromFailedStack should pick that row, derive service+code from the
// resource type, and produce the catalogue-rendered AppError.
func TestFromFailedStackHappyPath(t *testing.T) {
	// CFN returns newest-first; the OLDEST FAILED row is the actual root
	// cause and the fetcher must walk past the cascade rollbacks above
	// it to find it.
	api := &fakeStackEvents{
		events: []errors.StackEvent{
			{
				EventID:        "evt-3",
				StackName:      "my-app",
				ResourceType:   "AWS::CloudFormation::Stack",
				ResourceStatus: "ROLLBACK_IN_PROGRESS",
				Reason:         "The following resource(s) failed to create: [MyTargetGroup]",
				Time:           time.Unix(3000, 0),
			},
			{
				EventID:           "evt-2",
				StackName:         "my-app",
				LogicalResourceID: "MyOtherResource",
				ResourceType:      "AWS::CloudFormation::Stack",
				ResourceStatus:    "CREATE_FAILED",
				Reason:            "cascade rollback follower",
				Time:              time.Unix(2000, 0),
			},
			{
				EventID:           "evt-1",
				StackName:         "my-app",
				LogicalResourceID: "MyTargetGroup",
				ResourceType:      "AWS::ElasticLoadBalancingV2::TargetGroup",
				ResourceStatus:    "CREATE_FAILED",
				Reason:            "Target group with name 'tg-api' already exists (Service: ElasticLoadBalancingV2; Status Code: 400; Error Code: DuplicateTargetGroupName; Request ID: abc)",
				Time:              time.Unix(1000, 0),
			},
		},
	}

	got, err := errors.FromFailedStack(context.Background(), api, "my-app", "us-east-1", map[string]any{
		"VpcId": "vpc-abc",
	})
	if err != nil {
		t.Fatalf("FromFailedStack: %v", err)
	}
	if api.lastName != "my-app" {
		t.Errorf("stackName not passed through: got %q", api.lastName)
	}
	if got.MatchedID != "tg-name-collision" {
		t.Fatalf("expected tg-name-collision, got %q (raw=%s)", got.MatchedID, got.Raw)
	}
	if got.StackName != "my-app" {
		t.Errorf("StackName not annotated: got %q", got.StackName)
	}
	if got.Resource != "MyTargetGroup" {
		t.Errorf("Resource not threaded through: got %q", got.Resource)
	}
}

// TestFromFailedStackNoFailed returns an empty event list — the fetcher
// must surface ErrNoFailedEvent rather than blow up with a nil deref.
func TestFromFailedStackNoFailed(t *testing.T) {
	api := &fakeStackEvents{
		events: []errors.StackEvent{
			{ResourceStatus: "CREATE_IN_PROGRESS"},
			{ResourceStatus: "CREATE_COMPLETE"},
		},
	}
	got, err := errors.FromFailedStack(context.Background(), api, "s", "us-east-1", nil)
	if got != nil {
		t.Errorf("expected nil AppError, got %+v", got)
	}
	if !stderrors.Is(err, errors.ErrNoFailedEvent) {
		t.Fatalf("expected ErrNoFailedEvent, got %v", err)
	}
}

// TestFromFailedStackAPIError surfaces describe failures so the caller can
// distinguish them from "no failed events" — the renderer chooses a
// different card for each.
func TestFromFailedStackAPIError(t *testing.T) {
	wantErr := fmt.Errorf("aws: throttling")
	api := &fakeStackEvents{err: wantErr}
	got, err := errors.FromFailedStack(context.Background(), api, "s", "us-east-1", nil)
	if got != nil {
		t.Errorf("expected nil AppError on API error, got %+v", got)
	}
	if err == nil || !stderrors.Is(err, wantErr) {
		t.Fatalf("expected wrapped API error, got %v", err)
	}
}

// TestFromFailedStackGuards covers the input validation: nil api and an
// empty stackName are caller bugs and the fetcher refuses to invent
// behaviour for them.
func TestFromFailedStackGuards(t *testing.T) {
	if _, err := errors.FromFailedStack(context.Background(), nil, "s", "us-east-1", nil); err == nil {
		t.Errorf("nil api accepted")
	}
	if _, err := errors.FromFailedStack(context.Background(), &fakeStackEvents{}, "", "us-east-1", nil); err == nil {
		t.Errorf("empty stackName accepted")
	}
}

// TestFromFailedStackUnknownFailure exercises the fallback path through
// FromFailedStack: the failed event has a Reason that no catalogue entry
// matches, so the returned AppError carries only Raw + AWS metadata.
func TestFromFailedStackUnknownFailure(t *testing.T) {
	api := &fakeStackEvents{
		events: []errors.StackEvent{
			{
				EventID:           "evt-1",
				LogicalResourceID: "MyResource",
				ResourceType:      "AWS::SomeService::SomeResource",
				ResourceStatus:    "CREATE_FAILED",
				Reason:            "totally unrecognised failure text",
				Time:              time.Unix(1000, 0),
			},
		},
	}
	got, err := errors.FromFailedStack(context.Background(), api, "s", "us-east-1", nil)
	if err != nil {
		t.Fatalf("FromFailedStack: %v", err)
	}
	if got.MatchedID != "" {
		t.Errorf("expected fallback, got matched id %q", got.MatchedID)
	}
	if got.StackName != "s" {
		t.Errorf("StackName not annotated: got %q", got.StackName)
	}
	if got.Resource != "MyResource" {
		t.Errorf("Resource not annotated: got %q", got.Resource)
	}
	if got.AWSService != "SomeService" {
		t.Errorf("AWSService not derived from ResourceType: got %q", got.AWSService)
	}
	if got.Raw != "totally unrecognised failure text" {
		t.Errorf("Raw not threaded through: got %q", got.Raw)
	}
}
