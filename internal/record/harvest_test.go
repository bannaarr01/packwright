package record

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/smithy-go"
)

// fakeCFN is a hand-rolled cloudFormationAPI for harvest tests. Each Describe
// call returns the stub configured for the named stack; errCalls / resErrCalls
// inject errors for the respective entry point.
type fakeCFN struct {
	stacks    map[string][]cfntypes.Stack
	resources map[string][]cfntypes.StackResource
	errCalls  map[string]error
	resErrors map[string]error
}

func newFakeCFN() *fakeCFN {
	return &fakeCFN{
		stacks:    map[string][]cfntypes.Stack{},
		resources: map[string][]cfntypes.StackResource{},
		errCalls:  map[string]error{},
		resErrors: map[string]error{},
	}
}

func (f *fakeCFN) DescribeStacks(_ context.Context, in *cloudformation.DescribeStacksInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	name := aws.ToString(in.StackName)
	if err := f.errCalls[name]; err != nil {
		return nil, err
	}
	return &cloudformation.DescribeStacksOutput{Stacks: f.stacks[name]}, nil
}

func (f *fakeCFN) DescribeStackResources(_ context.Context, in *cloudformation.DescribeStackResourcesInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeStackResourcesOutput, error) {
	name := aws.ToString(in.StackName)
	if err := f.resErrors[name]; err != nil {
		return nil, err
	}
	return &cloudformation.DescribeStackResourcesOutput{StackResources: f.resources[name]}, nil
}

// stackNotFoundError mimics the smithy.APIError shape AWS returns when
// DescribeStacks is called on a missing stack. PR-02's harvest treats it as
// "deleted in AWS".
type stackNotFoundError struct{}

func (stackNotFoundError) Error() string {
	return "ValidationError: Stack with id missing-stack does not exist"
}
func (stackNotFoundError) ErrorCode() string             { return "ValidationError" }
func (stackNotFoundError) ErrorMessage() string          { return "Stack with id missing-stack does not exist" }
func (stackNotFoundError) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

// silentLogger swallows everything — keeps go test -v output focused on the
// assertions rather than the recorder's debug lines.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// canonicalIdentity is the project/env stamping used by the harvest tests so
// each test's file lands at the same on-disk path.
func canonicalIdentity() Identity {
	return Identity{
		Project: "acme",
		Env:     "dev",
		Profile: "acme-dev",
		Region:  "eu-west-1",
		Account: "123456789012",
		Manifest: ManifestRef{
			Slash:  "/alb",
			Source: "packs/reference/manifests/alb.yaml",
		},
	}
}

// pinTime installs a deterministic nowFunc for the lifetime of one test and
// restores the real clock on cleanup. Records made by the harvest then have
// stable timestamps to assert against.
func pinTime(t *testing.T, ts time.Time) {
	t.Helper()
	prev := nowFunc
	nowFunc = func() time.Time { return ts }
	t.Cleanup(func() { nowFunc = prev })
}

// fixtureStack builds a CFN stack reply matching ADR-0046's example payload.
// stackStatus is the only field tests typically override.
func fixtureStack(stackName, stackStatus string, ts time.Time) cfntypes.Stack {
	return cfntypes.Stack{
		StackName:       aws.String(stackName),
		StackStatus:     cfntypes.StackStatus(stackStatus),
		CreationTime:    aws.Time(ts),
		LastUpdatedTime: aws.Time(ts),
		Parameters: []cfntypes.Parameter{
			{ParameterKey: aws.String("VpcId"), ParameterValue: aws.String("vpc-0abc1234")},
			{ParameterKey: aws.String("SubnetIds"), ParameterValue: aws.String("subnet-a,subnet-b")},
		},
		Outputs: []cfntypes.Output{
			{OutputKey: aws.String("LoadBalancerArn"), OutputValue: aws.String("arn:aws:elasticloadbalancing:eu-west-1:123:lb")},
			{OutputKey: aws.String("LoadBalancerDNSName"), OutputValue: aws.String("alb-dev.elb.amazonaws.com")},
		},
	}
}

// fixtureResources returns n resources, all CREATE_COMPLETE — used to drive
// the "12/12 CREATE_COMPLETE" partial-discrepancy scenario.
func fixtureResources(n int) []cfntypes.StackResource {
	out := make([]cfntypes.StackResource, n)
	for i := range out {
		out[i] = cfntypes.StackResource{
			LogicalResourceId:  aws.String(fmt.Sprintf("Res%d", i)),
			PhysicalResourceId: aws.String(fmt.Sprintf("phy-%d", i)),
			ResourceType:       aws.String("AWS::ElasticLoadBalancingV2::LoadBalancer"),
			ResourceStatus:     cfntypes.ResourceStatus("CREATE_COMPLETE"),
		}
	}
	return out
}

