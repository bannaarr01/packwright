package delete

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
)

// Dependent is a single AWS object that references the resource a
// dependency probe was run for. Blocking means AWS will refuse the
// Delete* call until this dependent is removed first; informational
// dependents are surfaced for the user's awareness only.
type Dependent struct {
	// Kind names the type of the referring resource. It does not
	// have to be one this package can delete — e.g. AMIs block
	// snapshots but no audit/delete-ami tool exists.
	Kind string
	// Identifier is the AWS handle of the referring resource (AMI
	// id, instance id, listener ARN, ...).
	Identifier string
	// Blocking is true when AWS will refuse the Delete* until this
	// dependent is removed.
	Blocking bool
	// Detail is a short human-readable explanation, e.g.
	// "AMI ami-7e8 (in use by 2 EC2 instances)".
	Detail string
}

// RowDependencies is the result of probing a single row.
type RowDependencies struct {
	// RowID is the tray row this result describes.
	RowID string
	// Dependents lists every reference the probe found.
	Dependents []Dependent
	// Blocked is true when at least one Dependent has Blocking=true.
	// Mirrors a helper that callers would otherwise re-derive on
	// every check; keeping it on the struct lets the consent layer
	// branch on a single field.
	Blocked bool
	// Err, when non-nil, means the probe failed and the badge for
	// this row should be "?" — neither safe nor explicitly blocked.
	// The consent modal renders the error verbatim so the user can
	// retry, skip, or proceed at their own risk.
	Err error
}

// Probe runs a read-only Describe* sweep for every row in rows,
// returning per-row dependency information. A probe failure for one
// row never aborts the sweep; the error is stored on the affected
// RowDependencies entry.
//
// The sweep is sequential because dependency probes are short and a
// row-failure-is-local guarantee is more important than the small
// wall-clock gain from parallelism here. Callers that need the
// concurrency win can fan out a Probe per row themselves.
func Probe(ctx context.Context, c *Clients, rows []Row) []RowDependencies {
	out := make([]RowDependencies, len(rows))
	for i, row := range rows {
		out[i] = ProbeRow(ctx, c, row)
	}
	return out
}

// ProbeRow runs the kind-specific dependency probe for a single row.
// Unknown kinds yield an empty RowDependencies (the row's Validate
// would have caught the same problem at tray-add time, so this is a
// defensive fallback).
func ProbeRow(ctx context.Context, c *Clients, row Row) RowDependencies {
	rd := RowDependencies{RowID: row.ID}
	var probe func(context.Context, *Clients, Resource) ([]Dependent, error)
	switch row.Resource.Kind {
	case KindEC2Volume:
		probe = probeVolume
	case KindEC2Snapshot:
		probe = probeSnapshot
	case KindEC2EIP:
		probe = probeEIP
	case KindEC2NATGateway:
		probe = probeNATGateway
	case KindELBv2TargetGroup:
		probe = probeTargetGroup
	case KindLogsLogGroup:
		probe = probeLogGroup
	case KindRDSDBSnapshot:
		probe = probeRDSSnapshot
	case KindECRImage:
		probe = probeECRImage
	default:
		return rd
	}
	deps, err := probe(ctx, c, row.Resource)
	if err != nil {
		rd.Err = err
		return rd
	}
	rd.Dependents = deps
	for _, d := range deps {
		if d.Blocking {
			rd.Blocked = true
			break
		}
	}
	return rd
}

// probeVolume surfaces the instance a volume is attached to (if any).
// An attached volume is blocking — AWS refuses DeleteVolume on it.
func probeVolume(ctx context.Context, c *Clients, res Resource) ([]Dependent, error) {
	if c == nil || c.EC2 == nil {
		return nil, fmt.Errorf("%w: EC2 (ec2/volume)", ErrServiceUnavailable)
	}
	out, err := c.EC2.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{
		VolumeIds: []string{res.Identifier},
	})
	if err != nil {
		return nil, fmt.Errorf("delete: describe volumes %s: %w", res.Identifier, err)
	}
	if len(out.Volumes) == 0 {
		return nil, nil
	}
	v := out.Volumes[0]
	deps := make([]Dependent, 0, len(v.Attachments))
	for _, a := range v.Attachments {
		deps = append(deps, Dependent{
			Kind:       "ec2/instance",
			Identifier: aws.ToString(a.InstanceId),
			Blocking:   true,
			Detail:     fmt.Sprintf("attached to instance %s (device %s)", aws.ToString(a.InstanceId), aws.ToString(a.Device)),
		})
	}
	return deps, nil
}

