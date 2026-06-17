package read

import (
	"context"

	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// stsVerify is the helper Verify call. var so tests can swap.
var stsVerify = awsx.Verify

// getCallerIdentity wraps sts:GetCallerIdentity via awsx.Verify so the answer
// includes the resolved profile and region — the AI uses this when it needs
// to confirm which account it's reading from.
type getCallerIdentity struct{}

// Name reports the catalogue name.
func (getCallerIdentity) Name() string { return "sts/get-caller-identity" }

// Permission returns the const PermissionRead.
func (getCallerIdentity) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t getCallerIdentity) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Resolve the active AWS profile/region and return account, ARN, and user id.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

// Execute calls awsx.Verify.
func (t getCallerIdentity) Execute(ctx context.Context, _ map[string]any) (any, error) {
	c, err := tools.RequireAWSClient(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	id, err := stsVerify(ctx, c)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"profile": id.Profile,
		"region":  id.Region,
		"account": id.Account,
		"arn":     id.Arn,
		"user_id": id.UserId,
	}, nil
}

func init() {
	tools.MustRegister(tools.Default, getCallerIdentity{})
}
