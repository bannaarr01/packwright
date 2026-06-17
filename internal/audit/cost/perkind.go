package cost

import (
	"fmt"
	"strings"

	"github.com/bannaarr01/packwright/internal/audit/cost/pricing"
)

// confidenceFromSource maps a pricing Source to its baseline
// Confidence per ADR-0042 §3. Snapshot data is one notch lower than
// the live Pricing API; missing data falls to Low.
func confidenceFromSource(src Source) Confidence {
	switch src {
	case SourcePricingAPI:
		return High
	case SourceSnapshot:
		return Medium
	case SourceCostExplorer:
		return High
	default:
		return Low
	}
}

// snapshotSource is the source every embedded-snapshot computer
// returns. Surfaced as a constant for readability.
const snapshotSource = SourceSnapshot

// ----------------- EC2 instance -----------------

// EC2InstanceInput is the per-instance cost computer's input. The
// caller extracts these fields from the scanner's Raw map.
type EC2InstanceInput struct {
	// InstanceType is the EC2 instance type ("t3.medium", "m5.large").
	InstanceType string
	// State is the EC2 state ("running", "stopped", "terminated"). A
	// stopped instance still bills for attached storage but not
	// compute; this computer reports the running-cost equivalent and
	// notes when the instance is in a non-running state.
	State string
	// Platform is the OS family ("linux", "windows"). Empty means
	// linux.
	Platform string
}

// EC2Instance computes the monthly cost of one running EC2 instance.
// Stopped instances return MonthlyUSD=0 with a note.
func EC2Instance(snap *pricing.Snapshot, in EC2InstanceInput) CostEstimate {
	if in.State != "" && !strings.EqualFold(in.State, "running") {
		return CostEstimate{
			Source:     snapshotSource,
			Confidence: Medium,
			Notes:      fmt.Sprintf("Instance state %q — no compute cost while not running.", in.State),
		}
	}
	if snap == nil {
		return Unavailable("no pricing snapshot for region")
	}
	price, ok := snap.EC2Instance[in.InstanceType]
	if !ok {
		return Unavailable(fmt.Sprintf("no pricing for instance type %q", in.InstanceType))
	}
	perHour := price.LinuxPerHour
	notes := ""
	if strings.EqualFold(in.Platform, "windows") {
		if price.WindowsPerHour > 0 {
			perHour = price.WindowsPerHour
		} else {
			notes = "Windows price unavailable; using Linux price as approximation."
		}
	}
	monthly := perHour * HoursPerMonth
	lines := []CostLine{{
		Component:  fmt.Sprintf("EC2 %s hours", in.InstanceType),
		Amount:     fmt.Sprintf("730h × $%.4f/h", perHour),
		MonthlyUSD: monthly,
	}}
	return Sum(snapshotSource, confidenceFromSource(snapshotSource), notes, lines)
}

// ----------------- EBS volume -----------------

// EBSVolumeInput is the per-volume cost computer's input.
type EBSVolumeInput struct {
	// VolumeType is one of "gp2", "gp3", "io1", "io2", "st1", "sc1",
	// "standard".
	VolumeType string
	// SizeGB is the provisioned size in GB.
	SizeGB int64
	// IOPS is the provisioned IOPS for io1/io2/gp3 volumes. Zero for
	// other types.
	IOPS int64
	// ThroughputMBPS is the provisioned throughput for gp3 volumes.
	// Zero for other types.
	ThroughputMBPS int64
}

