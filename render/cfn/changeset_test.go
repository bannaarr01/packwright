package cfn_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/bannaarr01/packwright/render/cfn"
)

// fakeChangeSetAPI is the in-process stand-in for *cloudformation.Client used
// across every change-set test. It captures every input so assertions can
// look at the wire payload, and runs caller-supplied handlers so each test
// can sculpt the response shape and timing it cares about.
type fakeChangeSetAPI struct {
	mu sync.Mutex

	createIn   []*cloudformation.CreateChangeSetInput
	describeIn []*cloudformation.DescribeChangeSetInput
	executeIn  []*cloudformation.ExecuteChangeSetInput
	deleteIn   []*cloudformation.DeleteChangeSetInput
	listIn     []*cloudformation.ListChangeSetsInput

	create   func(*cloudformation.CreateChangeSetInput) (*cloudformation.CreateChangeSetOutput, error)
	describe func(call int, in *cloudformation.DescribeChangeSetInput) (*cloudformation.DescribeChangeSetOutput, error)
	execute  func(*cloudformation.ExecuteChangeSetInput) (*cloudformation.ExecuteChangeSetOutput, error)
	delete   func(*cloudformation.DeleteChangeSetInput) (*cloudformation.DeleteChangeSetOutput, error)
	list     func(*cloudformation.ListChangeSetsInput) (*cloudformation.ListChangeSetsOutput, error)

	describeCalls int32
}

func (f *fakeChangeSetAPI) CreateChangeSet(_ context.Context, in *cloudformation.CreateChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.CreateChangeSetOutput, error) {
	f.mu.Lock()
	f.createIn = append(f.createIn, in)
	f.mu.Unlock()
	if f.create != nil {
		return f.create(in)
	}
	return &cloudformation.CreateChangeSetOutput{Id: aws.String("arn:cs:1"), StackId: aws.String("arn:stack:1")}, nil
}

func (f *fakeChangeSetAPI) DescribeChangeSet(_ context.Context, in *cloudformation.DescribeChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeChangeSetOutput, error) {
	idx := int(atomic.AddInt32(&f.describeCalls, 1)) - 1
	f.mu.Lock()
	f.describeIn = append(f.describeIn, in)
	f.mu.Unlock()
	if f.describe != nil {
		return f.describe(idx, in)
	}
	return &cloudformation.DescribeChangeSetOutput{
		Status: cfntypes.ChangeSetStatusCreateComplete,
	}, nil
}

func (f *fakeChangeSetAPI) ExecuteChangeSet(_ context.Context, in *cloudformation.ExecuteChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.ExecuteChangeSetOutput, error) {
	f.mu.Lock()
	f.executeIn = append(f.executeIn, in)
	f.mu.Unlock()
	if f.execute != nil {
		return f.execute(in)
	}
	return &cloudformation.ExecuteChangeSetOutput{}, nil
}

func (f *fakeChangeSetAPI) DeleteChangeSet(_ context.Context, in *cloudformation.DeleteChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.DeleteChangeSetOutput, error) {
	f.mu.Lock()
	f.deleteIn = append(f.deleteIn, in)
	f.mu.Unlock()
	if f.delete != nil {
		return f.delete(in)
	}
	return &cloudformation.DeleteChangeSetOutput{}, nil
}

func (f *fakeChangeSetAPI) ListChangeSets(_ context.Context, in *cloudformation.ListChangeSetsInput, _ ...func(*cloudformation.Options)) (*cloudformation.ListChangeSetsOutput, error) {
	f.mu.Lock()
	f.listIn = append(f.listIn, in)
	f.mu.Unlock()
	if f.list != nil {
		return f.list(in)
	}
	return &cloudformation.ListChangeSetsOutput{}, nil
}

