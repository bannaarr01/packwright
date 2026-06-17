package postprocess

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit"
	"github.com/bannaarr01/packwright/internal/audit/lastused"
	perkind "github.com/bannaarr01/packwright/internal/audit/lastused/per_kind"
)

// computeLastUsed dispatches on r.Kind to the matching per-kind
// composer and returns the LastUsedSignal it produces. Kinds the
// dispatcher does not recognise return a zero LastUsedSignal — the
// scanner-stored fields (CreatedAt, State, Raw) still surface in the
// UI; only the idleness panel is empty.
func computeLastUsed(ctx context.Context, r *audit.Resource, c *clients, lookback int) lastused.LastUsedSignal {
	if r == nil {
		return lastused.LastUsedSignal{}
	}
	switch r.Kind {
	case "ec2/instance":
		return perkind.EC2Instance(ctx, c.metrics, c.eni, perkind.EC2InstanceInput{
			InstanceID:   r.ID,
			LaunchTime:   timePtr(r.CreatedAt),
			ENIIDs:       stringSlice(r.Raw, "eni_ids"),
			LookbackDays: lookback,
		})
	case "ec2/volume":
		return perkind.EC2Volume(ctx, c.metrics, perkind.EC2VolumeInput{
			VolumeID:     r.ID,
			State:        r.State,
			AttachTime:   timeFromRaw(r.Raw, "attach_time"),
			CreateTime:   timePtr(r.CreatedAt),
			LookbackDays: lookback,
		})
	case "ec2/snapshot":
		return perkind.EC2Snapshot(ctx, c.ami, perkind.EC2SnapshotInput{
			SnapshotID: r.ID,
			StartTime:  timePtr(r.CreatedAt),
		})
	case "ec2/eip":
		return perkind.EC2EIP(perkind.EC2EIPInput{
			AllocationID:   r.ID,
			AssociationID:  stringFromRaw(r.Raw, "association_id"),
			AllocationTime: timePtr(r.CreatedAt),
		})
	case "ec2/nat-gateway":
		return perkind.EC2NATGateway(ctx, c.metrics, perkind.EC2NATGatewayInput{
			NATGatewayID: r.ID,
			CreateTime:   timePtr(r.CreatedAt),
			LookbackDays: lookback,
		})
	case "elbv2/load-balancer":
		return perkind.ELBv2LoadBalancer(ctx, c.metrics, c.elbAccessLogs, perkind.ELBv2LoadBalancerInput{
			LoadBalancerName: stringFromRaw(r.Raw, "lb_name"),
			AccessLogsBucket: stringFromRaw(r.Raw, "access_logs_bucket"),
			AccessLogsPrefix: stringFromRaw(r.Raw, "access_logs_prefix"),
			LookbackDays:     lookback,
		})
	case "elbv2/target-group":
		return perkind.ELBv2TargetGroup(ctx, c.metrics, perkind.ELBv2TargetGroupInput{
			TargetGroupFullName: stringFromRaw(r.Raw, "tg_full_name"),
			LoadBalancerName:    stringFromRaw(r.Raw, "lb_name"),
			HealthyTargets:      intFromRaw(r.Raw, "healthy_targets"),
			LookbackDays:        lookback,
		})
	case "rds/db-instance":
		return perkind.RDSDBInstance(ctx, c.metrics, perkind.RDSDBInstanceInput{
			DBInstanceIdentifier: r.ID,
			LatestRestorableTime: timeFromRaw(r.Raw, "latest_restorable_time"),
			LookbackDays:         lookback,
		})
	case "rds/db-snapshot":
		return perkind.RDSDBSnapshot(ctx, c.rdsExists, perkind.RDSDBSnapshotInput{
			DBSnapshotIdentifier:       r.ID,
			SourceDBInstanceIdentifier: stringFromRaw(r.Raw, "source_db_identifier"),
			SnapshotCreateTime:         timePtr(r.CreatedAt),
		})
	case "efs/file-system":
		return perkind.EFSFileSystem(ctx, c.metrics, perkind.EFSFileSystemInput{
			FileSystemID:     r.ID,
			LastModifiedTime: timeFromRaw(r.Raw, "last_modified_time"),
			LookbackDays:     lookback,
		})
	case "logs/log-group":
		return perkind.LogsLogGroup(ctx, c.logs, perkind.LogsLogGroupInput{
			LogGroupName:    r.Name,
			CreationTime:    timePtr(r.CreatedAt),
			RetentionInDays: intPtrFromRaw(r.Raw, "retention_days"),
		})
	case "ecr/repository":
		return perkind.ECRRepository(ctx, c.ecr, perkind.ECRRepositoryInput{
			RepositoryName: r.Name,
		})
	case "s3/bucket":
		return perkind.S3Bucket(ctx, c.metrics, c.s3Sample, perkind.S3BucketInput{
			BucketName:   r.Name,
			SampleSize:   100,
			LookbackDays: lookback,
		})
	case "codepipeline/artifacts":
		return perkind.Pipeline(ctx, c.pipeline, perkind.PipelineInput{
			PipelineName: r.Name,
		})
	}
	return lastused.LastUsedSignal{}
}

// timePtr returns &t when t is non-zero, nil otherwise. The lastused
// composers treat nil as "no signal", which is exactly the right
// behaviour for scanners that left CreatedAt zero.
func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// timeFromRaw reads key from r.Raw and returns it as *time.Time when
// the value is time.Time, *time.Time, or RFC3339-formatted string.
// Otherwise returns nil.
func timeFromRaw(raw map[string]any, key string) *time.Time {
	if raw == nil {
		return nil
	}
	v, ok := raw[key]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case time.Time:
		if t.IsZero() {
			return nil
		}
		return &t
	case *time.Time:
		return t
	case string:
		parsed, err := time.Parse(time.RFC3339, t)
		if err != nil {
			return nil
		}
		return &parsed
	}
	return nil
}

// stringFromRaw reads key from r.Raw and returns it as a string, or
// "" when missing or not a string.
func stringFromRaw(raw map[string]any, key string) string {
	if raw == nil {
		return ""
	}
	v, ok := raw[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// intFromRaw reads key from r.Raw and returns it as an int. Handles
// int, int32, int64, and float64 (JSON-decoded). Returns 0 on miss
// or type mismatch.
func intFromRaw(raw map[string]any, key string) int {
	if raw == nil {
		return 0
	}
	v, ok := raw[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// intPtrFromRaw reads key from r.Raw and returns it as a *int. Returns
// nil when the key is missing (which the composers interpret as
// "never expire" for retention).
func intPtrFromRaw(raw map[string]any, key string) *int {
	if raw == nil {
		return nil
	}
	v, ok := raw[key]
	if !ok {
		return nil
	}
	switch n := v.(type) {
	case int:
		return &n
	case int32:
		x := int(n)
		return &x
	case int64:
		x := int(n)
		return &x
	case float64:
		x := int(n)
		return &x
	}
	return nil
}

// stringSlice reads key from r.Raw and returns it as a []string. Handles
// []string and []any of strings. Returns nil on miss or mismatch.
func stringSlice(raw map[string]any, key string) []string {
	if raw == nil {
		return nil
	}
	v, ok := raw[key]
	if !ok {
		return nil
	}
	switch xs := v.(type) {
	case []string:
		return xs
	case []any:
		out := make([]string, 0, len(xs))
		for _, x := range xs {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