// probeSnapshot surfaces AMIs that reference the snapshot. An AMI
// reference is blocking (AWS refuses DeleteSnapshot while an AMI
// still uses it). The probe drills one level further when the AMI
// is itself in use by an instance, so the modal can explain
// "REQUIRES the AMI be deleted first; cannot proceed".
func probeSnapshot(ctx context.Context, c *Clients, res Resource) ([]Dependent, error) {
	if c == nil || c.EC2 == nil {
		return nil, fmt.Errorf("%w: EC2 (ec2/snapshot)", ErrServiceUnavailable)
	}
	out, err := c.EC2.DescribeImages(ctx, &ec2.DescribeImagesInput{
		Filters: []ec2types.Filter{{
			Name:   aws.String("block-device-mapping.snapshot-id"),
			Values: []string{res.Identifier},
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("delete: describe images for snapshot %s: %w", res.Identifier, err)
	}
	if len(out.Images) == 0 {
		return nil, nil
	}
	deps := make([]Dependent, 0, len(out.Images))
	for _, img := range out.Images {
		amiID := aws.ToString(img.ImageId)
		users, err := imageUsers(ctx, c, amiID)
		detail := fmt.Sprintf("backs AMI %s", amiID)
		if err == nil && users > 0 {
			detail = fmt.Sprintf("backs AMI %s (in use by %d EC2 instance(s))", amiID, users)
		}
		deps = append(deps, Dependent{
			Kind:       "ec2/image",
			Identifier: amiID,
			Blocking:   true,
			Detail:     detail,
		})
	}
	return deps, nil
}

// imageUsers counts running instances that use amiID. Errors are
// swallowed — this is decoration text on a Dependent.Detail and a
// transient API failure should not fail the surrounding probe.
func imageUsers(ctx context.Context, c *Clients, amiID string) (int, error) {
	out, err := c.EC2.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		Filters: []ec2types.Filter{{
			Name:   aws.String("image-id"),
			Values: []string{amiID},
		}},
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range out.Reservations {
		n += len(r.Instances)
	}
	return n, nil
}

// probeEIP surfaces a current association (instance or
// network-interface) for the EIP. An associated EIP is treated as
// blocking: AWS refuses ReleaseAddress on a VPC EIP while it is
// associated, and the safer default is to keep the row unselectable
// in the modal until the user disassociates the address themselves.
func probeEIP(ctx context.Context, c *Clients, res Resource) ([]Dependent, error) {
	if c == nil || c.EC2 == nil {
		return nil, fmt.Errorf("%w: EC2 (ec2/eip)", ErrServiceUnavailable)
	}
	out, err := c.EC2.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{
		AllocationIds: []string{res.Identifier},
	})
	if err != nil {
		return nil, fmt.Errorf("delete: describe addresses %s: %w", res.Identifier, err)
	}
	var deps []Dependent
	for _, a := range out.Addresses {
		if a.AssociationId == nil && a.InstanceId == nil && a.NetworkInterfaceId == nil {
			continue
		}
		target := aws.ToString(a.InstanceId)
		kind := "ec2/instance"
		if target == "" {
			target = aws.ToString(a.NetworkInterfaceId)
			kind = "ec2/network-interface"
		}
		deps = append(deps, Dependent{
			Kind:       kind,
			Identifier: target,
			Blocking:   true,
			Detail:     fmt.Sprintf("associated with %s %s (assoc %s)", kind, target, aws.ToString(a.AssociationId)),
		})
	}
	return deps, nil
}

// probeNATGateway surfaces routes that send traffic through it. A
// route is informational — AWS deletes a NAT gateway happily even
// while routes still reference it, leaving orphan routes the user
// will want to clean up afterwards.
func probeNATGateway(ctx context.Context, c *Clients, res Resource) ([]Dependent, error) {
	if c == nil || c.EC2 == nil {
		return nil, fmt.Errorf("%w: EC2 (ec2/nat-gateway)", ErrServiceUnavailable)
	}
	out, err := c.EC2.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
		Filters: []ec2types.Filter{{
			Name:   aws.String("route.nat-gateway-id"),
			Values: []string{res.Identifier},
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("delete: describe route tables for nat-gateway %s: %w", res.Identifier, err)
	}
	deps := make([]Dependent, 0, len(out.RouteTables))
	for _, rt := range out.RouteTables {
		deps = append(deps, Dependent{
			Kind:       "ec2/route-table",
			Identifier: aws.ToString(rt.RouteTableId),
			Blocking:   false,
			Detail:     fmt.Sprintf("route table %s has a route via this NAT", aws.ToString(rt.RouteTableId)),
		})
	}
	return deps, nil
}

// probeTargetGroup surfaces listeners and rules that forward to the
// target group. Any listener/rule reference is blocking (ELBv2
// refuses DeleteTargetGroup while one exists). Both the listener's
// default action and every rule on the listener are inspected — a
// rule-level forward is just as blocking as a default-action one.
func probeTargetGroup(ctx context.Context, c *Clients, res Resource) ([]Dependent, error) {
	if c == nil || c.ELBv2 == nil {
		return nil, fmt.Errorf("%w: ELBv2 (elbv2/target-group)", ErrServiceUnavailable)
	}
	tg, err := c.ELBv2.DescribeTargetGroups(ctx, &elasticloadbalancingv2.DescribeTargetGroupsInput{
		TargetGroupArns: []string{res.Identifier},
	})
	if err != nil {
		return nil, fmt.Errorf("delete: describe target groups %s: %w", res.Identifier, err)
	}
	if len(tg.TargetGroups) == 0 {
		return nil, nil
	}
	var deps []Dependent
	for _, t := range tg.TargetGroups {
		for _, lbArn := range t.LoadBalancerArns {
			ls, err := c.ELBv2.DescribeListeners(ctx, &elasticloadbalancingv2.DescribeListenersInput{
				LoadBalancerArn: aws.String(lbArn),
			})
			if err != nil {
				return nil, fmt.Errorf("delete: describe listeners for %s: %w", lbArn, err)
			}
			for _, l := range ls.Listeners {
				listenerARN := aws.ToString(l.ListenerArn)
				if listenerForwardsTo(l.DefaultActions, res.Identifier) {
					deps = append(deps, Dependent{
						Kind:       "elbv2/listener",
						Identifier: listenerARN,
						Blocking:   true,
						Detail:     fmt.Sprintf("listener %s forwards to this target group", listenerARN),
					})
				}
				rules, err := c.ELBv2.DescribeRules(ctx, &elasticloadbalancingv2.DescribeRulesInput{
					ListenerArn: aws.String(listenerARN),
				})
				if err != nil {
					return nil, fmt.Errorf("delete: describe rules for %s: %w", listenerARN, err)
				}
				for _, r := range rules.Rules {
					if listenerForwardsTo(r.Actions, res.Identifier) {
						deps = append(deps, Dependent{
							Kind:       "elbv2/listener-rule",
							Identifier: aws.ToString(r.RuleArn),
							Blocking:   true,
							Detail:     fmt.Sprintf("rule %s on listener %s forwards to this target group", aws.ToString(r.RuleArn), listenerARN),
						})
					}
				}
			}
		}
	}
	return deps, nil
}

// listenerForwardsTo reports whether any action in actions forwards
// to tgARN. Both the simple "TargetGroupArn" form and the weighted
// "ForwardConfig.TargetGroups" form are inspected.
func listenerForwardsTo(actions []elbv2types.Action, tgARN string) bool {
	for _, a := range actions {
		if aws.ToString(a.TargetGroupArn) == tgARN {
			return true
		}
		if a.ForwardConfig != nil {
			for _, t := range a.ForwardConfig.TargetGroups {
				if aws.ToString(t.TargetGroupArn) == tgARN {
					return true
				}
			}
		}
	}
	return false
}

// probeLogGroup applies the ADR-0043 heuristic: a log group named
// /aws/lambda/<fn> implies a Lambda function "<fn>"; we surface that
// as an informational dependent annotated "(name suggests source
// Lambda <fn>)". Real cross-checking against the Lambda service is
// out of scope (the LogsAPI subset would have to expand, and Lambda
// is not yet wired up); this heuristic is enough to support the
// modal's "source fn deleted" badge meaningfully.
//
// Log groups also have no AWS-side blocking dependents — Delete works
// regardless — so every Dependent here is informational.
func probeLogGroup(ctx context.Context, c *Clients, res Resource) ([]Dependent, error) {
	if c == nil || c.Logs == nil {
		return nil, fmt.Errorf("%w: Logs (logs/log-group)", ErrServiceUnavailable)
	}
	// Quick existence check via DescribeLogGroups so we can report
	// an explicit "no such log group" rather than a confusing AWS
	// error on Delete.
	out, err := c.Logs.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(res.Identifier),
	})
	if err != nil {
		return nil, fmt.Errorf("delete: describe log groups %s: %w", res.Identifier, err)
	}
	var present bool
	for _, lg := range out.LogGroups {
		if aws.ToString(lg.LogGroupName) == res.Identifier {
			present = true
			break
		}
	}
	if !present {
		return nil, fmt.Errorf("delete: log group %q does not exist in this account/region", res.Identifier)
	}
	const lambdaPrefix = "/aws/lambda/"
	if strings.HasPrefix(res.Identifier, lambdaPrefix) {
		fn := strings.TrimPrefix(res.Identifier, lambdaPrefix)
		return []Dependent{{
			Kind:       "lambda/function",
			Identifier: fn,
			Blocking:   false,
			Detail:     fmt.Sprintf("name suggests source Lambda %q (verify the function still exists before deleting)", fn),
		}}, nil
	}
	return nil, nil
}

// probeRDSSnapshot surfaces whether the source DB instance still
// exists. A live source is informational (a snapshot remains
// useful while the source is alive); a deleted source matches the
// ADR-0043 "source deleted" badge case.
func probeRDSSnapshot(ctx context.Context, c *Clients, res Resource) ([]Dependent, error) {
	if c == nil || c.RDS == nil {
		return nil, fmt.Errorf("%w: RDS (rds/db-snapshot)", ErrServiceUnavailable)
	}
	snaps, err := c.RDS.DescribeDBSnapshots(ctx, &rds.DescribeDBSnapshotsInput{
		DBSnapshotIdentifier: aws.String(res.Identifier),
	})
	if err != nil {
		return nil, fmt.Errorf("delete: describe db snapshots %s: %w", res.Identifier, err)
	}
	if len(snaps.DBSnapshots) == 0 {
		return nil, fmt.Errorf("db snapshot %q does not exist", res.Identifier)
	}
	src := aws.ToString(snaps.DBSnapshots[0].DBInstanceIdentifier)
	if src == "" {
		return nil, nil
	}
	dbs, err := c.RDS.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(src),
	})
	if err != nil {
		// AWS returns DBInstanceNotFound when the source has been
		// deleted; surface that as the documented "source deleted"
		// case rather than treating it as a probe failure.
		var notFound interface{ ErrorCode() string }
		if errors.As(err, &notFound) && notFound.ErrorCode() == "DBInstanceNotFound" {
			return []Dependent{{
				Kind:       "rds/db-instance",
				Identifier: src,
				Blocking:   false,
				Detail:     fmt.Sprintf("source DB %q has been deleted (safe to delete this snapshot)", src),
			}}, nil
		}
		return nil, fmt.Errorf("delete: describe db instance %s: %w", src, err)
	}
	if len(dbs.DBInstances) == 0 {
		return []Dependent{{
			Kind:       "rds/db-instance",
			Identifier: src,
			Blocking:   false,
			Detail:     fmt.Sprintf("source DB %q has been deleted (safe to delete this snapshot)", src),
		}}, nil
	}
	return []Dependent{{
		Kind:       "rds/db-instance",
		Identifier: src,
		Blocking:   false,
		Detail:     fmt.Sprintf("source DB %q still exists", src),
	}}, nil
}