// EBSVolume computes the monthly cost of one EBS volume.
func EBSVolume(snap *pricing.Snapshot, in EBSVolumeInput) CostEstimate {
	if snap == nil {
		return Unavailable("no pricing snapshot for region")
	}
	t := strings.ToLower(in.VolumeType)
	if t == "" {
		t = "gp3"
	}
	price, ok := snap.EBSVolume[t]
	if !ok {
		return Unavailable(fmt.Sprintf("no pricing for volume type %q", in.VolumeType))
	}
	lines := []CostLine{{
		Component:  fmt.Sprintf("EBS %s storage", t),
		Amount:     fmt.Sprintf("%d GB × $%.4f/GB-month", in.SizeGB, price.PerGBMonth),
		MonthlyUSD: float64(in.SizeGB) * price.PerGBMonth,
	}}
	if price.PerIOPSMonth > 0 && in.IOPS > 0 {
		// gp3 includes 3000 baseline IOPS at no extra cost.
		billable := in.IOPS
		if t == "gp3" && billable > 3000 {
			billable -= 3000
		} else if t == "gp3" {
			billable = 0
		}
		if billable > 0 {
			lines = append(lines, CostLine{
				Component:  fmt.Sprintf("EBS %s IOPS", t),
				Amount:     fmt.Sprintf("%d IOPS × $%.4f/IOPS-month", billable, price.PerIOPSMonth),
				MonthlyUSD: float64(billable) * price.PerIOPSMonth,
			})
		}
	}
	if price.PerThroughputMonth > 0 && in.ThroughputMBPS > 0 {
		// gp3 includes 125 MB/s baseline at no extra cost.
		billable := in.ThroughputMBPS
		if t == "gp3" && billable > 125 {
			billable -= 125
		} else if t == "gp3" {
			billable = 0
		}
		if billable > 0 {
			lines = append(lines, CostLine{
				Component:  fmt.Sprintf("EBS %s throughput", t),
				Amount:     fmt.Sprintf("%d MB/s × $%.4f/MB-s-month", billable, price.PerThroughputMonth),
				MonthlyUSD: float64(billable) * price.PerThroughputMonth,
			})
		}
	}
	return Sum(snapshotSource, confidenceFromSource(snapshotSource), "", lines)
}

// ----------------- EBS snapshot -----------------

// EBSSnapshotInput is the per-snapshot cost computer's input.
type EBSSnapshotInput struct {
	SizeGB int64
}

// EBSSnapshot computes the monthly cost of one EBS snapshot.
func EBSSnapshot(snap *pricing.Snapshot, in EBSSnapshotInput) CostEstimate {
	if snap == nil || snap.EBSSnapshot == nil {
		return Unavailable("no snapshot pricing for region")
	}
	monthly := float64(in.SizeGB) * snap.EBSSnapshot.PerGBMonth
	lines := []CostLine{{
		Component:  "EBS snapshot",
		Amount:     fmt.Sprintf("%d GB × $%.4f/GB-month", in.SizeGB, snap.EBSSnapshot.PerGBMonth),
		MonthlyUSD: monthly,
	}}
	return Sum(snapshotSource, confidenceFromSource(snapshotSource), "", lines)
}

// ----------------- Elastic IP -----------------

// EIPInput is the per-EIP cost computer's input.
type EIPInput struct {
	// Associated is true when the EIP is attached to a running
	// instance. Idle EIPs incur an hourly charge; associated ones do
	// not.
	Associated bool
}

// EIP computes the monthly cost of one Elastic IP.
func EIP(snap *pricing.Snapshot, in EIPInput) CostEstimate {
	if snap == nil || snap.EIP == nil {
		return Unavailable("no EIP pricing for region")
	}
	if in.Associated {
		return CostEstimate{
			Source:     snapshotSource,
			Confidence: High,
			Notes:      "EIP associated to a running instance — no idle charge.",
		}
	}
	monthly := snap.EIP.IdlePerHour * HoursPerMonth
	lines := []CostLine{{
		Component:  "EIP idle",
		Amount:     fmt.Sprintf("730h × $%.4f/h", snap.EIP.IdlePerHour),
		MonthlyUSD: monthly,
	}}
	return Sum(snapshotSource, confidenceFromSource(snapshotSource), "Unassociated EIP — billed hourly.", lines)
}

// ----------------- NAT gateway -----------------

// NATGatewayInput is the per-NAT cost computer's input.
type NATGatewayInput struct {
	// ProcessedGB is the monthly traffic estimate in GB. Zero when no
	// CloudWatch data is available — the computer then bills hourly
	// only and notes the missing component.
	ProcessedGB float64
}

