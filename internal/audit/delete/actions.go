package delete

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

// ErrServiceUnavailable is returned when a row's Kind requires a
// service client that is nil on the supplied Clients struct (e.g.
// the caller did not wire up ECR but staged an ecr/image row).
var ErrServiceUnavailable = errors.New("delete: required AWS service client is nil")

// DeleteResource dispatches to the kind-specific Delete* helper for
// res. This is the single entry point both the Executor and the
// audit/delete-* tool catalogue funnel through; keeping the dispatch
// in one place means a future kind only adds two functions (probe +
// delete) and one tool registration without touching the orchestration.
//
// The caller is responsible for confirming the user has consented
// (typed-DELETE confirmation for the human flow; consent.Gate for
// the AI flow) before invoking DeleteResource.
func DeleteResource(ctx context.Context, c *Clients, res Resource) error {
	if err := res.Validate(); err != nil {
		return err
	}
	switch res.Kind {
	case KindEC2Volume:
		return deleteVolume(ctx, c, res)
	case KindEC2Snapshot:
		return deleteSnapshot(ctx, c, res)
	case KindEC2EIP:
		return releaseEIP(ctx, c, res)
	case KindEC2NATGateway:
		return deleteNATGateway(ctx, c, res)
	case KindELBv2TargetGroup:
		return deleteTargetGroup(ctx, c, res)
	case KindLogsLogGroup:
		return deleteLogGroup(ctx, c, res)
	case KindRDSDBSnapshot:
		return deleteRDSSnapshot(ctx, c, res)
	case KindECRImage:
		return deleteECRImage(ctx, c, res)
	}
	return fmt.Errorf("delete: unsupported kind %q", res.Kind)
}

func deleteVolume(ctx context.Context, c *Clients, res Resource) error {
	if c == nil || c.EC2 == nil {
		return fmt.Errorf("%w: EC2 (ec2/volume)", ErrServiceUnavailable)
	}
	_, err := c.EC2.DeleteVolume(ctx, &ec2.DeleteVolumeInput{
		VolumeId: aws.String(res.Identifier),
	})
	if err != nil {
		return fmt.Errorf("delete: ec2:DeleteVolume %s: %w", res.Identifier, err)
	}
	return nil
}

func deleteSnapshot(ctx context.Context, c *Clients, res Resource) error {
	if c == nil || c.EC2 == nil {
		return fmt.Errorf("%w: EC2 (ec2/snapshot)", ErrServiceUnavailable)
	}
	_, err := c.EC2.DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{
		SnapshotId: aws.String(res.Identifier),
	})
	if err != nil {
		return fmt.Errorf("delete: ec2:DeleteSnapshot %s: %w", res.Identifier, err)
	}
	return nil
}

func releaseEIP(ctx context.Context, c *Clients, res Resource) error {
	if c == nil || c.EC2 == nil {
		return fmt.Errorf("%w: EC2 (ec2/eip)", ErrServiceUnavailable)
	}
	_, err := c.EC2.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{
		AllocationId: aws.String(res.Identifier),
	})
	if err != nil {
		return fmt.Errorf("delete: ec2:ReleaseAddress %s: %w", res.Identifier, err)
	}
	return nil
}

func deleteNATGateway(ctx context.Context, c *Clients, res Resource) error {
	if c == nil || c.EC2 == nil {
		return fmt.Errorf("%w: EC2 (ec2/nat-gateway)", ErrServiceUnavailable)
	}
	_, err := c.EC2.DeleteNatGateway(ctx, &ec2.DeleteNatGatewayInput{
		NatGatewayId: aws.String(res.Identifier),
	})
	if err != nil {
		return fmt.Errorf("delete: ec2:DeleteNatGateway %s: %w", res.Identifier, err)
	}
	return nil
}

func deleteTargetGroup(ctx context.Context, c *Clients, res Resource) error {
	if c == nil || c.ELBv2 == nil {
		return fmt.Errorf("%w: ELBv2 (elbv2/target-group)", ErrServiceUnavailable)
	}
	_, err := c.ELBv2.DeleteTargetGroup(ctx, &elasticloadbalancingv2.DeleteTargetGroupInput{
		TargetGroupArn: aws.String(res.Identifier),
	})
	if err != nil {
		return fmt.Errorf("delete: elbv2:DeleteTargetGroup %s: %w", res.Identifier, err)
	}
	return nil
}

func deleteLogGroup(ctx context.Context, c *Clients, res Resource) error {
	if c == nil || c.Logs == nil {
		return fmt.Errorf("%w: Logs (logs/log-group)", ErrServiceUnavailable)
	}
	_, err := c.Logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{
		LogGroupName: aws.String(res.Identifier),
	})
	if err != nil {
		return fmt.Errorf("delete: logs:DeleteLogGroup %s: %w", res.Identifier, err)
	}
	return nil
}

func deleteRDSSnapshot(ctx context.Context, c *Clients, res Resource) error {
	if c == nil || c.RDS == nil {
		return fmt.Errorf("%w: RDS (rds/db-snapshot)", ErrServiceUnavailable)
	}
	_, err := c.RDS.DeleteDBSnapshot(ctx, &rds.DeleteDBSnapshotInput{
		DBSnapshotIdentifier: aws.String(res.Identifier),
	})
	if err != nil {
		return fmt.Errorf("delete: rds:DeleteDBSnapshot %s: %w", res.Identifier, err)
	}
	return nil
}

func deleteECRImage(ctx context.Context, c *Clients, res Resource) error {
	if c == nil || c.ECR == nil {
		return fmt.Errorf("%w: ECR (ecr/image)", ErrServiceUnavailable)
	}
	repo := res.Extra["repository_name"]
	if repo == "" {
		return errors.New("delete: ecr/image: missing extra.repository_name")
	}
	out, err := c.ECR.BatchDeleteImage(ctx, &ecr.BatchDeleteImageInput{
		RepositoryName: aws.String(repo),
		ImageIds: []ecrtypes.ImageIdentifier{
			{ImageDigest: aws.String(res.Identifier)},
		},
	})
	if err != nil {
		return fmt.Errorf("delete: ecr:BatchDeleteImage %s/%s: %w", repo, res.Identifier, err)
	}
	if len(out.Failures) > 0 {
		f := out.Failures[0]
		return fmt.Errorf("delete: ecr:BatchDeleteImage %s/%s: %s: %s",
			repo, res.Identifier, f.FailureCode, aws.ToString(f.FailureReason))
	}
	return nil
}