// probeECRImage confirms the image exists and reports any tags
// pointing at the digest. ECR has no cross-account "who pulls this?"
// surface, so dependents are informational only: a digest with no
// tags is the typical safe-to-delete case, a tagged digest signals
// the image is reachable by name and may still be in use.
func probeECRImage(ctx context.Context, c *Clients, res Resource) ([]Dependent, error) {
	if c == nil || c.ECR == nil {
		return nil, fmt.Errorf("%w: ECR (ecr/image)", ErrServiceUnavailable)
	}
	repo := res.Extra["repository_name"]
	if repo == "" {
		return nil, fmt.Errorf("ecr/image: extra.repository_name is required")
	}
	out, err := c.ECR.DescribeImages(ctx, &ecr.DescribeImagesInput{
		RepositoryName: aws.String(repo),
		ImageIds: []ecrtypes.ImageIdentifier{
			{ImageDigest: aws.String(res.Identifier)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("delete: describe ecr image %s/%s: %w", repo, res.Identifier, err)
	}
	if len(out.ImageDetails) == 0 {
		return nil, fmt.Errorf("ecr image %s/%s does not exist", repo, res.Identifier)
	}
	tags := out.ImageDetails[0].ImageTags
	if len(tags) == 0 {
		return nil, nil
	}
	deps := make([]Dependent, 0, len(tags))
	for _, t := range tags {
		deps = append(deps, Dependent{
			Kind:       "ecr/image-tag",
			Identifier: t,
			Blocking:   false,
			Detail:     fmt.Sprintf("digest is tagged %q (consumers pulling by tag will break)", t),
		})
	}
	return deps, nil
}