// NATGateway computes the monthly cost of one NAT gateway.
func NATGateway(snap *pricing.Snapshot, in NATGatewayInput) CostEstimate {
	if snap == nil || snap.NATGateway == nil {
		return Unavailable("no NAT pricing for region")
	}
	hourly := snap.NATGateway.PerHour * HoursPerMonth
	lines := []CostLine{{
		Component:  "NAT gateway hours",
		Amount:     fmt.Sprintf("730h × $%.4f/h", snap.NATGateway.PerHour),
		MonthlyUSD: hourly,
	}}
	notes := ""
	if in.ProcessedGB > 0 && snap.NATGateway.PerGBProcess > 0 {
		lines = append(lines, CostLine{
			Component:  "NAT gateway data processed",
			Amount:     fmt.Sprintf("%.1f GB × $%.4f/GB", in.ProcessedGB, snap.NATGateway.PerGBProcess),
			MonthlyUSD: in.ProcessedGB * snap.NATGateway.PerGBProcess,
		})
	} else {
		notes = "No data-processed signal — billing reflects hourly only."
	}
	return Sum(snapshotSource, Medium, notes, lines)
}

// ----------------- ELBv2 load balancer -----------------

// ELBv2LBInput is the per-LB cost computer's input.
type ELBv2LBInput struct {
	// Type is one of "application", "network", "gateway".
	Type string
	// LCUMonthly is the monthly Load Balancer Capacity Unit estimate.
	// Zero when no CloudWatch data is available — the computer then
	// bills hourly only and notes the missing component.
	LCUMonthly float64
}

// ELBv2LB computes the monthly cost of one ELBv2 load balancer.
func ELBv2LB(snap *pricing.Snapshot, in ELBv2LBInput) CostEstimate {
	if snap == nil {
		return Unavailable("no LB pricing for region")
	}
	t := strings.ToLower(in.Type)
	if t == "" {
		t = "application"
	}
	price, ok := snap.ELBv2LB[t]
	if !ok {
		return Unavailable(fmt.Sprintf("no pricing for LB type %q", in.Type))
	}
	lines := []CostLine{{
		Component:  fmt.Sprintf("%s LB hours", t),
		Amount:     fmt.Sprintf("730h × $%.4f/h", price.PerHour),
		MonthlyUSD: price.PerHour * HoursPerMonth,
	}}
	notes := ""
	if in.LCUMonthly > 0 && price.PerLCUHour > 0 {
		lines = append(lines, CostLine{
			Component:  fmt.Sprintf("%s LB LCUs", t),
			Amount:     fmt.Sprintf("%.1f LCU-h × $%.4f/LCU-h", in.LCUMonthly, price.PerLCUHour),
			MonthlyUSD: in.LCUMonthly * price.PerLCUHour,
		})
	} else {
		notes = "No LCU signal — billing reflects hourly only."
	}
	return Sum(snapshotSource, Medium, notes, lines)
}

// ----------------- RDS instance -----------------

// RDSDBInstanceInput is the per-RDS-instance cost computer's input.
type RDSDBInstanceInput struct {
	// Class is the RDS DB instance class ("db.t3.medium", ...).
	Class string
	// MultiAZ is true when the deployment is Multi-AZ.
	MultiAZ bool
	// AllocatedStorageGB is the provisioned storage in GB.
	AllocatedStorageGB int64
	// StorageType is one of "gp2", "gp3", "io1". Used to price
	// storage; default gp3 when empty.
	StorageType string
	// State is the RDS state ("available", "stopped", "deleting"). A
	// stopped instance still bills storage but not compute.
	State string
}

// RDSDBInstance computes the monthly cost of one RDS DB instance.
func RDSDBInstance(snap *pricing.Snapshot, in RDSDBInstanceInput) CostEstimate {
	if snap == nil {
		return Unavailable("no pricing snapshot for region")
	}
	price, ok := snap.RDSInstance[in.Class]
	if !ok {
		return Unavailable(fmt.Sprintf("no pricing for RDS class %q", in.Class))
	}
	scale := 1.0
	if in.MultiAZ && price.MultiAZScale > 0 {
		scale = price.MultiAZScale
	}
	var lines []CostLine
	notes := ""
	if strings.EqualFold(in.State, "stopped") {
		notes = "Instance stopped — no compute cost; storage still billed."
	} else {
		hours := price.PerHour * scale * HoursPerMonth
		multi := ""
		if in.MultiAZ {
			multi = " (multi-AZ)"
		}
		lines = append(lines, CostLine{
			Component:  fmt.Sprintf("RDS %s hours%s", in.Class, multi),
			Amount:     fmt.Sprintf("730h × $%.4f/h", price.PerHour*scale),
			MonthlyUSD: hours,
		})
	}
	if in.AllocatedStorageGB > 0 {
		stType := strings.ToLower(in.StorageType)
		if stType == "" {
			stType = "gp3"
		}
		if vp, ok := snap.EBSVolume[stType]; ok {
			lines = append(lines, CostLine{
				Component:  fmt.Sprintf("RDS %s storage", stType),
				Amount:     fmt.Sprintf("%d GB × $%.4f/GB-month", in.AllocatedStorageGB, vp.PerGBMonth),
				MonthlyUSD: float64(in.AllocatedStorageGB) * vp.PerGBMonth,
			})
		}
	}
	return Sum(snapshotSource, confidenceFromSource(snapshotSource), notes, lines)
}