// syntheticChangeSet returns a DescribeChangeSetOutput with one Modify and
// one Replace row — the canonical fixture ADR-0048 / the DoD calls out.
func syntheticChangeSet() *cloudformation.DescribeChangeSetOutput {
	return &cloudformation.DescribeChangeSetOutput{
		ChangeSetId:   aws.String("arn:cs:1"),
		ChangeSetName: aws.String("packwright-1700000000"),
		StackId:       aws.String("arn:stack:1"),
		StackName:     aws.String("acme-dev-alb"),
		Status:        cfntypes.ChangeSetStatusCreateComplete,
		Capabilities:  []cfntypes.Capability{cfntypes.CapabilityCapabilityIam},
		Parameters: []cfntypes.Parameter{
			{ParameterKey: aws.String("VpcId"), ParameterValue: aws.String("vpc-0abc")},
		},
		Changes: []cfntypes.Change{
			{
				Type: cfntypes.ChangeTypeResource,
				ResourceChange: &cfntypes.ResourceChange{
					Action:            cfntypes.ChangeActionModify,
					LogicalResourceId: aws.String("ApplicationLoadBalancer"),
					ResourceType:      aws.String("AWS::ElasticLoadBalancingV2::LoadBalancer"),
					Replacement:       cfntypes.ReplacementFalse,
					Scope:             []cfntypes.ResourceAttribute{cfntypes.ResourceAttributeProperties},
				},
			},
			{
				Type: cfntypes.ChangeTypeResource,
				ResourceChange: &cfntypes.ResourceChange{
					Action:            cfntypes.ChangeActionModify,
					LogicalResourceId: aws.String("DBInstance"),
					ResourceType:      aws.String("AWS::RDS::DBInstance"),
					Replacement:       cfntypes.ReplacementTrue,
					Scope:             []cfntypes.ResourceAttribute{cfntypes.ResourceAttributeProperties},
					Details: []cfntypes.ResourceChangeDetail{{
						Target: &cfntypes.ResourceTargetDefinition{
							Attribute:          cfntypes.ResourceAttributeProperties,
							Name:               aws.String("DBInstanceClass"),
							RequiresRecreation: cfntypes.RequiresRecreationAlways,
						},
						Evaluation:    cfntypes.EvaluationTypeStatic,
						ChangeSource:  cfntypes.ChangeSourceDirectModification,
						CausingEntity: aws.String("DBInstanceClass"),
					}},
				},
			},
		},
	}
}

func TestNewChangeSetName(t *testing.T) {
	got := cfn.NewChangeSetName(time.Unix(1_700_000_000, 0))
	want := "packwright-1700000000"
	if got != want {
		t.Errorf("NewChangeSetName = %q, want %q", got, want)
	}
	if !cfn.IsPackwrightChangeSet(got) {
		t.Errorf("IsPackwrightChangeSet(%q) = false, want true", got)
	}
	if cfn.IsPackwrightChangeSet("aws-managed-cs-1") {
		t.Error("IsPackwrightChangeSet(aws-managed) returned true")
	}
}

func TestCreateChangeSet_DefaultsAndInputShape(t *testing.T) {
	api := &fakeChangeSetAPI{}
	res, err := cfn.CreateChangeSet(context.Background(), api, cfn.CreateChangeSetInput{
		StackName:    "acme-dev-alb",
		TemplateBody: "{ \"Resources\": {} }",
		Parameters:   map[string]string{"VpcId": "vpc-0abc"},
		Capabilities: []string{"CAPABILITY_IAM"},
	})
	if err != nil {
		t.Fatalf("CreateChangeSet err = %v", err)
	}
	if res.ChangeSetID != "arn:cs:1" {
		t.Errorf("ChangeSetID = %q, want arn:cs:1", res.ChangeSetID)
	}
	if !cfn.IsPackwrightChangeSet(res.ChangeSetName) {
		t.Errorf("ChangeSetName = %q, want packwright- prefix", res.ChangeSetName)
	}
	if len(api.createIn) != 1 {
		t.Fatalf("CreateChangeSet calls = %d, want 1", len(api.createIn))
	}
	in := api.createIn[0]
	if aws.ToString(in.StackName) != "acme-dev-alb" {
		t.Errorf("StackName = %q, want acme-dev-alb", aws.ToString(in.StackName))
	}
	if in.ChangeSetType != cfntypes.ChangeSetTypeUpdate {
		t.Errorf("ChangeSetType = %q, want UPDATE", in.ChangeSetType)
	}
	if len(in.Parameters) != 1 || aws.ToString(in.Parameters[0].ParameterKey) != "VpcId" {
		t.Errorf("Parameters payload = %+v", in.Parameters)
	}
	if len(in.Capabilities) != 1 || in.Capabilities[0] != cfntypes.CapabilityCapabilityIam {
		t.Errorf("Capabilities = %+v", in.Capabilities)
	}
}

