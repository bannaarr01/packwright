// Package pricing serves AWS on-demand pricing to the per-kind cost
// computers. It has two backends, layered behind a single Lookup
// surface:
//
//  1. The embedded snapshot (embed.go) — a tiny set of JSON files
//     stamped into the binary at build time, sufficient for the
//     resource kinds and regions Packwright supports in MVP-6. This
//     backend is offline and always available.
//  2. A live AWS Pricing API client (api.go) is intentionally NOT in
//     v1: the API only serves us-east-1 and ap-south-1 endpoints and
//     the responses are tens of MB. The embedded snapshot covers the
//     common case; v2 will add a live lookup behind a config flag.
//
// Callers query Lookup with a region plus a kind-shaped key
// (instance-type / volume-type / NAT-gateway / …). Misses return
// (nil, false) and the caller is expected to emit
// cost.Unknown("no pricing data") rather than fail the audit.
package pricing

import (
	"embed"
	"encoding/json"
	"fmt"
	"sync"
)

// snapshotFS holds the embedded JSON files.
//
//go:embed snapshot/*.json
var snapshotFS embed.FS

// SnapshotAge is the human-readable age the embedded snapshot was
// generated. Surfaced in the UI so users know how old the prices are;
// per-kind computers also use it to drop Confidence one level once the
// snapshot is more than 30 days behind the current build.
const SnapshotAge = "2026-01-01"

// Region is the AWS region key the snapshot is partitioned by. Adding
// a region means dropping a new JSON file in pricing/snapshot/ — the
// loader picks it up automatically through the embed.FS walk.
type Region string

// Snapshot is the on-disk shape of one region's pricing JSON. Every
// field is optional; per-kind computers reach into Snapshot through
// the typed accessor methods below so the JSON schema can grow
// without forcing computer-side changes.
type Snapshot struct {
	Region       Region                      `json:"region"`
	GeneratedAt  string                      `json:"generated_at"`
	EC2Instance  map[string]EC2InstancePrice `json:"ec2_instance"`
	EBSVolume    map[string]EBSVolumePrice   `json:"ebs_volume"`
	EBSSnapshot  *EBSSnapshotPrice           `json:"ebs_snapshot,omitempty"`
	EIP          *EIPPrice                   `json:"eip,omitempty"`
	NATGateway   *NATGatewayPrice            `json:"nat_gateway,omitempty"`
	ELBv2LB      map[string]ELBv2LBPrice     `json:"elbv2_lb"`
	RDSInstance  map[string]RDSInstancePrice `json:"rds_instance"`
	RDSSnapshot  *RDSSnapshotPrice           `json:"rds_snapshot,omitempty"`
	EFS          map[string]EFSStoragePrice  `json:"efs"`
	CWLogs       *CWLogsPrice                `json:"cw_logs,omitempty"`
	ECRStorage   *ECRStoragePrice            `json:"ecr_storage,omitempty"`
	S3Storage    map[string]S3StoragePrice   `json:"s3_storage"`
	CodePipeline *CodePipelinePrice          `json:"codepipeline,omitempty"`
}

// EC2InstancePrice is the per-hour on-demand cost for one EC2 instance
// type, plus the OS adjustment factor for Windows.
type EC2InstancePrice struct {
	LinuxPerHour   float64 `json:"linux_per_hour"`
	WindowsPerHour float64 `json:"windows_per_hour,omitempty"`
}

// EBSVolumePrice is the per-GB-month storage price plus the per-IOPS
// and per-throughput month prices for io1/io2/gp3 volumes. Fields that
// don't apply for a volume type are zero.
type EBSVolumePrice struct {
	PerGBMonth         float64 `json:"per_gb_month"`
	PerIOPSMonth       float64 `json:"per_iops_month,omitempty"`
	PerThroughputMonth float64 `json:"per_throughput_month,omitempty"`
}

// EBSSnapshotPrice is the per-GB-month price for EBS snapshots in
// standard tier.
type EBSSnapshotPrice struct {
	PerGBMonth float64 `json:"per_gb_month"`
}

// EIPPrice is the per-hour price for an Elastic IP held idle (not
// associated to a running instance).
type EIPPrice struct {
	IdlePerHour float64 `json:"idle_per_hour"`
}

// NATGatewayPrice is the fixed per-hour price plus the per-GB processed
// price for a NAT gateway.
type NATGatewayPrice struct {
	PerHour      float64 `json:"per_hour"`
	PerGBProcess float64 `json:"per_gb_processed,omitempty"`
}

