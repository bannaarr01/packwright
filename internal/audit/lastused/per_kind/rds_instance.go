package perkind

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
	"github.com/bannaarr01/packwright/internal/audit/lastused/sources"
)

// RDSDBInstanceInput collects the per-instance facts the scanner has
// from DescribeDBInstances.
type RDSDBInstanceInput struct {
	// DBInstanceIdentifier is the user-facing identifier used as the
	// CloudWatch dimension.
	DBInstanceIdentifier string
	// LatestRestorableTime is the LatestRestorableTime field. Not a
	// usage signal per se, but a reasonable lower bound on "the engine
	// is still doing work."
	LatestRestorableTime *time.Time
	// LookbackDays overrides [lastused.DefaultLookbackDays] for the CW
	// scans.
	LookbackDays int
	// Now is the reference time.
	Now time.Time
}

// RDSDBInstance composes the ADR-0041 signals for an rds/db-instance:
// CW DatabaseConnections, CW CPUUtilization, and LatestRestorableTime.
// Confidence is High when connections landed recently, Low when every
// metric is zero across the lookback window.
func RDSDBInstance(ctx context.Context, m sources.MetricsClient, in RDSDBInstanceInput) lastused.LastUsedSignal {
	if in.LookbackDays == 0 {
		in.LookbackDays = lastused.DefaultLookbackDays
	}
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	lookback := lastused.Days(in.LookbackDays)

	dim := []sources.Dimension{{Name: "DBInstanceIdentifier", Value: in.DBInstanceIdentifier}}
	srcs := []lastused.LastUsedSource{
		sources.Static("rds.latest-restorable", in.LatestRestorableTime),
		sources.Metric(ctx, "cw.connections", m, sources.MetricQuery{
			Namespace: "AWS/RDS", Metric: "DatabaseConnections",
			Dimensions: dim, Statistic: "Sum", Lookback: lookback, Period: time.Hour,
		}),
		sources.Metric(ctx, "cw.cpu", m, sources.MetricQuery{
			Namespace: "AWS/RDS", Metric: "CPUUtilization",
			Dimensions: dim, Statistic: "Maximum", Lookback: lookback, Period: time.Hour,
		}),
	}

	rule := func(ss []lastused.LastUsedSource, best, now time.Time) (lastused.Confidence, string) {
		conns := lastused.SourceByName(ss, "cw.connections")
		if conns != nil && conns.HasValue() && lastused.Within(*conns.Value, now, lookback) {
			return lastused.High, ""
		}
		cpu := lastused.SourceByName(ss, "cw.cpu")
		hasCPU := cpu != nil && cpu.HasValue() && lastused.Within(*cpu.Value, now, lookback)
		hasConns := conns != nil && conns.HasValue()

		switch {
		case !hasConns && !hasCPU:
			return lastused.Low, "No connections or CPU activity for ≥30 d — likely idle."
		case best.IsZero():
			return lastused.Unknown, ""
		default:
			return lastused.Medium, "Indirect signals only — no recent connections."
		}
	}

	return lastused.Compose(srcs, rule, in.Now)
}
