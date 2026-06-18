package record

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
)

// cloudFormationAPI is the narrow subset of the AWS CloudFormation client the
// recorder calls during a harvest. The real SDK client
// (*cloudformation.Client) satisfies it structurally; tests inject their own
// implementation so no harvest test ever reaches the network.
//
// Mirrors the cfnAPI interface in internal/ai/tools/read/cfn.go on purpose —
// the read tools and the recorder do the same kind of read-only round-trip,
// so the two interfaces describe overlapping but independent dependencies.
type cloudFormationAPI interface {
	DescribeStacks(ctx context.Context, in *cloudformation.DescribeStacksInput, opts ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error)
	DescribeStackResources(ctx context.Context, in *cloudformation.DescribeStackResourcesInput, opts ...func(*cloudformation.Options)) (*cloudformation.DescribeStackResourcesOutput, error)
}
