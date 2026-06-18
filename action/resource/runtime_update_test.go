package resource_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/bannaarr01/packwright/action/resource"
	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/internal/update"
)

// updateAPI is the minimal cfn.ChangeSetAPI stand-in for runtime update
// branch tests. Each operation tracks call count so the test can assert
// the coordinator drove the change-set lifecycle.
type updateAPI struct {
	describe func(call int) (*cloudformation.DescribeChangeSetOutput, error)

	createCalls   int32
	describeCalls int32
	executeCalls  int32
	deleteCalls   int32
}

func (a *updateAPI) CreateChangeSet(_ context.Context, _ *cloudformation.CreateChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.CreateChangeSetOutput, error) {
	atomic.AddInt32(&a.createCalls, 1)
	return &cloudformation.CreateChangeSetOutput{Id: aws.String("arn:cs:1"), StackId: aws.String("arn:stack:1")}, nil
}
func (a *updateAPI) DescribeChangeSet(_ context.Context, _ *cloudformation.DescribeChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.DescribeChangeSetOutput, error) {
	idx := int(atomic.AddInt32(&a.describeCalls, 1)) - 1
	if a.describe != nil {
		return a.describe(idx)
	}
	return &cloudformation.DescribeChangeSetOutput{Status: cfntypes.ChangeSetStatusCreateComplete}, nil
}
func (a *updateAPI) ExecuteChangeSet(_ context.Context, _ *cloudformation.ExecuteChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.ExecuteChangeSetOutput, error) {
	atomic.AddInt32(&a.executeCalls, 1)
	return &cloudformation.ExecuteChangeSetOutput{}, nil
}
func (a *updateAPI) DeleteChangeSet(_ context.Context, _ *cloudformation.DeleteChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.DeleteChangeSetOutput, error) {
	atomic.AddInt32(&a.deleteCalls, 1)
	return &cloudformation.DeleteChangeSetOutput{}, nil
}
func (a *updateAPI) ListChangeSets(_ context.Context, _ *cloudformation.ListChangeSetsInput, _ ...func(*cloudformation.Options)) (*cloudformation.ListChangeSetsOutput, error) {
	return &cloudformation.ListChangeSetsOutput{}, nil
}

