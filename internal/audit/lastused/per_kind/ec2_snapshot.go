package perkind

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
	"github.com/bannaarr01/packwright/internal/audit/lastused/sources"
)

// AMIClient is the narrow EC2 surface [EC2Snapshot] uses to find the
// most-recent AMI whose block-device mapping references a snapshot.
// LatestAMIReferencing returns nil with nil error when no AMI references
// the snapshot.
type AMIClient interface {
	LatestAMIReferencing(ctx context.Context, snapshotID string) (*time.Time, error)
}

// EC2SnapshotInput collects the per-snapshot facts the scanner already
// has from DescribeSnapshots.
type EC2SnapshotInput struct {
	// SnapshotID is the snap-XXXXXXXX identifier.
	SnapshotID string
	// StartTime is the snapshot's StartTime.
	StartTime *time.Time
	// Now is the reference time for "within N days" confidence checks.
	Now time.Time
}

// EC2Snapshot composes the ADR-0041 signals for an ec2/snapshot: the
// snapshot's StartTime and the most-recent AMI CreationDate that
// references it. Confidence is High when a recent (< 90 d) AMI uses the
// snapshot, Low when no signal landed within 90 d.
func EC2Snapshot(ctx context.Context, a AMIClient, in EC2SnapshotInput) lastused.LastUsedSignal {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}

	srcs := []lastused.LastUsedSource{
		sources.Static("snapshot.start-time", in.StartTime),
		amiReferencingSource(ctx, a, in.SnapshotID),
	}

	rule := func(ss []lastused.LastUsedSource, best, now time.Time) (lastused.Confidence, string) {
		ami := lastused.SourceByName(ss, "ami.referencing")
		switch {
		case ami != nil && ami.HasValue() && lastused.Within(*ami.Value, now, lastused.Days(90)):
			return lastused.High, ""
		case best.IsZero():
			return lastused.Unknown, ""
		case !lastused.Within(best, now, lastused.Days(90)):
			return lastused.Low, "No AMI references this snapshot and it is > 90 d old — likely waste."
		default:
			return lastused.Medium, ""
		}
	}

	return lastused.Compose(srcs, rule, in.Now)
}

// amiReferencingSource calls the AMI client and turns the result into a
// LastUsedSource. Cost is 1 when the client is non-nil (one
// DescribeImages call).
func amiReferencingSource(ctx context.Context, a AMIClient, snapshotID string) lastused.LastUsedSource {
	src := lastused.LastUsedSource{Name: "ami.referencing"}
	if a == nil {
		return src
	}
	src.Cost = 1
	if t, err := a.LatestAMIReferencing(ctx, snapshotID); err == nil {
		src.Value = sources.CopyTimePtr(t)
	}
	return src
}
