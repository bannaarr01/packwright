package validate

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	smithy "github.com/aws/smithy-go"
)

// inlineTemplateLimit is CloudFormation's documented hard cap on TemplateBody
// length (51,200 bytes). Above the cap the user must upload to S3 and call
// ValidateTemplate with TemplateURL; ADR-0050 leaves that path opt-in for a
// future MVP. PR-03 emits an info finding pointing at the future setting and
// exits the stage cleanly.
const inlineTemplateLimit = 51_200

// runCFNStage reads the template body and calls ValidateTemplate. The
// returned findings are:
//
//   - one error-severity finding when ValidateTemplate fails (the raw AWS
//     message flows through the error catalogue at the call site);
//   - one info-severity finding per declared Capability so PR-06 can pre-fill
//     capability confirmations in the update form;
//   - one info-severity finding per declared Parameter so PR-06 can render
//     missing-param hints;
//   - one info-severity finding when the template is too large for inline
//     validation (Stage 2 exits cleanly — the deploy is not blocked).
//
// A nil AWS client is treated as "validators not wired"; the stage returns
// no findings and no error so callers that opt out of Stage 2 (e.g. unit
// tests of resource.Execute that don't care about CFN validation) don't have
// to special-case the nil.
func runCFNStage(ctx context.Context, in Input) ([]Finding, error) {
	if in.AWS == nil {
		return nil, nil
	}
	if in.TemplatePath == "" {
		return nil, errors.New("validate: template path is empty")
	}

	body, err := os.ReadFile(in.TemplatePath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", in.TemplatePath, err)
	}

	if len(body) > inlineTemplateLimit {
		return []Finding{{
			Stage:    StageCFN,
			Severity: SeverityInfo,
			Path:     in.TemplatePath,
			Reason: fmt.Sprintf(
				"template is %d bytes (limit %d for inline validation); large-template S3 upload is a future opt-in setting, skipping ValidateTemplate",
				len(body), inlineTemplateLimit,
			),
		}}, nil
	}

	out, err := in.AWS.ValidateTemplate(ctx, &cloudformation.ValidateTemplateInput{
		TemplateBody: aws.String(string(body)),
	})
	if err != nil {
		return []Finding{{
			Stage:    StageCFN,
			Severity: SeverityError,
			Path:     in.TemplatePath,
			Reason:   summariseAWSError(err),
		}}, nil
	}

	return capabilityAndParameterFindings(in.TemplatePath, out), nil
}

// capabilityAndParameterFindings turns the response from a successful
// ValidateTemplate call into the info-severity findings PR-06 will consume
// to pre-fill capability prompts and missing-parameter hints. The output is
// stable: capabilities first (sorted by their position in the API response,
// which AWS returns deterministically), then parameters (same).
func capabilityAndParameterFindings(path string, out *cloudformation.ValidateTemplateOutput) []Finding {
	if out == nil {
		return nil
	}
	findings := make([]Finding, 0, len(out.Capabilities)+len(out.Parameters))
	for _, cap := range out.Capabilities {
		findings = append(findings, Finding{
			Stage:    StageCFN,
			Severity: SeverityInfo,
			Path:     path,
			Reason:   "template requires capability " + string(cap),
		})
	}
	for _, p := range out.Parameters {
		findings = append(findings, Finding{
			Stage:    StageCFN,
			Severity: SeverityInfo,
			Path:     path,
			Reason:   "template declares parameter " + aws.ToString(p.ParameterKey),
		})
	}
	return findings
}

// summariseAWSError returns the AWS error's ErrorMessage when the underlying
// error is a smithy.APIError (which the SDK uses for service-side failures),
// or the raw err.Error() otherwise. The error catalogue's regex match runs
// against this string, so the cleaner it is, the more reliably the catalogue
// fires.
func summariseAWSError(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorMessage()
	}
	return err.Error()
}