// ----------------- RDS snapshot -----------------

// RDSDBSnapshotInput is the per-RDS-snapshot cost computer's input.
type RDSDBSnapshotInput struct {
	SizeGB int64
}

// RDSDBSnapshot computes the monthly cost of one RDS snapshot.
func RDSDBSnapshot(snap *pricing.Snapshot, in RDSDBSnapshotInput) CostEstimate {
	if snap == nil || snap.RDSSnapshot == nil {
		return Unavailable("no RDS snapshot pricing for region")
	}
	monthly := float64(in.SizeGB) * snap.RDSSnapshot.PerGBMonth
	lines := []CostLine{{
		Component:  "RDS snapshot",
		Amount:     fmt.Sprintf("%d GB × $%.4f/GB-month", in.SizeGB, snap.RDSSnapshot.PerGBMonth),
		MonthlyUSD: monthly,
	}}
	return Sum(snapshotSource, confidenceFromSource(snapshotSource), "", lines)
}

// ----------------- EFS file system -----------------

// EFSInput is the per-EFS cost computer's input.
type EFSInput struct {
	// StorageClass is one of "standard", "ia", "archive". Empty
	// defaults to "standard".
	StorageClass string
	// SizeGB is the storage size in GB. Zero when the scanner could
	// not read it.
	SizeGB float64
}

// EFS computes the monthly cost of one EFS file system.
func EFS(snap *pricing.Snapshot, in EFSInput) CostEstimate {
	if snap == nil {
		return Unavailable("no EFS pricing for region")
	}
	class := strings.ToLower(in.StorageClass)
	if class == "" {
		class = "standard"
	}
	price, ok := snap.EFS[class]
	if !ok {
		return Unavailable(fmt.Sprintf("no pricing for EFS class %q", class))
	}
	if in.SizeGB == 0 {
		return CostEstimate{
			Source:     snapshotSource,
			Confidence: Low,
			Notes:      "EFS size unknown — estimate not produced.",
		}
	}
	monthly := in.SizeGB * price.PerGBMonth
	lines := []CostLine{{
		Component:  fmt.Sprintf("EFS %s storage", class),
		Amount:     fmt.Sprintf("%.1f GB × $%.4f/GB-month", in.SizeGB, price.PerGBMonth),
		MonthlyUSD: monthly,
	}}
	return Sum(snapshotSource, Medium, "", lines)
}

// ----------------- CloudWatch Logs group -----------------

// LogsLogGroupInput is the per-log-group cost computer's input.
type LogsLogGroupInput struct {
	// StoredBytes is the current storedBytes value from
	// DescribeLogGroups. Zero when unknown.
	StoredBytes int64
	// RetentionDays is the retention setting; zero means never-expire
	// (the costliest configuration).
	RetentionDays int
}

// LogsLogGroup computes the monthly cost of one CloudWatch Logs group.
// Only storage is billed here — ingestion and scans live in the bill's
// account-level rollups and cannot be attributed to one group from a
// describe call alone.
func LogsLogGroup(snap *pricing.Snapshot, in LogsLogGroupInput) CostEstimate {
	if snap == nil || snap.CWLogs == nil {
		return Unavailable("no CW Logs pricing for region")
	}
	gb := float64(in.StoredBytes) / (1024 * 1024 * 1024)
	monthly := gb * snap.CWLogs.PerGBStored
	notes := ""
	if in.RetentionDays == 0 {
		notes = "Retention is never-expire — storage cost grows over time."
	}
	lines := []CostLine{{
		Component:  "CW Logs storage",
		Amount:     fmt.Sprintf("%.2f GB × $%.4f/GB-month", gb, snap.CWLogs.PerGBStored),
		MonthlyUSD: monthly,
	}}
	return Sum(snapshotSource, Medium, notes, lines)
}

