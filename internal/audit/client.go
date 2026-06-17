package audit

import (
	"context"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/bannaarr01/packwright/awsx"
)

// EC2API is the EC2 surface the audit scanners depend on. The union
// covers every paginated Describe* the EC2 scanners invoke; this lets
// one fake implementation satisfy every EC2 scanner's narrow paginator
// requirement (the SDK paginators ask for one method each, all of which
// EC2API names).
//
// *ec2.Client satisfies EC2API structurally.
type EC2API interface {
	DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, opts ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	DescribeVolumes(ctx context.Context, in *ec2.DescribeVolumesInput, opts ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error)
	DescribeSnapshots(ctx context.Context, in *ec2.DescribeSnapshotsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSnapshotsOutput, error)
	DescribeAddresses(ctx context.Context, in *ec2.DescribeAddressesInput, opts ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error)
	DescribeNatGateways(ctx context.Context, in *ec2.DescribeNatGatewaysInput, opts ...func(*ec2.Options)) (*ec2.DescribeNatGatewaysOutput, error)
}

// ELBv2API is the ELBv2 surface the audit scanners depend on.
// *elasticloadbalancingv2.Client satisfies it structurally.
type ELBv2API interface {
	DescribeLoadBalancers(ctx context.Context, in *elasticloadbalancingv2.DescribeLoadBalancersInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error)
	DescribeTargetGroups(ctx context.Context, in *elasticloadbalancingv2.DescribeTargetGroupsInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error)
}

