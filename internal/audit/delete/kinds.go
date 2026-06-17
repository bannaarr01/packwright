package delete

import (
	"fmt"
	"time"
)

// Kind names an AWS resource type this package can stage and delete.
// The string form matches ADR-0043's namespaced spelling — e.g.
// "ec2/volume" — and is the same identifier surfaced to the LLM via
// the audit/delete-* tool catalogue names ("audit/delete-volume" for
// KindEC2Volume, and so on).
//
// Per ADR-0043 the set is closed: anything not enumerated here is
// out of scope for MVP 6 by design. RDS DB instances, S3 buckets,
// and KMS keys are explicitly excluded and require manual action via
// the AWS Console.
type Kind string

// Supported deletion kinds, mirroring ADR-0043's table.
const (
	// KindEC2Volume is an EBS volume. Backing API: ec2:DeleteVolume.
	KindEC2Volume Kind = "ec2/volume"
	// KindEC2Snapshot is an EBS snapshot. Backing API: ec2:DeleteSnapshot.
	KindEC2Snapshot Kind = "ec2/snapshot"
	// KindEC2EIP is an Elastic IP. Backing API: ec2:ReleaseAddress.
	KindEC2EIP Kind = "ec2/eip"
	// KindEC2NATGateway is a NAT gateway. Backing API: ec2:DeleteNatGateway.
	KindEC2NATGateway Kind = "ec2/nat-gateway"
	// KindELBv2TargetGroup is an ELBv2 target group.
	// Backing API: elasticloadbalancing:DeleteTargetGroup.
	KindELBv2TargetGroup Kind = "elbv2/target-group"
	// KindLogsLogGroup is a CloudWatch Logs log group.
	// Backing API: logs:DeleteLogGroup.
	KindLogsLogGroup Kind = "logs/log-group"
	// KindRDSDBSnapshot is an RDS DB snapshot.
	// Backing API: rds:DeleteDBSnapshot.
	KindRDSDBSnapshot Kind = "rds/db-snapshot"
	// KindECRImage is a single ECR image (repository + image digest).
	// Backing API: ecr:BatchDeleteImage.
	KindECRImage Kind = "ecr/image"
)

// AllKinds returns every Kind this package supports, in stable order.
func AllKinds() []Kind {
	return []Kind{
		KindEC2Volume,
		KindEC2Snapshot,
		KindEC2EIP,
		KindEC2NATGateway,
		KindELBv2TargetGroup,
		KindLogsLogGroup,
		KindRDSDBSnapshot,
		KindECRImage,
	}
}

// IsKnown reports whether k is one of the supported deletion kinds.
// A false return is the data-layer guard that prevents callers from
// staging a kind ADR-0043 explicitly excludes (RDS DB instances, S3
// buckets, KMS keys, anything else).
func IsKnown(k Kind) bool {
	for _, known := range AllKinds() {
		if k == known {
			return true
		}
	}
	return false
}

// Resource identifies a single AWS object that may be staged for
// deletion. Identifier is the primary AWS handle for the resource
// (volume id, snapshot id, allocation id, target-group ARN, log
// group name, db-snapshot identifier, image digest); Extra carries
// the additional fields a Kind needs beyond Identifier (e.g. the
// repository name for an ECR image).
//
// Display, EstimatedMonthlyUSD, and IdleDays are UI-only metadata
// produced by the scanner (MVP-6 PR-01 / PR-03) and surfaced
// verbatim in the consent modal; the executor does not consult
// them.
type Resource struct {
	// Kind is the AWS resource type (KindEC2Volume, ...).
	Kind Kind `json:"kind"`
	// Identifier is the canonical AWS handle (vol-..., snap-...,
	// arn:aws:elasticloadbalancing:..., log-group-name, etc.).
	Identifier string `json:"identifier"`
	// Region is the AWS region the resource lives in.
	Region string `json:"region,omitempty"`
	// AccountID is the AWS account that owns the resource.
	AccountID string `json:"account_id,omitempty"`
	// Profile is the AWS CLI profile that resolves to AccountID.
	Profile string `json:"profile,omitempty"`

	// Display is a short human-readable label rendered in the
	// consent modal. Filled by the scanner.
	Display string `json:"display,omitempty"`
	// EstimatedMonthlyUSD is the cost saving the scanner attributed
	// to this resource. Zero when unknown.
	EstimatedMonthlyUSD float64 `json:"estimated_monthly_usd,omitempty"`
	// IdleDays is the number of days the scanner believes this
	// resource has been idle. Zero when unknown.
	IdleDays int `json:"idle_days,omitempty"`

	// Extra carries kind-specific fields not covered above:
	//   ecr/image:       {"repository_name": "...", "image_digest": "..."}
	//   ec2/eip:         {"allocation_id": "..." (alias for Identifier)}
	// Keys are documented per-kind by the dependency probe and the
	// delete action; consumers should treat unknown keys as opaque.
	Extra map[string]string `json:"extra,omitempty"`
}

// Validate reports the first problem in r (unknown kind, empty
// identifier, missing required Extra key). The tray refuses to
// store an invalid Resource so a malformed row can never reach the
// executor.
func (r Resource) Validate() error {
	if !IsKnown(r.Kind) {
		return fmt.Errorf("unknown kind %q (must be one of %v)", r.Kind, AllKinds())
	}
	if r.Identifier == "" {
		return fmt.Errorf("identifier is required")
	}
	if r.Kind == KindECRImage {
		if r.Extra["repository_name"] == "" {
			return fmt.Errorf("ecr/image: extra.repository_name is required")
		}
	}
	return nil
}

// Row is a single staged entry in the tray. Note is free-text the
// user can add to remind themselves why this row is in the list.
type Row struct {
	// ID is a stable, opaque identifier — set by Tray.Add. Used to
	// reference the row across the consent and executor steps.
	ID string `json:"id"`
	// Resource is the AWS object the row points at.
	Resource Resource `json:"resource"`
	// AddedAt is the time the user staged the row, in UTC.
	AddedAt time.Time `json:"added_at"`
	// Note is free-text the user attached on add.
	Note string `json:"note,omitempty"`
	// DependsOn is the IDs of OTHER rows in the same tray that
	// must be deleted before this one. The scanner / dep probe can
	// fill this so the executor orders the batch correctly.
	DependsOn []string `json:"depends_on,omitempty"`
}
