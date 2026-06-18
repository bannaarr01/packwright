package validate

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
)

// CloudFormationAPI is the narrow CloudFormation surface the validator
// depends on. The SDK *cloudformation.Client and awsx.CloudFormation()'s
// return value both satisfy it structurally; tests inject a fake.
//
// The interface is intentionally tighter than awsx.CloudFormationAPI:
// keeping it local to internal/validate lets tests stand up a fake with one
// method, and decouples the package from any future awsx surface growth
// (PR-06 will add CreateChangeSet etc. through awsx; the validator does
// not need them).
type CloudFormationAPI interface {
	ValidateTemplate(ctx context.Context, in *cloudformation.ValidateTemplateInput, opts ...func(*cloudformation.Options)) (*cloudformation.ValidateTemplateOutput, error)
}
