package awsx

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
)

// CloudFormationAPI is the narrow CloudFormation surface awsx exposes to its
// callers. *cloudformation.Client satisfies it structurally; tests inject a
// fake. The interface lives here (not in the consumer package) so any future
// awsx consumer that needs the same operations can depend on this single,
// stable seam rather than redefining its own.
//
// Today the only consumer is internal/validate (Stage 2 — ValidateTemplate);
// PR-06's update flow will add CreateChangeSet etc. through this same accessor.
type CloudFormationAPI interface {
	ValidateTemplate(ctx context.Context, in *cloudformation.ValidateTemplateInput, opts ...func(*cloudformation.Options)) (*cloudformation.ValidateTemplateOutput, error)
}

// cfnFactory builds a CloudFormation API for the awsx Client's config. It is
// a package-level var so tests can substitute a fake without going through
// the SDK and the live AWS network stack.
var cfnFactory = func(c *Client) CloudFormationAPI {
	return cloudformation.NewFromConfig(c.cfg)
}

// CloudFormation returns a CloudFormation client built from the same
// shared-config the awsx Client was constructed against, so it inherits the
// Client's profile + region without a second config load.
//
// The returned value satisfies CloudFormationAPI; callers that only need a
// subset of operations (e.g. just ValidateTemplate) can define their own
// narrower interface and the SDK client will still satisfy it structurally.
//
// Calling on a nil *Client is a programmer error and triggers the standard
// nil-pointer-dereference panic; the call sites in this repo all hold a
// non-nil Client by construction.
func (c *Client) CloudFormation() CloudFormationAPI {
	return cfnFactory(c)
}
