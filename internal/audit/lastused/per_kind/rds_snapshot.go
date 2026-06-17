package perkind

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
	"github.com/bannaarr01/packwright/internal/audit/lastused/sources"
)

// RDSDBExistsClient is the narrow RDS surface [RDSDBSnapshot] uses to
// check whether the snapshot's source DB still exists. Implementations
// return false (with nil error) when DescribeDBInstances returns
// DBInstanceNotFound for the identifier.
type RDSDBExistsClient interface {
	DBInstanceExists(ctx context.Context, dbInstanceIdentifier string) (bool, error)
}

// RDSDBSnapshotInput collects the per-snapshot facts the scanner has
// from DescribeDBSnapshots.
type RDSDBSnapshotInput struct {
	// DBSnapshotIdentifier is the snapshot's identifier.
	DBSnapshotIdentifier string
	// SourceDBInstanceIdentifier is the snapshot's source DB. Empty
	// when the snapshot has no source DB recorded.
	SourceDBInstanceIdentifier string
	// SnapshotCreateTime is the snapshot's creation timestamp.
	SnapshotCreateTime *time.Time
	// Now is the reference time.
	Now time.Time
}

// RDSDBSnapshot composes the ADR-0041 signals for an rds/db-snapshot:
// SnapshotCreateTime + whether the source DB still exists. Snapshots
// whose source DB has been deleted and which are more than 90 days old
// are flagged as stale.
func RDSDBSnapshot(ctx context.Context, c RDSDBExistsClient, in RDSDBSnapshotInput) lastused.LastUsedSignal {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}

	exists := false
	cost := 0
	if c != nil && in.SourceDBInstanceIdentifier != "" {
		cost = 1
		if ok, err := c.DBInstanceExists(ctx, in.SourceDBInstanceIdentifier); err == nil {
			exists = ok
		}
	}

	srcs := []lastused.LastUsedSource{
		sources.Static("snapshot.create-time", in.SnapshotCreateTime),
		// Carries no timestamp but counts toward Cost — the rule reads
		// the captured `exists` variable.
		{Name: "rds.source-db-exists", Cost: cost},
	}

	rule := func(_ []lastused.LastUsedSource, best, now time.Time) (lastused.Confidence, string) {
		olderThan90 := !best.IsZero() && !lastused.Within(best, now, lastused.Days(90))
		switch {
		case best.IsZero():
			return lastused.Unknown, ""
		case !exists && olderThan90:
			return lastused.Low, "Source DB deleted and snapshot > 90 d — stale, candidate for deletion."
		case !exists:
			return lastused.Medium, "Source DB deleted; snapshot retained for recovery."
		case olderThan90:
			return lastused.Low, "Source DB present but snapshot > 90 d old."
		default:
			return lastused.Medium, ""
		}
	}

	return lastused.Compose(srcs, rule, in.Now)
}
