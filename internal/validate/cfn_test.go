package validate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	smithy "github.com/aws/smithy-go"
)

// fakeCFN is the test seam for CloudFormationAPI. validate ID is matched
// against the request to drive different responses without re-implementing
// pattern matching in each test.
type fakeCFN struct {
	out *cloudformation.ValidateTemplateOutput
	err error

	// calls counts ValidateTemplate invocations for assertions about
	// short-circuit paths (the >51,200-byte case must NOT call the API).
	calls int
	// lastBody captures the TemplateBody so tests can assert the engine
	// streamed the file through correctly.
	lastBody string
}

func (f *fakeCFN) ValidateTemplate(ctx context.Context, in *cloudformation.ValidateTemplateInput, _ ...func(*cloudformation.Options)) (*cloudformation.ValidateTemplateOutput, error) {
	f.calls++
	if in != nil && in.TemplateBody != nil {
		f.lastBody = *in.TemplateBody
	}
	return f.out, f.err
}

// fakeAPIError is the smithy.APIError shape used by aws-sdk-go-v2 for service
// errors. We need a real implementation here because the SDK's concrete types
// (smithy.GenericAPIError) work but are easier to assemble inline.
type fakeAPIError struct {
	code    string
	message string
}

func (e *fakeAPIError) Error() string                 { return e.code + ": " + e.message }
func (e *fakeAPIError) ErrorCode() string             { return e.code }
func (e *fakeAPIError) ErrorMessage() string          { return e.message }
func (e *fakeAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

func TestCFNStage_SurfacesCapabilitiesAndParametersAsInfo(t *testing.T) {
	path := writeTemp(t, "tpl.yaml", "Resources: {}\n")
	api := &fakeCFN{out: &cloudformation.ValidateTemplateOutput{
		Capabilities: []cfntypes.Capability{cfntypes.CapabilityCapabilityIam, cfntypes.CapabilityCapabilityNamedIam},
		Parameters: []cfntypes.TemplateParameter{
			{ParameterKey: aws.String("VpcId")},
			{ParameterKey: aws.String("Environment")},
		},
	}}

	findings, err := runCFNStage(context.Background(), Input{TemplatePath: path, AWS: api})
	if err != nil {
		t.Fatalf("runCFNStage err = %v", err)
	}

	var caps, params int
	for _, f := range findings {
		if f.Severity != SeverityInfo {
			t.Errorf("non-info finding: %+v", f)
		}
		switch {
		case strings.Contains(f.Reason, "capability"):
			caps++
		case strings.Contains(f.Reason, "parameter"):
			params++
		}
	}
	if caps != 2 {
		t.Errorf("capability findings = %d, want 2 (PR-06 depends on this surface)", caps)
	}
	if params != 2 {
		t.Errorf("parameter findings = %d, want 2", params)
	}
	if api.calls != 1 {
		t.Errorf("ValidateTemplate calls = %d, want 1", api.calls)
	}
}

func TestCFNStage_BananaPlantResourceFailsWithCatalogueMappableReason(t *testing.T) {
	path := writeTemp(t, "tpl.yaml", `Resources:
  Banana:
    Type: AWS::Banana::Plant
`)
	// Mirror the message CloudFormation returns for an unknown resource
	// type — the catalogue regex in validate-template-unknown-resource-type.yaml
	// matches this exact shape.
	api := &fakeCFN{err: &fakeAPIError{
		code:    "ValidationError",
		message: "Unknown resource type: 'AWS::Banana::Plant'",
	}}

	findings, err := runCFNStage(context.Background(), Input{TemplatePath: path, AWS: api})
	if err != nil {
		t.Fatalf("runCFNStage err = %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one error finding", findings)
	}
	got := findings[0]
	if got.Severity != SeverityError {
		t.Errorf("Severity = %q, want %q", got.Severity, SeverityError)
	}
	if !strings.Contains(got.Reason, "AWS::Banana::Plant") {
		t.Errorf("Reason = %q, want it to carry the unknown type", got.Reason)
	}
	if !strings.Contains(strings.ToLower(got.Reason), "unknown resource type") {
		t.Errorf("Reason = %q, want it to keep the catalogue-mappable prefix", got.Reason)
	}
}

func TestCFNStage_TooLargeEmitsInfoAndSkipsAPI(t *testing.T) {
	// 51,201 bytes — one over the inline limit.
	big := strings.Repeat("a", inlineTemplateLimit+1)
	path := writeTemp(t, "big.yaml", big)
	api := &fakeCFN{out: &cloudformation.ValidateTemplateOutput{}}

	findings, err := runCFNStage(context.Background(), Input{TemplatePath: path, AWS: api})
	if err != nil {
		t.Fatalf("runCFNStage err = %v", err)
	}
	if len(findings) != 1 || findings[0].Severity != SeverityInfo {
		t.Fatalf("findings = %+v, want one info finding", findings)
	}
	if api.calls != 0 {
		t.Errorf("ValidateTemplate calls = %d, want 0 (large templates must not hit AWS)", api.calls)
	}
	if !strings.Contains(findings[0].Reason, "limit 51200") {
		t.Errorf("Reason = %q, want it to mention the 51200 limit", findings[0].Reason)
	}
}

func TestCFNStage_NilAPIIsNoOp(t *testing.T) {
	path := writeTemp(t, "tpl.yaml", "Resources: {}\n")
	findings, err := runCFNStage(context.Background(), Input{TemplatePath: path, AWS: nil})
	if err != nil {
		t.Fatalf("runCFNStage err = %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want empty when no AWS client is wired", findings)
	}
}

func TestCFNStage_NonAPIErrorStillRendersAsErrorFinding(t *testing.T) {
	path := writeTemp(t, "tpl.yaml", "Resources: {}\n")
	api := &fakeCFN{err: errors.New("network unreachable")}

	findings, err := runCFNStage(context.Background(), Input{TemplatePath: path, AWS: api})
	if err != nil {
		t.Fatalf("runCFNStage err = %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want one finding", findings)
	}
	if findings[0].Severity != SeverityError {
		t.Errorf("Severity = %q, want %q", findings[0].Severity, SeverityError)
	}
	if !strings.Contains(findings[0].Reason, "network unreachable") {
		t.Errorf("Reason = %q, want it to carry the raw error", findings[0].Reason)
	}
}