func TestCreateChangeSet_RejectsMissingTemplate(t *testing.T) {
	api := &fakeChangeSetAPI{}
	_, err := cfn.CreateChangeSet(context.Background(), api, cfn.CreateChangeSetInput{StackName: "s"})
	if err == nil {
		t.Fatal("CreateChangeSet err = nil, want error for missing template")
	}
}

func TestCreateChangeSet_RejectsBothTemplates(t *testing.T) {
	api := &fakeChangeSetAPI{}
	_, err := cfn.CreateChangeSet(context.Background(), api, cfn.CreateChangeSetInput{
		StackName:    "s",
		TemplateBody: "{}",
		TemplateURL:  "https://example.com/t.json",
	})
	if err == nil {
		t.Fatal("CreateChangeSet err = nil, want error for both templates set")
	}
}

func TestCreateChangeSet_RejectsEmptyStackName(t *testing.T) {
	api := &fakeChangeSetAPI{}
	_, err := cfn.CreateChangeSet(context.Background(), api, cfn.CreateChangeSetInput{TemplateBody: "{}"})
	if err == nil {
		t.Fatal("CreateChangeSet err = nil, want error for empty StackName")
	}
}

func TestDescribeChangeSet_MapsSyntheticBundleAndDetectsReplace(t *testing.T) {
	api := &fakeChangeSetAPI{describe: func(_ int, _ *cloudformation.DescribeChangeSetInput) (*cloudformation.DescribeChangeSetOutput, error) {
		return syntheticChangeSet(), nil
	}}
	res, err := cfn.DescribeChangeSet(context.Background(), api, "arn:cs:1", "acme-dev-alb")
	if err != nil {
		t.Fatalf("DescribeChangeSet err = %v", err)
	}
	if res.NoChanges {
		t.Error("NoChanges = true on synthetic with two changes")
	}
	if len(res.Changes) != 2 {
		t.Fatalf("Changes = %d, want 2", len(res.Changes))
	}
	var sawReplace bool
	for _, c := range res.Changes {
		if c.Replacement == string(cfntypes.ReplacementTrue) {
			sawReplace = true
			if c.LogicalResourceID != "DBInstance" {
				t.Errorf("replace target = %q, want DBInstance", c.LogicalResourceID)
			}
			if len(c.Details) != 1 || c.Details[0].Target.Name != "DBInstanceClass" {
				t.Errorf("replace details = %+v", c.Details)
			}
		}
	}
	if !sawReplace {
		t.Error("expected at least one Replacement=True change")
	}
}

func TestDescribeChangeSet_NoChangesDetected(t *testing.T) {
	api := &fakeChangeSetAPI{describe: func(_ int, _ *cloudformation.DescribeChangeSetInput) (*cloudformation.DescribeChangeSetOutput, error) {
		return &cloudformation.DescribeChangeSetOutput{
			Status:       cfntypes.ChangeSetStatusFailed,
			StatusReason: aws.String("The submitted information didn't contain changes. Submit different information to create a change set."),
		}, nil
	}}
	res, err := cfn.DescribeChangeSet(context.Background(), api, "arn:cs:1", "s")
	if err != nil {
		t.Fatalf("DescribeChangeSet err = %v", err)
	}
	if !res.NoChanges {
		t.Errorf("NoChanges = false, want true for AWS \"didn't contain changes\" reason")
	}

	// "No updates are to be performed" — the deploy.sh equivalent phrasing.
	api2 := &fakeChangeSetAPI{describe: func(_ int, _ *cloudformation.DescribeChangeSetInput) (*cloudformation.DescribeChangeSetOutput, error) {
		return &cloudformation.DescribeChangeSetOutput{
			Status:       cfntypes.ChangeSetStatusFailed,
			StatusReason: aws.String("No updates are to be performed."),
		}, nil
	}}
	res2, err := cfn.DescribeChangeSet(context.Background(), api2, "arn:cs:1", "s")
	if err != nil {
		t.Fatalf("DescribeChangeSet (no-updates) err = %v", err)
	}
	if !res2.NoChanges {
		t.Error("NoChanges = false for \"No updates are to be performed\"")
	}
}