// writeTestTemplate creates a minimal CFN template on disk so the update
// branch can read it via the manifest's TemplateSpec.Path.
func writeTestTemplate(t *testing.T, dir string) {
	t.Helper()
	body := []byte("AWSTemplateFormatVersion: '2010-09-09'\nResources:\n  Bucket:\n    Type: AWS::S3::Bucket\n")
	if err := os.WriteFile(filepath.Join(dir, "alb-template.yaml"), body, 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
}

func TestExecute_UpdateBranchSkipsScriptAndDrivesChangeSet(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, "testdata/fake-deploy.sh", filepath.Join(dir, "fake-deploy.sh"), 0o755)
	writeTestTemplate(t, dir)

	api := &updateAPI{describe: func(_ int) (*cloudformation.DescribeChangeSetOutput, error) {
		return &cloudformation.DescribeChangeSetOutput{
			Status: cfntypes.ChangeSetStatusCreateComplete,
			Changes: []cfntypes.Change{
				{Type: cfntypes.ChangeTypeResource, ResourceChange: &cfntypes.ResourceChange{
					Action: cfntypes.ChangeActionModify, LogicalResourceId: aws.String("Bucket"), ResourceType: aws.String("AWS::S3::Bucket"), Replacement: cfntypes.ReplacementFalse,
				}},
			},
		}, nil
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	res, err := resource.Execute(
		ctx,
		albManifest(t, dir),
		canonicalInputs(),
		awsx.NewForTest("test-profile", "eu-west-1"),
		resource.WithBaseDir(dir),
		resource.WithAZLookup(fakeAZLookup()),
		resource.WithValidators(false),
		resource.WithUpdate(resource.UpdateOptions{
			StackName:    "alb-stack-babe-main-prd",
			API:          api,
			PollInterval: time.Millisecond,
		}),
	)
	if err != nil {
		t.Fatalf("Execute (update branch): %v", err)
	}

	// Drain events; update path streamer is nil, so the channel should close immediately.
	for range res.Events {
	}
	if err := res.Wait(); err != nil {
		t.Errorf("Wait: %v", err)
	}

	if c := atomic.LoadInt32(&api.createCalls); c != 1 {
		t.Errorf("CreateChangeSet calls = %d, want 1", c)
	}
	if c := atomic.LoadInt32(&api.executeCalls); c != 1 {
		t.Errorf("ExecuteChangeSet calls = %d, want 1 (no replacements)", c)
	}
	if c := atomic.LoadInt32(&api.deleteCalls); c != 0 {
		t.Errorf("DeleteChangeSet calls = %d, want 0 on a successful update", c)
	}

	// parameters.json must NOT have been written — the update branch
	// bypasses the renderer entirely.
	if _, err := os.Stat(filepath.Join(dir, "parameters.json")); err == nil {
		t.Error("parameters.json was written in update branch — expected the renderer to be bypassed")
	}
}

func TestExecute_UpdateBranchDoesNotCallExecuteUntilConsent(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, "testdata/fake-deploy.sh", filepath.Join(dir, "fake-deploy.sh"), 0o755)
	writeTestTemplate(t, dir)

	api := &updateAPI{describe: func(_ int) (*cloudformation.DescribeChangeSetOutput, error) {
		return &cloudformation.DescribeChangeSetOutput{
			Status: cfntypes.ChangeSetStatusCreateComplete,
			Changes: []cfntypes.Change{
				{Type: cfntypes.ChangeTypeResource, ResourceChange: &cfntypes.ResourceChange{
					Action: cfntypes.ChangeActionModify, LogicalResourceId: aws.String("DBInstance"), ResourceType: aws.String("AWS::RDS::DBInstance"), Replacement: cfntypes.ReplacementTrue,
					Details: []cfntypes.ResourceChangeDetail{{
						Target: &cfntypes.ResourceTargetDefinition{Name: aws.String("DBInstanceClass"), RequiresRecreation: cfntypes.RequiresRecreationAlways},
					}},
				}},
			},
		}, nil
	}}

	consentCalls := int32(0)
	res, err := resource.Execute(
		context.Background(),
		albManifest(t, dir),
		canonicalInputs(),
		awsx.NewForTest("test-profile", "eu-west-1"),
		resource.WithBaseDir(dir),
		resource.WithAZLookup(fakeAZLookup()),
		resource.WithValidators(false),
		resource.WithUpdate(resource.UpdateOptions{
			StackName:    "alb-stack-babe-main-prd",
			API:          api,
			PollInterval: time.Millisecond,
			Consent: func(_ context.Context, p update.ReplacementPayload) update.ConsentDecision {
				atomic.AddInt32(&consentCalls, 1)
				if atomic.LoadInt32(&api.executeCalls) != 0 {
					t.Errorf("ExecuteChangeSet was called before consent gate ran")
				}
				if p.Count != 1 || p.Rows[0].LogicalID != "DBInstance" {
					t.Errorf("consent payload = %+v", p)
				}
				return update.ConsentDeny
			},
		}),
	)
	if err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	for range res.Events {
	}
	if atomic.LoadInt32(&consentCalls) != 1 {
		t.Errorf("consent calls = %d, want 1", consentCalls)
	}
	if atomic.LoadInt32(&api.executeCalls) != 0 {
		t.Errorf("ExecuteChangeSet calls = %d, want 0 (consent denied)", api.executeCalls)
	}
	if atomic.LoadInt32(&api.deleteCalls) != 1 {
		t.Errorf("DeleteChangeSet calls = %d, want 1 (cleanup after deny)", api.deleteCalls)
	}
}

func TestExecute_UpdateBranchNoChangesSkipsExecute(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, "testdata/fake-deploy.sh", filepath.Join(dir, "fake-deploy.sh"), 0o755)
	writeTestTemplate(t, dir)

	api := &updateAPI{describe: func(_ int) (*cloudformation.DescribeChangeSetOutput, error) {
		return &cloudformation.DescribeChangeSetOutput{
			Status:       cfntypes.ChangeSetStatusFailed,
			StatusReason: aws.String("No updates are to be performed."),
		}, nil
	}}

	res, err := resource.Execute(
		context.Background(),
		albManifest(t, dir),
		canonicalInputs(),
		awsx.NewForTest("test-profile", "eu-west-1"),
		resource.WithBaseDir(dir),
		resource.WithAZLookup(fakeAZLookup()),
		resource.WithValidators(false),
		resource.WithUpdate(resource.UpdateOptions{
			StackName:    "alb-stack-babe-main-prd",
			API:          api,
			PollInterval: time.Millisecond,
		}),
	)
	if err != nil {
		t.Fatalf("Execute err = %v (no-changes should NOT be an error)", err)
	}
	for range res.Events {
	}
	if atomic.LoadInt32(&api.executeCalls) != 0 {
		t.Errorf("ExecuteChangeSet calls = %d, want 0 on no-changes", api.executeCalls)
	}
	if atomic.LoadInt32(&api.deleteCalls) != 1 {
		t.Errorf("DeleteChangeSet calls = %d, want 1 (empty change set torn down)", api.deleteCalls)
	}
}
