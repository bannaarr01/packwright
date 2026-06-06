package awsx

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/acm"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
)

// Client wraps the AWS service clients Packwright uses for live pickers and
// the disk cache that fronts them. Construct one with New.
//
// Client is bound to a single (profile, region) pair; callers create a new
// Client when the user switches profile or region. New deliberately does not
// call STS — credential verification is implemented separately in MVP-2 PR-07.
type Client struct {
	profile string
	region  string
	log     *slog.Logger
	cache   *Cache

	ec2API   ec2API
	elbv2API elbv2API
	acmAPI   acmAPI
}

// ec2API is the minimum EC2 surface awsx depends on. *ec2.Client satisfies it
// structurally; tests inject their own implementation to count calls and
// drive paginated responses.
type ec2API interface {
	DescribeVpcs(ctx context.Context, in *ec2.DescribeVpcsInput, opts ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
	DescribeSubnets(ctx context.Context, in *ec2.DescribeSubnetsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	DescribeSecurityGroups(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
}

// elbv2API is the minimum ELBv2 surface awsx depends on. *elasticloadbalancingv2.Client
// satisfies it structurally.
type elbv2API interface {
	DescribeLoadBalancers(ctx context.Context, in *elasticloadbalancingv2.DescribeLoadBalancersInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error)
}

// acmAPI is the minimum ACM surface awsx depends on. *acm.Client satisfies it
// structurally.
type acmAPI interface {
	ListCertificates(ctx context.Context, in *acm.ListCertificatesInput, opts ...func(*acm.Options)) (*acm.ListCertificatesOutput, error)
}

// New loads the AWS shared config for the named profile and region and returns
// a Client wired up with EC2, ELBv2, and ACM service clients plus a disk cache
// rooted at cacheHome (typically the Packwright state directory).
//
// An empty profile or region falls through to the SDK's default chain (env vars
// then the profile's own region/the default profile). A nil log is replaced by
// slog.Default. New does not call STS; that is MVP-2 PR-07's job.
func New(ctx context.Context, profile, region, cacheHome string, log *slog.Logger) (*Client, error) {
	if log == nil {
		log = slog.Default()
	}

	var opts []func(*config.LoadOptions) error
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("awsx: loading AWS config (profile=%q region=%q): %w", profile, region, err)
	}

	cache, err := NewCache(cacheHome, DefaultTTL, log)
	if err != nil {
		return nil, err
	}

	return &Client{
		profile:  profile,
		region:   cfg.Region,
		log:      log,
		cache:    cache,
		ec2API:   ec2.NewFromConfig(cfg),
		elbv2API: elasticloadbalancingv2.NewFromConfig(cfg),
		acmAPI:   acm.NewFromConfig(cfg),
	}, nil
}

// Profile reports the AWS profile this client was constructed for. It is the
// raw string passed to New; an empty value means "SDK default chain".
func (c *Client) Profile() string { return c.profile }

// Region reports the AWS region this client is bound to, as resolved by the
// SDK (so the profile's region wins when New was called with an empty region).
func (c *Client) Region() string { return c.region }

// Cache returns the underlying disk cache. Exposed for tests and callers that
// want to introspect or clear cache state; production code uses the picker
// methods on Client.
func (c *Client) Cache() *Cache { return c.cache }
