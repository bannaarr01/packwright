package update

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/bannaarr01/packwright/render/cfn"
)

// fakeAPI implements cfn.ChangeSetAPI for the coordinator integration
// tests. Hooks let each test sculpt the change set Describe returns and
// observe Execute / Delete calls.
type fakeAPI struct {
	describe func(call int) (*cloudformation.DescribeChangeSetOutput, error)
	create   func(*cloudformation.CreateChangeSetInput) (*cloudformation.CreateChangeSetOutput, error)
	execute  func(*cloudformation.ExecuteChangeSetInput) (*cloudformation.ExecuteChangeSetOutput, error)
	delete   func(*cloudformation.DeleteChangeSetInput) (*cloudformation.DeleteChangeSetOutput, error)

	createCalls   int32
	describeCalls int32
	executeCalls  int32
	deleteCalls   int32
	lastDeleteID  string
}

func (f *fakeAPI) CreateChangeSet(_ context.Context, in *cloudformation.CreateChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.CreateChangeSetOutput, error) {
	atomic.AddInt32(&f.createCalls, 1)
	if f.create != nil {
		return f.create(in)
	}
	return &cloudformation.CreateChangeSetOutput{Id: aws.String("arn:cs:1"), StackId: aws.String("arn:stack:1")}, nil
}

func (f *fakeAPI) DescribeChangeSet(_ context.Context, _ *cloudformation.DescribeChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeChangeSetOutput, error) {
	idx := int(atomic.AddInt32(&f.describeCalls, 1)) - 1
	if f.describe != nil {
		return f.describe(idx)
	}
	return &cloudformation.DescribeChangeSetOutput{Status: cfntypes.ChangeSetStatusCreateComplete}, nil
}

func (f *fakeAPI) ExecuteChangeSet(_ context.Context, in *cloudformation.ExecuteChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.ExecuteChangeSetOutput, error) {
	atomic.AddInt32(&f.executeCalls, 1)
	if f.execute != nil {
		return f.execute(in)
	}
	return &cloudformation.ExecuteChangeSetOutput{}, nil
}

func (f *fakeAPI) DeleteChangeSet(_ context.Context, in *cloudformation.DeleteChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.DeleteChangeSetOutput, error) {
	atomic.AddInt32(&f.deleteCalls, 1)
	f.lastDeleteID = aws.ToString(in.ChangeSetName)
	if f.delete != nil {
		return f.delete(in)
	}
	return &cloudformation.DeleteChangeSetOutput{}, nil
}

func (f *fakeAPI) ListChangeSets(_ context.Context, _ *cloudformation.ListChangeSetsInput, _ ...func(*cloudformation.Options)) (*cloudformation.ListChangeSetsOutput, error) {
	return &cloudformation.ListChangeSetsOutput{}, nil
}

// describeSynthetic returns the canonical "one Modify + one Replace"
// fixture the DoD asks the integration tests to drive against.
func describeSynthetic() *cloudformation.DescribeChangeSetOutput {
	return &cloudformation.DescribeChangeSetOutput{
		ChangeSetId:   aws.String("arn:cs:1"),
		ChangeSetName: aws.String("packwright-1700000000"),
		StackId:       aws.String("arn:stack:1"),
		StackName:     aws.String("acme-dev-alb"),
		Status:        cfntypes.ChangeSetStatusCreateComplete,
		Changes: []cfntypes.Change{
			{
				Type: cfntypes.ChangeTypeResource,
				ResourceChange: &cfntypes.ResourceChange{
					Action:            cfntypes.ChangeActionModify,
					LogicalResourceId: aws.String("ALB"),
					ResourceType:      aws.String("AWS::ElasticLoadBalancingV2::LoadBalancer"),
					Replacement:       cfntypes.ReplacementFalse,
				},
			},
			{
				Type: cfntypes.ChangeTypeResource,
				ResourceChange: &cfntypes.ResourceChange{
					Action:            cfntypes.ChangeActionModify,
					LogicalResourceId: aws.String("DBInstance"),
					ResourceType:      aws.String("AWS::RDS::DBInstance"),
					Replacement:       cfntypes.ReplacementTrue,
					Details: []cfntypes.ResourceChangeDetail{{
						Target: &cfntypes.ResourceTargetDefinition{
							Name:               aws.String("DBInstanceClass"),
							RequiresRecreation: cfntypes.RequiresRecreationAlways,
						},
					}},
				},
			},
		},
	}
}