func TestHarvest_HappyPath_WritesRecord(t *testing.T) {
	pinTime(t, time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC))
	stackName := "alb-dev-stack"
	cfn := newFakeCFN()
	cfn.stacks[stackName] = []cfntypes.Stack{fixtureStack(stackName, "CREATE_COMPLETE", nowFunc())}
	cfn.resources[stackName] = fixtureResources(3)

	store := NewStore(t.TempDir())
	rec := &Recorder{CFN: cfn, Store: store, Identity: canonicalIdentity(), Logger: silentLogger()}

	if err := rec.Harvest(context.Background(), stackName, nil); err != nil {
		t.Fatalf("Harvest: %v", err)
	}

	got, err := store.Read("acme", "dev", stackName)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Status.CFN != "CREATE_COMPLETE" || got.Status.Broad != BroadDeployed {
		t.Errorf("status = %+v, want deployed", got.Status)
	}
	if len(got.Resources) != 3 || len(got.Outputs) != 2 || len(got.Parameters) != 2 {
		t.Errorf("counts: resources=%d outputs=%d params=%d", len(got.Resources), len(got.Outputs), len(got.Parameters))
	}
	if len(got.History) != 1 || got.History[0].Kind != KindCreate || got.History[0].Result != ResultSuccess {
		t.Errorf("history = %+v", got.History)
	}
	if got.Manifest.Slash != "/alb" || got.Account != "123456789012" {
		t.Errorf("identity not stamped: %+v", got)
	}
}

func TestHarvest_PartialDiscrepancy_TwelveOfTwelveCreateComplete(t *testing.T) {
	pinTime(t, time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC))
	stackName := "alb-dev-stack"
	cfn := newFakeCFN()
	cfn.stacks[stackName] = []cfntypes.Stack{fixtureStack(stackName, "ROLLBACK_COMPLETE", nowFunc())}
	cfn.resources[stackName] = fixtureResources(12)

	store := NewStore(t.TempDir())
	rec := &Recorder{CFN: cfn, Store: store, Identity: canonicalIdentity(), Logger: silentLogger()}

	if err := rec.Harvest(context.Background(), stackName, errors.New("deploy script exit 1")); err != nil {
		t.Fatalf("Harvest: %v", err)
	}

	got, err := store.Read("acme", "dev", stackName)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Status.Broad != BroadPartial {
		t.Fatalf("Status.Broad = %q, want %q", got.Status.Broad, BroadPartial)
	}
	if got.Status.Discrepancy == "" {
		t.Errorf("Status.Discrepancy empty; want a note explaining the disagreement")
	}
	// Even though the deploy script failed, CFN looks ok on the resource
	// side — we still write the record. The history row should reflect
	// the script failure though.
	if got.History[0].Result != ResultFailure {
		t.Errorf("history result = %q, want failure (deploy script exit 1)", got.History[0].Result)
	}
}

func TestHarvest_AppendsHistoryOnRedeploy(t *testing.T) {
	stackName := "alb-dev-stack"

	cfn := newFakeCFN()
	cfn.stacks[stackName] = []cfntypes.Stack{fixtureStack(stackName, "CREATE_COMPLETE", time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC))}
	cfn.resources[stackName] = fixtureResources(2)

	store := NewStore(t.TempDir())
	rec := &Recorder{CFN: cfn, Store: store, Identity: canonicalIdentity(), Logger: silentLogger()}

	pinTime(t, time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC))
	if err := rec.Harvest(context.Background(), stackName, nil); err != nil {
		t.Fatalf("Harvest #1: %v", err)
	}
	pinTime(t, time.Date(2026, 6, 18, 11, 0, 0, 0, time.UTC))
	if err := rec.Harvest(context.Background(), stackName, nil); err != nil {
		t.Fatalf("Harvest #2: %v", err)
	}

	got, err := store.Read("acme", "dev", stackName)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got.History) != 2 {
		t.Fatalf("history len = %d, want 2 (append, not duplicate)", len(got.History))
	}
	if !got.History[0].At.Before(got.History[1].At) {
		t.Errorf("history not in chronological order: %+v", got.History)
	}
	// Other fields (outputs, resources, identity) should be merged in
	// place — counts must not have doubled.
	if len(got.Outputs) != 2 || len(got.Resources) != 2 {
		t.Errorf("merged fields duplicated: outputs=%d resources=%d", len(got.Outputs), len(got.Resources))
	}
}

