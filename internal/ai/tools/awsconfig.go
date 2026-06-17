package tools

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"

	"github.com/bannaarr01/packwright/awsx"
)

// LoadAWSConfig builds an aws.Config bound to the same (profile, region)
// the supplied awsx.Client uses. Tools that need an SDK service client
// awsx does not yet wrap call this helper, then build the service client
// from the returned config — mirroring how awsx.New itself initialises
// its EC2 / ELBv2 / ACM clients.
//
// LoadAWSConfig errors are wrapped into a *ToolError with
// ErrCodeMisconfigured so the LLM-facing layer can distinguish "no AWS
// credentials available" from a per-API failure.
func LoadAWSConfig(ctx context.Context, toolName string, c *awsx.Client) (aws.Config, error) {
	if c == nil {
		return aws.Config{}, &ToolError{
			Code: ErrCodeMisconfigured, Tool: toolName,
			Message: errNoAWSClient.Error(),
			Cause:   errNoAWSClient,
		}
	}
	var opts []func(*config.LoadOptions) error
	if p := c.Profile(); p != "" {
		opts = append(opts, config.WithSharedConfigProfile(p))
	}
	if r := c.Region(); r != "" {
		opts = append(opts, config.WithRegion(r))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, &ToolError{
			Code: ErrCodeMisconfigured, Tool: toolName,
			Message: fmt.Sprintf("loading AWS config (profile=%q region=%q): %v",
				c.Profile(), c.Region(), err),
			Cause: err,
		}
	}
	return cfg, nil
}
