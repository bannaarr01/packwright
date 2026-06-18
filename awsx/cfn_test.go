package awsx

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
)

// stubCFN is the test seam for CloudFormationAPI used by withCFNFactory.
type stubCFN struct {
	called int
}

func (s *stubCFN) ValidateTemplate(_ context.Context, _ *cloudformation.ValidateTemplateInput, _ ...func(*cloudformation.Options)) (*cloudformation.ValidateTemplateOutput, error) {
	s.called++
	return &cloudformation.ValidateTemplateOutput{}, nil
}

// withCFNFactory replaces the package-level cfnFactory for the duration of a
// test, restoring the previous value on cleanup. Mirrors the helper pattern
// used for the STS factory.
func withCFNFactory(t *testing.T, fn func(*Client) CloudFormationAPI) {
	t.Helper()
	prev := cfnFactory
	cfnFactory = fn
	t.Cleanup(func() { cfnFactory = prev })
}

func TestCloudFormation_UsesFactoryAndReturnsAPI(t *testing.T) {
	stub := &stubCFN{}
	withCFNFactory(t, func(*Client) CloudFormationAPI { return stub })

	c := NewForTest("p", "eu-west-1")
	api := c.CloudFormation()
	if api == nil {
		t.Fatal("CloudFormation() returned nil")
	}
	if _, err := api.ValidateTemplate(context.Background(), &cloudformation.ValidateTemplateInput{}); err != nil {
		t.Fatalf("ValidateTemplate: %v", err)
	}
	if stub.called != 1 {
		t.Errorf("stub called %d times, want 1", stub.called)
	}
}