func TestHarvest_HistoryCapsAt50(t *testing.T) {
	pinTime(t, time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC))
	stackName := "alb-dev-stack"
	cfn := newFakeCFN()
	cfn.stacks[stackName] = []cfntypes.Stack{fixtureStack(stackName, "CREATE_COMPLETE", nowFunc())}
	cfn.resources[stackName] = fixtureResources(1)

	store := NewStore(t.TempDir())
	rec := &Recorder{CFN: cfn, Store: store, Identity: canonicalIdentity(), Logger: silentLogger()}

	// Prime 51 history entries then harvest; final length should be 50.
	ctx := context.Background()
	for i := 0; i < 51; i++ {
		pinTime(t, time.Date(2026, 6, 18, 10, i, 0, 0, time.UTC))
		if err := rec.Harvest(ctx, stackName, nil); err != nil {
			t.Fatalf("Harvest #%d: %v", i, err)
		}
	}
	got, err := store.Read("acme", "dev", stackName)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got.History) != MaxHistoryEntries {
		t.Errorf("history len = %d, want %d", len(got.History), MaxHistoryEntries)
	}
}

func TestHarvest_StackNotFound_NoPriorRecord_NoOp(t *testing.T) {
	pinTime(t, time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC))
	stackName := "missing-stack"
	cfn := newFakeCFN()
	cfn.errCalls[stackName] = stackNotFoundError{}

	store := NewStore(t.TempDir())
	rec := &Recorder{CFN: cfn, Store: store, Identity: canonicalIdentity(), Logger: silentLogger()}

	if err := rec.Harvest(context.Background(), stackName, errors.New("deploy failed before stack creation")); err != nil {
		t.Fatalf("Harvest: %v", err)
	}
	// No file should be written when there is nothing on either side.
	if _, err := store.Read("acme", "dev", stackName); err == nil {
		t.Errorf("Read: expected fs.ErrNotExist, got record")
	}
}

func TestHarvest_StackNotFound_WithPriorRecord_MarksDeleted(t *testing.T) {
	pinTime(t, time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC))
	stackName := "alb-dev-stack"
	store := NewStore(t.TempDir())

	// Seed a prior "deployed" record on disk.
	priorCFN := newFakeCFN()
	priorCFN.stacks[stackName] = []cfntypes.Stack{fixtureStack(stackName, "CREATE_COMPLETE", nowFunc())}
	priorCFN.resources[stackName] = fixtureResources(1)
	prior := &Recorder{CFN: priorCFN, Store: store, Identity: canonicalIdentity(), Logger: silentLogger()}
	if err := prior.Harvest(context.Background(), stackName, nil); err != nil {
		t.Fatalf("seed Harvest: %v", err)
	}

	// Now harvest against a CFN that says the stack is gone.
	missingCFN := newFakeCFN()
	missingCFN.errCalls[stackName] = stackNotFoundError{}
	pinTime(t, time.Date(2026, 6, 18, 11, 0, 0, 0, time.UTC))
	rec := &Recorder{CFN: missingCFN, Store: store, Identity: canonicalIdentity(), Logger: silentLogger()}
	if err := rec.Harvest(context.Background(), stackName, nil); err != nil {
		t.Fatalf("Harvest: %v", err)
	}

	got, err := store.Read("acme", "dev", stackName)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Status.Broad != BroadDeleted {
		t.Errorf("Status.Broad = %q, want %q", got.Status.Broad, BroadDeleted)
	}
	if len(got.History) != 2 || got.History[1].Kind != KindDeleteAttempt {
		t.Errorf("history did not record delete attempt: %+v", got.History)
	}
}

func TestHarvest_DescribeStacksError_NotMissing_ReturnsError(t *testing.T) {
	stackName := "alb-dev-stack"
	cfn := newFakeCFN()
	cfn.errCalls[stackName] = errors.New("throttled: try again later")

	store := NewStore(t.TempDir())
	rec := &Recorder{CFN: cfn, Store: store, Identity: canonicalIdentity(), Logger: silentLogger()}

	if err := rec.Harvest(context.Background(), stackName, nil); err == nil {
		t.Errorf("Harvest: expected error, got nil")
	}
}

// TestRecorder_Hook_SwallowsErrors confirms the engine-facing hook never
// returns or panics on a failure path — the deploy must continue past a
// harvest miss.
func TestRecorder_Hook_SwallowsErrors(t *testing.T) {
	stackName := "alb-dev-stack"
	cfn := newFakeCFN()
	cfn.errCalls[stackName] = errors.New("ec2 throttled (irrelevant)")
	rec := &Recorder{CFN: cfn, Store: NewStore(t.TempDir()), Identity: canonicalIdentity(), Logger: silentLogger()}

	hook := rec.Hook()
	hook(context.Background(), stackName, nil) // must not panic, must not return
}