// ----------------- ECR repository -----------------

// ECRRepositoryInput is the per-ECR-repo cost computer's input.
type ECRRepositoryInput struct {
	// ImageSizeBytesTotal is the total size of all images in the repo,
	// summed across DescribeImages output. Zero when unknown.
	ImageSizeBytesTotal int64
}

// ECRRepository computes the monthly cost of one ECR repository.
func ECRRepository(snap *pricing.Snapshot, in ECRRepositoryInput) CostEstimate {
	if snap == nil || snap.ECRStorage == nil {
		return Unavailable("no ECR pricing for region")
	}
	gb := float64(in.ImageSizeBytesTotal) / (1024 * 1024 * 1024)
	// 500 MB free tier per account; computer is per-repo so a small
	// repo may still incur cost when the account aggregate exceeds
	// the free tier — note that here.
	monthly := gb * snap.ECRStorage.PerGBMonth
	notes := "Account-level 500 MB free tier not subtracted."
	lines := []CostLine{{
		Component:  "ECR storage",
		Amount:     fmt.Sprintf("%.2f GB × $%.4f/GB-month", gb, snap.ECRStorage.PerGBMonth),
		MonthlyUSD: monthly,
	}}
	return Sum(snapshotSource, Medium, notes, lines)
}

// ----------------- S3 bucket -----------------

// S3BucketInput is the per-bucket cost computer's input.
type S3BucketInput struct {
	// StorageClass is the bucket's primary storage class. Defaults to
	// "standard" when empty.
	StorageClass string
	// SizeBytes is the bucket's size from CloudWatch BucketSizeBytes.
	// Zero when unknown.
	SizeBytes int64
}

// S3Bucket computes the monthly cost of one S3 bucket.
func S3Bucket(snap *pricing.Snapshot, in S3BucketInput) CostEstimate {
	if snap == nil {
		return Unavailable("no S3 pricing for region")
	}
	class := strings.ToLower(in.StorageClass)
	if class == "" {
		class = "standard"
	}
	price, ok := snap.S3Storage[class]
	if !ok {
		return Unavailable(fmt.Sprintf("no pricing for S3 class %q", class))
	}
	if in.SizeBytes == 0 {
		return CostEstimate{
			Source:     snapshotSource,
			Confidence: Low,
			Notes:      "Bucket size unknown — estimate not produced.",
		}
	}
	gb := float64(in.SizeBytes) / (1024 * 1024 * 1024)
	monthly := gb * price.PerGBMonth
	lines := []CostLine{{
		Component:  fmt.Sprintf("S3 %s storage", class),
		Amount:     fmt.Sprintf("%.2f GB × $%.4f/GB-month", gb, price.PerGBMonth),
		MonthlyUSD: monthly,
	}}
	return Sum(snapshotSource, Medium, "", lines)
}

// ----------------- CodePipeline -----------------

// CodePipelineInput is the per-pipeline cost computer's input.
type CodePipelineInput struct {
	// Active is true when the pipeline ran in the last 30 days.
	// Inactive pipelines are free per AWS pricing.
	Active bool
}

// CodePipeline computes the monthly cost of one CodePipeline.
func CodePipeline(snap *pricing.Snapshot, in CodePipelineInput) CostEstimate {
	if snap == nil || snap.CodePipeline == nil {
		return Unavailable("no CodePipeline pricing for region")
	}
	if !in.Active {
		return CostEstimate{
			Source:     snapshotSource,
			Confidence: High,
			Notes:      "Pipeline inactive in the last 30 days — no charge.",
		}
	}
	lines := []CostLine{{
		Component:  "CodePipeline active",
		Amount:     fmt.Sprintf("$%.2f/month", snap.CodePipeline.PerPipelineMonth),
		MonthlyUSD: snap.CodePipeline.PerPipelineMonth,
	}}
	return Sum(snapshotSource, confidenceFromSource(snapshotSource), "", lines)
}