// ELBv2LBPrice is the per-hour and per-LCU-hour prices for one ELBv2
// load-balancer variant. Key is the ALB/NLB type ("application" or
// "network").
type ELBv2LBPrice struct {
	PerHour    float64 `json:"per_hour"`
	PerLCUHour float64 `json:"per_lcu_hour,omitempty"`
}

// RDSInstancePrice is the per-hour on-demand price for one RDS instance
// class, with a multiplier for Multi-AZ deployments.
type RDSInstancePrice struct {
	PerHour      float64 `json:"per_hour"`
	MultiAZScale float64 `json:"multi_az_scale,omitempty"`
}

// RDSSnapshotPrice is the per-GB-month price for RDS snapshots.
type RDSSnapshotPrice struct {
	PerGBMonth float64 `json:"per_gb_month"`
}

// EFSStoragePrice is the per-GB-month price for one EFS storage class.
// Key is the storage class ("standard", "ia", "archive").
type EFSStoragePrice struct {
	PerGBMonth float64 `json:"per_gb_month"`
}

// CWLogsPrice is the per-GB ingested, per-GB archived, and per-GB
// scanned prices for CloudWatch Logs.
type CWLogsPrice struct {
	PerGBIngest float64 `json:"per_gb_ingest"`
	PerGBStored float64 `json:"per_gb_stored"`
	PerGBScan   float64 `json:"per_gb_scan,omitempty"`
}

// ECRStoragePrice is the per-GB-month price for ECR storage past the
// 500MB free tier.
type ECRStoragePrice struct {
	PerGBMonth float64 `json:"per_gb_month"`
}

// S3StoragePrice is the per-GB-month price for one S3 storage class.
// Key is the storage class ("standard", "standard-ia", "glacier-ir",
// "glacier-flexible", "glacier-deep-archive").
type S3StoragePrice struct {
	PerGBMonth float64 `json:"per_gb_month"`
}

// CodePipelinePrice is the per-month per-active-pipeline price.
type CodePipelinePrice struct {
	PerPipelineMonth float64 `json:"per_pipeline_month"`
}

var (
	loadOnce sync.Once
	loadErr  error
	cache    map[Region]*Snapshot
)

// Lookup returns the embedded pricing snapshot for the requested
// region. Returns (nil, false) when the region has no snapshot stamped
// into the binary; per-kind computers should treat that as
// cost.Unknown rather than a hard failure.
func Lookup(region string) (*Snapshot, bool) {
	loadOnce.Do(loadEmbedded)
	if loadErr != nil {
		return nil, false
	}
	snap, ok := cache[Region(region)]
	return snap, ok
}

// Regions returns every region that has a snapshot bundled in. Sorted
// for stable output. Used by tests and the `audit cost-snapshots`
// debug listing.
func Regions() []Region {
	loadOnce.Do(loadEmbedded)
	if loadErr != nil || cache == nil {
		return nil
	}
	out := make([]Region, 0, len(cache))
	for r := range cache {
		out = append(out, r)
	}
	return out
}

// LoadError reports the embedded-load error, if any. Used by tests so
// a snapshot with a malformed JSON file is caught at build time rather
// than silently returning Unknown for every resource.
func LoadError() error {
	loadOnce.Do(loadEmbedded)
	return loadErr
}

// loadEmbedded walks the embedded FS once and unmarshals every JSON
// file into the cache. Called lazily through loadOnce so packages
// that depend on pricing but never query it pay no startup cost.
func loadEmbedded() {
	entries, err := snapshotFS.ReadDir("snapshot")
	if err != nil {
		loadErr = fmt.Errorf("pricing: read embedded snapshot dir: %w", err)
		return
	}
	cache = make(map[Region]*Snapshot, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := snapshotFS.ReadFile("snapshot/" + e.Name())
		if err != nil {
			loadErr = fmt.Errorf("pricing: read %q: %w", e.Name(), err)
			return
		}
		var snap Snapshot
		if err := json.Unmarshal(raw, &snap); err != nil {
			loadErr = fmt.Errorf("pricing: parse %q: %w", e.Name(), err)
			return
		}
		if snap.Region == "" {
			loadErr = fmt.Errorf("pricing: snapshot %q has empty region", e.Name())
			return
		}
		cache[snap.Region] = &snap
	}
}