// RDSAPI is the RDS surface the audit scanners depend on. *rds.Client
// satisfies it structurally.
type RDSAPI interface {
	DescribeDBInstances(ctx context.Context, in *rds.DescribeDBInstancesInput, opts ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
	DescribeDBSnapshots(ctx context.Context, in *rds.DescribeDBSnapshotsInput, opts ...func(*rds.Options)) (*rds.DescribeDBSnapshotsOutput, error)
}

// EFSAPI is the EFS surface the audit scanners depend on. *efs.Client
// satisfies it structurally.
type EFSAPI interface {
	DescribeFileSystems(ctx context.Context, in *efs.DescribeFileSystemsInput, opts ...func(*efs.Options)) (*efs.DescribeFileSystemsOutput, error)
}

// LogsAPI is the CloudWatch Logs surface the audit scanners depend on.
// *cloudwatchlogs.Client satisfies it structurally.
type LogsAPI interface {
	DescribeLogGroups(ctx context.Context, in *cloudwatchlogs.DescribeLogGroupsInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error)
}

// ECRAPI is the ECR surface the audit scanners depend on. *ecr.Client
// satisfies it structurally.
type ECRAPI interface {
	DescribeRepositories(ctx context.Context, in *ecr.DescribeRepositoriesInput, opts ...func(*ecr.Options)) (*ecr.DescribeRepositoriesOutput, error)
	DescribeImages(ctx context.Context, in *ecr.DescribeImagesInput, opts ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error)
}

// S3API is the S3 surface the audit scanners depend on. *s3.Client
// satisfies it structurally.
type S3API interface {
	ListBuckets(ctx context.Context, in *s3.ListBucketsInput, opts ...func(*s3.Options)) (*s3.ListBucketsOutput, error)
	GetBucketTagging(ctx context.Context, in *s3.GetBucketTaggingInput, opts ...func(*s3.Options)) (*s3.GetBucketTaggingOutput, error)
	GetBucketLocation(ctx context.Context, in *s3.GetBucketLocationInput, opts ...func(*s3.Options)) (*s3.GetBucketLocationOutput, error)
}

// CodePipelineAPI is the CodePipeline surface the audit scanners depend
// on. *codepipeline.Client satisfies it structurally.
type CodePipelineAPI interface {
	ListPipelines(ctx context.Context, in *codepipeline.ListPipelinesInput, opts ...func(*codepipeline.Options)) (*codepipeline.ListPipelinesOutput, error)
	GetPipelineState(ctx context.Context, in *codepipeline.GetPipelineStateInput, opts ...func(*codepipeline.Options)) (*codepipeline.GetPipelineStateOutput, error)
}

// Client is the per-audit context every Scanner.Scan receives. It bundles
// the AWS service clients, the resolved (profile, region, account)
// triple every Resource carries, the per-service throttles, and a
// logger.
//
// Production code constructs a Client with [NewFromAWSX]: the typed
// service clients are built once from the underlying aws.Config and
// every scanner shares them. Tests construct a Client with
// [NewForTest] and per-service options so each scanner can be exercised
// against a fake.
type Client struct {
	profile string
	region  string
	account string
	log     *slog.Logger

	ec2API          EC2API
	elbv2API        ELBv2API
	rdsAPI          RDSAPI
	efsAPI          EFSAPI
	logsAPI         LogsAPI
	ecrAPI          ECRAPI
	s3API           S3API
	codepipelineAPI CodePipelineAPI

	throttles map[string]*Bucket
}

// Profile reports the AWS profile this audit is bound to. Echoed into
// every Resource so the UI can label rows by source.
func (c *Client) Profile() string { return c.profile }

// Region reports the AWS region this audit is bound to.
func (c *Client) Region() string { return c.region }

// Account reports the 12-digit AWS account ID this audit is bound to,
// as resolved via STS by the caller before constructing the Client.
func (c *Client) Account() string { return c.account }

// Logger returns the slog.Logger scanners write structured warnings to.
// Never nil — NewForTest substitutes slog.Default when none is passed.
func (c *Client) Logger() *slog.Logger { return c.log }

// EC2 returns the EC2 service client. Nil only when NewForTest was
// called without WithEC2 — production paths always wire it.
func (c *Client) EC2() EC2API { return c.ec2API }

// ELBv2 returns the ELBv2 service client.
func (c *Client) ELBv2() ELBv2API { return c.elbv2API }

// RDS returns the RDS service client.
func (c *Client) RDS() RDSAPI { return c.rdsAPI }

// EFS returns the EFS service client.
func (c *Client) EFS() EFSAPI { return c.efsAPI }

// Logs returns the CloudWatch Logs service client.
func (c *Client) Logs() LogsAPI { return c.logsAPI }

// ECR returns the ECR service client.
func (c *Client) ECR() ECRAPI { return c.ecrAPI }

// S3 returns the S3 service client.
func (c *Client) S3() S3API { return c.s3API }

// CodePipeline returns the CodePipeline service client.
func (c *Client) CodePipeline() CodePipelineAPI { return c.codepipelineAPI }

// Throttle returns the token bucket the scanner should pace its requests
// through, keyed by service name (e.g. "ec2", "rds"). A nil return means
// no throttle is configured for that service — scanners should treat
// nil and a rate-zero bucket the same way (both effectively no-op).
func (c *Client) Throttle(service string) *Bucket {
	if c.throttles == nil {
		return nil
	}
	return c.throttles[service]
}

// NewFromAWSX builds a production audit Client from an awsx.Client and
// the account ID resolved via [awsx.Verify]. Every typed service client
// is constructed from the underlying aws.Config so credentials, region,
// and profile resolution stay consistent with the rest of the app.
//
// log is optional; nil falls through to slog.Default. The returned
// Client carries the default per-service throttles (see DefaultRate).
func NewFromAWSX(client *awsx.Client, account string, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	cfg := client.Config()
	return &Client{
		profile:         client.Profile(),
		region:          client.Region(),
		account:         account,
		log:             log,
		ec2API:          ec2.NewFromConfig(cfg),
		elbv2API:        elasticloadbalancingv2.NewFromConfig(cfg),
		rdsAPI:          rds.NewFromConfig(cfg),
		efsAPI:          efs.NewFromConfig(cfg),
		logsAPI:         cloudwatchlogs.NewFromConfig(cfg),
		ecrAPI:          ecr.NewFromConfig(cfg),
		s3API:           s3.NewFromConfig(cfg),
		codepipelineAPI: codepipeline.NewFromConfig(cfg),
		throttles:       defaultThrottles(),
	}
}

// defaultThrottles returns a token bucket per AWS service the scanners
// touch, all sharing the ADR-0040 default rate. The CLI surface in PR-02
// will let the user override these via flags.
func defaultThrottles() map[string]*Bucket {
	services := []string{"ec2", "elbv2", "rds", "efs", "logs", "ecr", "s3", "codepipeline"}
	out := make(map[string]*Bucket, len(services))
	for _, s := range services {
		out[s] = NewBucket(DefaultRate, DefaultBurst)
	}
	return out
}

// ClientOption configures a test Client built by [NewForTest]. Tests
// pass one option per service to inject the corresponding fake.
type ClientOption func(*Client)

// WithProfile sets the Client's profile string.
func WithProfile(p string) ClientOption { return func(c *Client) { c.profile = p } }

// WithRegion sets the Client's region string.
func WithRegion(r string) ClientOption { return func(c *Client) { c.region = r } }

// WithAccount sets the Client's account string.
func WithAccount(a string) ClientOption { return func(c *Client) { c.account = a } }

// WithLogger replaces the Client's logger; nil restores slog.Default.
func WithLogger(l *slog.Logger) ClientOption {
	return func(c *Client) {
		if l == nil {
			l = slog.Default()
		}
		c.log = l
	}
}

// WithEC2 injects a fake EC2 API.
func WithEC2(api EC2API) ClientOption { return func(c *Client) { c.ec2API = api } }

// WithELBv2 injects a fake ELBv2 API.
func WithELBv2(api ELBv2API) ClientOption { return func(c *Client) { c.elbv2API = api } }

// WithRDS injects a fake RDS API.
func WithRDS(api RDSAPI) ClientOption { return func(c *Client) { c.rdsAPI = api } }

// WithEFS injects a fake EFS API.
func WithEFS(api EFSAPI) ClientOption { return func(c *Client) { c.efsAPI = api } }

// WithLogs injects a fake CloudWatch Logs API.
func WithLogs(api LogsAPI) ClientOption { return func(c *Client) { c.logsAPI = api } }

// WithECR injects a fake ECR API.
func WithECR(api ECRAPI) ClientOption { return func(c *Client) { c.ecrAPI = api } }

// WithS3 injects a fake S3 API.
func WithS3(api S3API) ClientOption { return func(c *Client) { c.s3API = api } }

// WithCodePipeline injects a fake CodePipeline API.
func WithCodePipeline(api CodePipelineAPI) ClientOption {
	return func(c *Client) { c.codepipelineAPI = api }
}

// WithThrottle installs a Bucket for the named service. A nil bucket
// effectively disables throttling for that service.
func WithThrottle(service string, b *Bucket) ClientOption {
	return func(c *Client) {
		if c.throttles == nil {
			c.throttles = map[string]*Bucket{}
		}
		c.throttles[service] = b
	}
}

// NewForTest constructs a Client with no AWS service clients wired by
// default — every call to one of the API accessors returns nil until a
// With* option installs a fake. Throttles default to nil so tests do not
// sleep; pass WithThrottle to exercise the bucket.
func NewForTest(opts ...ClientOption) *Client {
	c := &Client{log: slog.Default()}
	for _, opt := range opts {
		opt(c)
	}
	return c
}