// describeNoChanges mimics AWS's "no updates to perform" FAILED status.
func describeNoChanges() *cloudformation.DescribeChangeSetOutput {
	return &cloudformation.DescribeChangeSetOutput{
		ChangeSetId:   aws.String("arn:cs:1"),
		ChangeSetName: aws.String("packwright-1700000000"),
		StackId:       aws.String("arn:stack:1"),
		StackName:     aws.String("acme-dev-alb"),
		Status:        cfntypes.ChangeSetStatusFailed,
		StatusReason:  aws.String("No updates are to be performed."),
	}
}

func TestStack_BlocksExecuteUntilConsentApproves(t *testing.T) {
	api := &fakeAPI{describe: func(_ int) (*cloudformation.DescribeChangeSetOutput, error) {
		return describeSynthetic(), nil
	}}
	// consent gate that flips approve after Execute would have been blocked.
	approved := atomic.Bool{}
	consent := func(_ context.Context, p ReplacementPayload) ConsentDecision {
		// Before consent runs, Execute must NOT have been called yet.
		if atomic.LoadInt32(&api.executeCalls) != 0 {
			t.Errorf("ExecuteChangeSet was called before consent gate ran (%d)", api.executeCalls)
		}
		if p.Count != 1 || p.Rows[0].LogicalID != "DBInstance" {
			t.Errorf("consent payload = %+v, want 1 row for DBInstance", p)
		}
		approved.Store(true)
		return ConsentApprove
	}

	harvestRan := false
	harvest := func(_ context.Context, info HarvestInfo) error {
		if info.HistoryKind != HistoryKind {
			t.Errorf("HistoryKind = %q, want update", info.HistoryKind)
		}
		if info.ChangeSetID != "arn:cs:1" {
			t.Errorf("ChangeSetID = %q, want arn:cs:1", info.ChangeSetID)
		}
		if info.Diff.Total() != 2 {
			t.Errorf("diff total = %d, want 2", info.Diff.Total())
		}
		harvestRan = true
		return nil
	}

	res, err := Stack(context.Background(), StackInput{
		StackName:          "acme-dev-alb",
		TemplateBody:       "{}",
		Parameters:         map[string]string{"DBInstanceClass": "db.t3.large"},
		PreviousParameters: map[string]string{"DBInstanceClass": "db.t3.medium"},
	}, StackOptions{
		API:          api,
		Consent:      consent,
		Harvest:      harvest,
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Stack err = %v", err)
	}
	if res.Outcome != OutcomeExecuted {
		t.Errorf("Outcome = %v, want OutcomeExecuted", res.Outcome)
	}
	if !approved.Load() {
		t.Error("consent gate was not consulted")
	}
	if atomic.LoadInt32(&api.executeCalls) != 1 {
		t.Errorf("Execute calls = %d, want 1", api.executeCalls)
	}
	if !harvestRan {
		t.Error("harvest was not called after Execute")
	}
	if atomic.LoadInt32(&api.deleteCalls) != 0 {
		t.Errorf("Delete calls = %d, want 0 on approved execute path", api.deleteCalls)
	}
	if !res.Replacement.HasReplacements() {
		t.Error("StackResult.Replacement.HasReplacements = false, want true")
	}
	if res.Events == nil {
		t.Error("StackResult.Events = nil, want closed channel even without streamer")
	}
}

func TestStack_DeniedConsentDoesNotExecute(t *testing.T) {
	api := &fakeAPI{describe: func(_ int) (*cloudformation.DescribeChangeSetOutput, error) {
		return describeSynthetic(), nil
	}}
	res, err := Stack(context.Background(), StackInput{
		StackName:    "acme-dev-alb",
		TemplateBody: "{}",
	}, StackOptions{
		API:          api,
		Consent:      func(_ context.Context, _ ReplacementPayload) ConsentDecision { return ConsentDeny },
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Stack err = %v", err)
	}
	if res.Outcome != OutcomeConsentDenied {
		t.Errorf("Outcome = %v, want OutcomeConsentDenied", res.Outcome)
	}
	if atomic.LoadInt32(&api.executeCalls) != 0 {
		t.Errorf("Execute calls = %d, want 0 after deny", api.executeCalls)
	}
	if atomic.LoadInt32(&api.deleteCalls) != 1 {
		t.Errorf("Delete calls = %d, want 1 (cleanup after deny)", api.deleteCalls)
	}
}

func TestStack_NoChangesRendersFriendlyNoticeAndSkipsHarvest(t *testing.T) {
	api := &fakeAPI{describe: func(_ int) (*cloudformation.DescribeChangeSetOutput, error) {
		return describeNoChanges(), nil
	}}
	harvestCalled := false
	res, err := Stack(context.Background(), StackInput{
		StackName:    "acme-dev-alb",
		TemplateBody: "{}",
	}, StackOptions{
		API:          api,
		Harvest:      func(_ context.Context, _ HarvestInfo) error { harvestCalled = true; return nil },
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Stack err = %v on no-changes (want nil error / friendly notice)", err)
	}
	if res.Outcome != OutcomeNoChanges {
		t.Errorf("Outcome = %v, want OutcomeNoChanges", res.Outcome)
	}
	if !strings.Contains(res.Notice, "No changes") {
		t.Errorf("Notice = %q, want friendly no-changes message", res.Notice)
	}
	if atomic.LoadInt32(&api.executeCalls) != 0 {
		t.Errorf("Execute calls = %d, want 0 on no-changes path", api.executeCalls)
	}
	if atomic.LoadInt32(&api.deleteCalls) != 1 {
		t.Errorf("Delete calls = %d, want 1 (empty change set torn down)", api.deleteCalls)
	}
	if harvestCalled {
		t.Error("harvest ran on no-changes path; want skipped")
	}
}

func TestStack_NoReplacementSkipsConsentGate(t *testing.T) {
	api := &fakeAPI{describe: func(_ int) (*cloudformation.DescribeChangeSetOutput, error) {
		out := describeSynthetic()
		// strip the replace row so only the Modify remains
		out.Changes = out.Changes[:1]
		return out, nil
	}}
	consentCalled := false
	res, err := Stack(context.Background(), StackInput{
		StackName:    "acme-dev-alb",
		TemplateBody: "{}",
	}, StackOptions{
		API: api,
		Consent: func(_ context.Context, _ ReplacementPayload) ConsentDecision {
			consentCalled = true
			return ConsentApprove
		},
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Stack err = %v", err)
	}
	if res.Outcome != OutcomeExecuted {
		t.Errorf("Outcome = %v, want OutcomeExecuted", res.Outcome)
	}
	if consentCalled {
		t.Error("consent gate ran with no replacements; want skipped")
	}
	if atomic.LoadInt32(&api.executeCalls) != 1 {
		t.Errorf("Execute calls = %d, want 1", api.executeCalls)
	}
}

func TestStack_ValidatorBlocksCreate(t *testing.T) {
	api := &fakeAPI{}
	_, err := Stack(context.Background(), StackInput{
		StackName:    "acme-dev-alb",
		TemplateBody: "{}",
	}, StackOptions{
		API:      api,
		Validate: func(_ context.Context) error { return errors.New("policy: missing tag") },
	})
	if err == nil || !strings.Contains(err.Error(), "policy: missing tag") {
		t.Fatalf("err = %v, want validator error to bubble up", err)
	}
	if atomic.LoadInt32(&api.createCalls) != 0 {
		t.Errorf("Create calls = %d, want 0 when validator fails", api.createCalls)
	}
}

func TestStack_RejectsMissingTemplate(t *testing.T) {
	api := &fakeAPI{}
	_, err := Stack(context.Background(), StackInput{StackName: "s"}, StackOptions{API: api})
	if err == nil {
		t.Error("err = nil, want validation for missing template")
	}
}

func TestStack_RejectsNilAPI(t *testing.T) {
	_, err := Stack(context.Background(), StackInput{StackName: "s", TemplateBody: "{}"}, StackOptions{})
	if err == nil {
		t.Error("err = nil, want explicit error for nil API")
	}
}

func TestStack_StreamForwardedAfterExecute(t *testing.T) {
	api := &fakeAPI{describe: func(_ int) (*cloudformation.DescribeChangeSetOutput, error) {
		// no-replacement diff so the consent gate is skipped
		out := describeSynthetic()
		out.Changes = out.Changes[:1]
		return out, nil
	}}
	want := cfn.StackEvent{EventID: "e1", StackName: "acme-dev-alb", ResourceType: "AWS::CloudFormation::Stack", ResourceStatus: "UPDATE_COMPLETE", Time: time.Now()}
	stream := func(_ context.Context, _ string) <-chan cfn.StackEvent {
		ch := make(chan cfn.StackEvent, 1)
		ch <- want
		close(ch)
		return ch
	}
	res, err := Stack(context.Background(), StackInput{StackName: "acme-dev-alb", TemplateBody: "{}"}, StackOptions{
		API: api, Stream: stream, PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Stack err = %v", err)
	}
	if res.Events == nil {
		t.Fatal("Events = nil")
	}
	got := <-res.Events
	if got.EventID != want.EventID {
		t.Errorf("forwarded event = %+v, want %+v", got, want)
	}
}

func TestStack_NotifiesOnExecuteFailure(t *testing.T) {
	api := &fakeAPI{
		describe: func(_ int) (*cloudformation.DescribeChangeSetOutput, error) {
			out := describeSynthetic()
			out.Changes = out.Changes[:1]
			return out, nil
		},
		execute: func(_ *cloudformation.ExecuteChangeSetInput) (*cloudformation.ExecuteChangeSetOutput, error) {
			return nil, errors.New("ValidationError: stack already in UPDATE_IN_PROGRESS")
		},
	}
	_, err := Stack(context.Background(), StackInput{StackName: "s", TemplateBody: "{}"}, StackOptions{
		API: api, PollInterval: time.Millisecond,
	})
	if err == nil {
		t.Fatal("err = nil, want execute failure to surface")
	}
	if atomic.LoadInt32(&api.deleteCalls) != 1 {
		t.Errorf("Delete calls = %d, want 1 (cleanup after Execute failure)", api.deleteCalls)
	}
}

func TestStack_FailedNonNoChangesReturnsError(t *testing.T) {
	api := &fakeAPI{describe: func(_ int) (*cloudformation.DescribeChangeSetOutput, error) {
		return &cloudformation.DescribeChangeSetOutput{
			ChangeSetId:  aws.String("arn:cs:1"),
			Status:       cfntypes.ChangeSetStatusFailed,
			StatusReason: aws.String("Template format error: at line 7"),
		}, nil
	}}
	_, err := Stack(context.Background(), StackInput{StackName: "s", TemplateBody: "{}"}, StackOptions{
		API: api, PollInterval: time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "Template format error") {
		t.Fatalf("err = %v, want template format error", err)
	}
	if atomic.LoadInt32(&api.deleteCalls) != 1 {
		t.Errorf("Delete calls = %d, want 1 after CreateChangeSet failure cleanup", api.deleteCalls)
	}
}
