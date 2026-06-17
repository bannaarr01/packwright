package postprocess

import (
	"strings"

	"github.com/bannaarr01/packwright/internal/audit"
	"github.com/bannaarr01/packwright/internal/audit/cost"
	"github.com/bannaarr01/packwright/internal/audit/cost/pricing"
)

// computeCost dispatches on r.Kind to the matching cost computer and
// returns the CostEstimate it produces. Kinds the dispatcher does not
// recognise return cost.Unavailable("no computer for kind").
func computeCost(snap *pricing.Snapshot, r *audit.Resource) cost.CostEstimate {
	if r == nil {
		return cost.Unavailable("nil resource")
	}
	switch r.Kind {
	case "ec2/instance":
		return cost.EC2Instance(snap, cost.EC2InstanceInput{
			InstanceType: stringFromRaw(r.Raw, "instance_type"),
			State:        r.State,
			Platform:     stringFromRaw(r.Raw, "platform"),
		})
	case "ec2/volume":
		return cost.EBSVolume(snap, cost.EBSVolumeInput{
			VolumeType:     stringFromRaw(r.Raw, "volume_type"),
			SizeGB:         int64FromRaw(r.Raw, "size_gb"),
			IOPS:           int64FromRaw(r.Raw, "iops"),
			ThroughputMBPS: int64FromRaw(r.Raw, "throughput_mbps"),
		})
	case "ec2/snapshot":
		return cost.EBSSnapshot(snap, cost.EBSSnapshotInput{
			SizeGB: int64FromRaw(r.Raw, "size_gb"),
		})
	case "ec2/eip":
		return cost.EIP(snap, cost.EIPInput{
			Associated: stringFromRaw(r.Raw, "association_id") != "",
		})
	case "ec2/nat-gateway":
		return cost.NATGateway(snap, cost.NATGatewayInput{
			ProcessedGB: float64FromRaw(r.Raw, "processed_gb_monthly"),
		})
	case "elbv2/load-balancer":
		return cost.ELBv2LB(snap, cost.ELBv2LBInput{
			Type:       strings.ToLower(stringFromRaw(r.Raw, "lb_type")),
			LCUMonthly: float64FromRaw(r.Raw, "lcu_monthly"),
		})
	case "rds/db-instance":
		return cost.RDSDBInstance(snap, cost.RDSDBInstanceInput{
			Class:              stringFromRaw(r.Raw, "instance_class"),
			MultiAZ:            boolFromRaw(r.Raw, "multi_az"),
			AllocatedStorageGB: int64FromRaw(r.Raw, "allocated_storage_gb"),
			StorageType:        stringFromRaw(r.Raw, "storage_type"),
			State:              r.State,
		})
	case "rds/db-snapshot":
		return cost.RDSDBSnapshot(snap, cost.RDSDBSnapshotInput{
			SizeGB: int64FromRaw(r.Raw, "size_gb"),
		})
	case "efs/file-system":
		return cost.EFS(snap, cost.EFSInput{
			StorageClass: stringFromRaw(r.Raw, "storage_class"),
			SizeGB:       float64FromRaw(r.Raw, "size_gb"),
		})
	case "logs/log-group":
		return cost.LogsLogGroup(snap, cost.LogsLogGroupInput{
			StoredBytes:   int64FromRaw(r.Raw, "stored_bytes"),
			RetentionDays: intFromRaw(r.Raw, "retention_days"),
		})
	case "ecr/repository":
		return cost.ECRRepository(snap, cost.ECRRepositoryInput{
			ImageSizeBytesTotal: int64FromRaw(r.Raw, "image_size_bytes_total"),
		})
	case "s3/bucket":
		return cost.S3Bucket(snap, cost.S3BucketInput{
			StorageClass: stringFromRaw(r.Raw, "storage_class"),
			SizeBytes:    int64FromRaw(r.Raw, "size_bytes"),
		})
	case "codepipeline/artifacts":
		return cost.CodePipeline(snap, cost.CodePipelineInput{
			Active: boolFromRaw(r.Raw, "active_in_lookback"),
		})
	case "elbv2/target-group":
		// Target groups have no standalone cost — their LB carries
		// the bill. Report zero explicitly so the UI doesn't show
		// "?" for a row that is genuinely free.
		return cost.CostEstimate{
			Source:     cost.SourceSnapshot,
			Confidence: cost.High,
			Notes:      "Target groups are billed via their load balancer.",
		}
	}
	return cost.Unavailable("no computer for kind " + r.Kind)
}

// int64FromRaw reads key from r.Raw as an int64.
func int64FromRaw(raw map[string]any, key string) int64 {
	if raw == nil {
		return 0
	}
	v, ok := raw[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

// float64FromRaw reads key from r.Raw as a float64.
func float64FromRaw(raw map[string]any, key string) float64 {
	if raw == nil {
		return 0
	}
	v, ok := raw[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int32:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

// boolFromRaw reads key from r.Raw as a bool.
func boolFromRaw(raw map[string]any, key string) bool {
	if raw == nil {
		return false
	}
	v, ok := raw[key]
	if !ok {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}
