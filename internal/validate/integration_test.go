package validate

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
)

// goldenValidTemplate is a minimal-but-realistic CFN template used by the
// integration tests. The S3 bucket has no IAM dependencies, so a real
// ValidateTemplate would succeed; the tests inject a fake that returns the
// scripted outputs without going out to AWS.
const goldenValidTemplate = `AWSTemplateFormatVersion: '2010-09-09'
Description: Integration-test fixture for internal/validate
Parameters:
  VpcId:
    Type: AWS::EC2::VPC::Id
Resources:
  Bucket:
    Type: AWS::S3::Bucket
    Properties:
      BucketName: pw-validate-it
`

func TestPipeline_HappyPath_RaisesNoErrors(t *testing.T) {
	tpl := writeTemp(t, "tpl.yaml", goldenValidTemplate)
	api := &fakeCFN{out: &cloudformation.ValidateTemplateOutput{
		Capabilities: []cfntypes.Capability{cfntypes.CapabilityCapabilityIam},
		Parameters:   []cfntypes.TemplateParameter{{ParameterKey: aws.String("VpcId")}},
	}}

	findings, err := Default{}.Run(context.Background(), Input{TemplatePath: tpl, AWS: api})
	if err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if HasErrors(findings) {
		t.Fatalf("HasErrors = true; want false. findings=%+v", findings)
	}
	if api.calls != 1 {
		t.Errorf("ValidateTemplate calls = %d, want 1", api.calls)
	}
}

func TestPipeline_Stage1ShortCircuitsStage2(t *testing.T) {
	// Mixed tabs/spaces on the indented properties line.
	body := "Resources:\n" +
		"  Bucket:\n" +
		" \tType: AWS::S3::Bucket\n"
	tpl := writeTemp(t, "tpl.yaml", body)
	api := &fakeCFN{out: &cloudformation.ValidateTemplateOutput{}}

	findings, err := Default{}.Run(context.Background(), Input{TemplatePath: tpl, AWS: api})
	if err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if !HasErrors(findings) {
		t.Fatalf("HasErrors = false; want true. findings=%+v", findings)
	}
	if api.calls != 0 {
		t.Errorf("ValidateTemplate calls = %d, want 0 (Stage 1 errors must short-circuit Stage 2)", api.calls)
	}
}

func TestPipeline_FailureFromFindingsResolvesCatalogue(t *testing.T) {
	tpl := writeTemp(t, "tpl.yaml", goldenValidTemplate)
	api := &fakeCFN{err: &fakeAPIError{
		code:    "ValidationError",
		message: "Unknown resource type: 'AWS::Banana::Plant'",
	}}

	findings, err := Default{}.Run(context.Background(), Input{TemplatePath: tpl, AWS: api})
	if err != nil {
		t.Fatalf("Run err = %v", err)
	}
	failure := FailureFromFindings(findings, "demo-stack", map[string]any{"VpcId": "vpc-x"})
	if failure == nil {
		t.Fatal("FailureFromFindings returned nil; want a populated failure")
	}
	if failure.AppError == nil {
		t.Fatal("AppError = nil; want a populated catalogue-resolved error")
	}
	// The validate-template-unknown-resource-type catalogue entry has the
	// matching priority and regex, so MatchString should pick it up.
	if failure.AppError.MatchedID != "validate-template-unknown-resource-type" {
		t.Errorf("MatchedID = %q, want %q",
			failure.AppError.MatchedID, "validate-template-unknown-resource-type")
	}
	if !strings.Contains(failure.AppError.Cause, "AWS::Banana::Plant") {
		t.Errorf("Cause = %q, want it to carry the unknown resource type", failure.AppError.Cause)
	}
}

// TestPipeline_NoFailureWhenAllFindingsAreInfo guards that capabilities and
// parameter info findings do NOT trigger a ValidationFailure. PR-06 will
// consume those infos for form pre-fills, and a false-positive failure here
// would block all valid deploys that declare IAM capabilities.
func TestPipeline_NoFailureWhenAllFindingsAreInfo(t *testing.T) {
	tpl := writeTemp(t, "tpl.yaml", goldenValidTemplate)
	api := &fakeCFN{out: &cloudformation.ValidateTemplateOutput{
		Capabilities: []cfntypes.Capability{cfntypes.CapabilityCapabilityIam},
		Parameters:   []cfntypes.TemplateParameter{{ParameterKey: aws.String("VpcId")}},
	}}

	findings, err := Default{}.Run(context.Background(), Input{TemplatePath: tpl, AWS: api})
	if err != nil {
		t.Fatalf("Run err = %v", err)
	}
	if got := FailureFromFindings(findings, "demo", nil); got != nil {
		t.Errorf("FailureFromFindings returned %+v, want nil for info-only findings", got)
	}
}
