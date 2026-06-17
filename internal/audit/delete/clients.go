package delete

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/rds"

	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// EC2API is the subset of EC2 operations this package uses. The
// real *ec2.Client satisfies it structurally; tests substitute a
// hand-rolled fake.
type EC2API interface {
	DeleteVolume(ctx context.Context, in *ec2.DeleteVolumeInput, opts ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error)
	DeleteSnapshot(ctx context.Context, in *ec2.DeleteSnapshotInput, opts ...func(*ec2.Options)) (*ec2.DeleteSnapshotOutput, error)
	ReleaseAddress(ctx context.Context, in *ec2.ReleaseAddressInput, opts ...func(*ec2.Options)) (*ec2.ReleaseAddressOutput, error)
	DeleteNatGateway(ctx context.Context, in *ec2.DeleteNatGatewayInput, opts ...func(*ec2.Options)) (*ec2.DeleteNatGatewayOutput, error)

	DescribeVolumes(ctx context.Context, in *ec2.DescribeVolumesInput, opts ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error)
	DescribeSnapshots(ctx context.Context, in *ec2.DescribeSnapshotsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSnapshotsOutput, error)
	DescribeImages(ctx context.Context, in *ec2.DescribeImagesInput, opts ...func(*ec2.Options)) (*ec2.DescribeImagesOutput, error)
	DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, opts ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	DescribeAddresses(ctx context.Context, in *ec2.DescribeAddressesInput, opts ...func(*ec2.Options)) (*ec2.DescribeAddressesOutput, error)
	DescribeNatGateways(ctx context.Context, in *ec2.DescribeNatGatewaysInput, opts ...func(*ec2.Options)) (*ec2.DescribeNatGatewaysOutput, error)
	DescribeRouteTables(ctx context.Context, in *ec2.DescribeRouteTablesInput, opts ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error)
}

// ELBv2API is the subset of ELBv2 operations this package uses.
type ELBv2API interface {
	DeleteTargetGroup(ctx context.Context, in *elasticloadbalancingv2.DeleteTargetGroupInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DeleteTargetGroupOutput, error)
	DescribeTargetGroups(ctx context.Context, in *elasticloadbalancingv2.DescribeTargetGroupsInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error)
	DescribeListeners(ctx context.Context, in *elasticloadbalancingv2.DescribeListenersInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeListenersOutput, error)
	DescribeRules(ctx context.Context, in *elasticloadbalancingv2.DescribeRulesInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeRulesOutput, error)
}

// LogsAPI is the subset of CloudWatch Logs operations this package uses.
type LogsAPI interface {
	DeleteLogGroup(ctx context.Context, in *cloudwatchlogs.DeleteLogGroupInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DeleteLogGroupOutput, error)
	DescribeLogGroups(ctx context.Context, in *cloudwatchlogs.DescribeLogGroupsInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.DescribeLogGroupsOutput, error)
}

// RDSAPI is the subset of RDS operations this package uses.
type RDSAPI interface {
	DeleteDBSnapshot(ctx context.Context, in *rds.DeleteDBSnapshotInput, opts ...func(*rds.Options)) (*rds.DeleteDBSnapshotOutput, error)
	DescribeDBSnapshots(ctx context.Context, in *rds.DescribeDBSnapshotsInput, opts ...func(*rds.Options)) (*rds.DescribeDBSnapshotsOutput, error)
	DescribeDBInstances(ctx context.Context, in *rds.DescribeDBInstancesInput, opts ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
}

// ECRAPI is the subset of ECR operations this package uses.
type ECRAPI interface {
	BatchDeleteImage(ctx context.Context, in *ecr.BatchDeleteImageInput, opts ...func(*ecr.Options)) (*ecr.BatchDeleteImageOutput, error)
	DescribeImages(ctx context.Context, in *ecr.DescribeImagesInput, opts ...func(*ecr.Options)) (*ecr.DescribeImagesOutput, error)
}

// Clients bundles every AWS service client the executor and the
// dependency probe rely on. Tests construct one with hand-rolled
// fakes; production code uses NewClients to build one from the
// session's awsx.Client.
//
// Any field may be nil — the executor returns an
// ErrServiceUnavailable when a row's Kind references a missing
// service. This lets callers opt out of (say) ECR if they have not
// configured those credentials.
type Clients struct {
	EC2   EC2API
	ELBv2 ELBv2API
	Logs  LogsAPI
	RDS   RDSAPI
	ECR   ECRAPI
}

// NewClients builds a Clients bundle from awsxClient. Every service
// client is constructed from the same aws.Config so they all share
// the awsxClient's profile + region selection.
//
// The toolName parameter is threaded into the error returned by
// tools.LoadAWSConfig so a misconfigured caller sees a clear
// origin in the *tools.ToolError.
func NewClients(ctx context.Context, toolName string, awsxClient *awsx.Client) (*Clients, error) {
	cfg, err := tools.LoadAWSConfig(ctx, toolName, awsxClient)
	if err != nil {
		return nil, err
	}
	return &Clients{
		EC2:   ec2.NewFromConfig(cfg),
		ELBv2: elasticloadbalancingv2.NewFromConfig(cfg),
		Logs:  cloudwatchlogs.NewFromConfig(cfg),
		RDS:   rds.NewFromConfig(cfg),
		ECR:   ecr.NewFromConfig(cfg),
	}, nil
}