func TestDescribeChangeSet_FailedForOtherReasonIsNotNoChanges(t *testing.T) {
	api := &fakeChangeSetAPI{describe: func(_ int, _ *cloudformation.DescribeChangeSetInput) (*cloudformation.DescribeChangeSetOutput, error) {
		return &cloudformation.DescribeChangeSetOutput{
			Status:       cfntypes.ChangeSetStatusFailed,
			StatusReason: aws.String("Template format error: at line 12"),
		}, nil
	}}
	res, err := cfn.DescribeChangeSet(context.Background(), api, "arn:cs:1", "s")
	if err != nil {
		t.Fatalf("DescribeChangeSet err = %v", err)
	}
	if res.NoChanges {
		t.Error("NoChanges = true on a genuine template-format failure")
	}
}

func TestPollDescribeChangeSet_TerminalAfterTwoPolls(t *testing.T) {
	api := &fakeChangeSetAPI{describe: func(call int, _ *cloudformation.DescribeChangeSetInput) (*cloudformation.DescribeChangeSetOutput, error) {
		if call == 0 {
			return &cloudformation.DescribeChangeSetOutput{Status: cfntypes.ChangeSetStatusCreateInProgress}, nil
		}
		return syntheticChangeSet(), nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := cfn.PollDescribeChangeSet(ctx, api, "arn:cs:1", "acme-dev-alb", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("PollDescribeChangeSet err = %v", err)
	}
	if res.Status != string(cfntypes.ChangeSetStatusCreateComplete) {
		t.Errorf("final status = %q, want CREATE_COMPLETE", res.Status)
	}
	if got := atomic.LoadInt32(&api.describeCalls); got < 2 {
		t.Errorf("describe calls = %d, want ≥ 2", got)
	}
}

func TestPollDescribeChangeSet_ContextCancelReturnsLast(t *testing.T) {
	api := &fakeChangeSetAPI{describe: func(_ int, _ *cloudformation.DescribeChangeSetInput) (*cloudformation.DescribeChangeSetOutput, error) {
		return &cloudformation.DescribeChangeSetOutput{Status: cfntypes.ChangeSetStatusCreateInProgress}, nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	res, err := cfn.PollDescribeChangeSet(ctx, api, "arn:cs:1", "s", 10*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	if res.Status != string(cfntypes.ChangeSetStatusCreateInProgress) {
		t.Errorf("last status = %q, want CREATE_IN_PROGRESS", res.Status)
	}
}

func TestExecuteAndDeleteChangeSet_FailFastOnNilAndEmpty(t *testing.T) {
	if err := cfn.ExecuteChangeSet(context.Background(), nil, "id", "s"); err == nil {
		t.Error("ExecuteChangeSet(nil api) err = nil, want error")
	}
	if err := cfn.ExecuteChangeSet(context.Background(), &fakeChangeSetAPI{}, "", "s"); err == nil {
		t.Error("ExecuteChangeSet(empty id) err = nil, want error")
	}
	if err := cfn.DeleteChangeSet(context.Background(), nil, "id", "s"); err == nil {
		t.Error("DeleteChangeSet(nil api) err = nil, want error")
	}
	if err := cfn.DeleteChangeSet(context.Background(), &fakeChangeSetAPI{}, "", "s"); err == nil {
		t.Error("DeleteChangeSet(empty id) err = nil, want error")
	}
}

func TestExecuteChangeSet_PassesNames(t *testing.T) {
	api := &fakeChangeSetAPI{}
	if err := cfn.ExecuteChangeSet(context.Background(), api, "arn:cs:1", "acme-dev-alb"); err != nil {
		t.Fatalf("ExecuteChangeSet err = %v", err)
	}
	if len(api.executeIn) != 1 {
		t.Fatalf("ExecuteChangeSet calls = %d, want 1", len(api.executeIn))
	}
	if aws.ToString(api.executeIn[0].ChangeSetName) != "arn:cs:1" {
		t.Errorf("ChangeSetName = %q, want arn:cs:1", aws.ToString(api.executeIn[0].ChangeSetName))
	}
	if aws.ToString(api.executeIn[0].StackName) != "acme-dev-alb" {
		t.Errorf("StackName = %q, want acme-dev-alb", aws.ToString(api.executeIn[0].StackName))
	}
}

func TestDeleteChangeSet_PassesNames(t *testing.T) {
	api := &fakeChangeSetAPI{}
	if err := cfn.DeleteChangeSet(context.Background(), api, "arn:cs:1", "acme-dev-alb"); err != nil {
		t.Fatalf("DeleteChangeSet err = %v", err)
	}
	if len(api.deleteIn) != 1 {
		t.Fatalf("DeleteChangeSet calls = %d, want 1", len(api.deleteIn))
	}
	if aws.ToString(api.deleteIn[0].ChangeSetName) != "arn:cs:1" {
		t.Errorf("ChangeSetName = %q, want arn:cs:1", aws.ToString(api.deleteIn[0].ChangeSetName))
	}
}

func TestListChangeSets_AggregatesPages(t *testing.T) {
	api := &fakeChangeSetAPI{}
	calls := 0
	api.list = func(in *cloudformation.ListChangeSetsInput) (*cloudformation.ListChangeSetsOutput, error) {
		calls++
		switch calls {
		case 1:
			return &cloudformation.ListChangeSetsOutput{
				Summaries: []cfntypes.ChangeSetSummary{
					{ChangeSetId: aws.String("arn:cs:1"), ChangeSetName: aws.String("packwright-1"), Status: cfntypes.ChangeSetStatusCreateComplete},
				},
				NextToken: aws.String("p2"),
			}, nil
		case 2:
			if aws.ToString(in.NextToken) != "p2" {
				t.Errorf("page 2 NextToken = %q, want p2", aws.ToString(in.NextToken))
			}
			return &cloudformation.ListChangeSetsOutput{
				Summaries: []cfntypes.ChangeSetSummary{
					{ChangeSetId: aws.String("arn:cs:2"), ChangeSetName: aws.String("packwright-2"), Status: cfntypes.ChangeSetStatusCreateComplete},
				},
			}, nil
		}
		return &cloudformation.ListChangeSetsOutput{}, nil
	}
	out, err := cfn.ListChangeSets(context.Background(), api, "acme-dev-alb")
	if err != nil {
		t.Fatalf("ListChangeSets err = %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("summaries = %d, want 2", len(out))
	}
	if out[0].ChangeSetName != "packwright-1" || out[1].ChangeSetName != "packwright-2" {
		t.Errorf("summaries = %+v", out)
	}
}

func TestIsTerminalChangeSetStatus(t *testing.T) {
	cases := map[string]bool{
		"CREATE_COMPLETE":    true,
		"FAILED":             true,
		"DELETE_COMPLETE":    true,
		"DELETE_FAILED":      true,
		"CREATE_IN_PROGRESS": false,
		"":                   false,
		"create_complete":    true,
	}
	for in, want := range cases {
		if got := cfn.IsTerminalChangeSetStatus(in); got != want {
			t.Errorf("IsTerminalChangeSetStatus(%q) = %v, want %v", in, got, want)
		}
	}
}
