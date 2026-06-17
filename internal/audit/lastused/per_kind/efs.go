package perkind

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
	"github.com/bannaarr01/packwright/internal/audit/lastused/sources"
)

// EFSFileSystemInput collects the per-FS facts the scanner has from
// DescribeFileSystems.
type EFSFileSystemInput struct {
	// FileSystemID is the fs-XXXXXXXX identifier used as the
	// CloudWatch dimension.
	FileSystemID string
	// LastModifiedTime is the FS's LifeCycleState change timestamp;
	// the closest thing EFS exposes to a "last modified" attribute on
	// the file system itself.
	LastModifiedTime *time.Time
	// LookbackDays overrides [lastused.DefaultLookbackDays] for CW
	// scans.
	LookbackDays int
	// Now is the reference time.
	Now time.Time
}

// EFSFileSystem composes the ADR-0041 signals for an efs/file-system:
// LastModifiedTime, CW TotalIOBytes, and CW ClientConnections.
// Confidence is High when IO landed in the lookback, Low when both
// metrics are zero for ≥ 30 d.
func EFSFileSystem(ctx context.Context, m sources.MetricsClient, in EFSFileSystemInput) lastused.LastUsedSignal {
	if in.LookbackDays == 0 {
		in.LookbackDays = lastused.DefaultLookbackDays
	}
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	lookback := lastused.Days(in.LookbackDays)

	dim := []sources.Dimension{{Name: "FileSystemId", Value: in.FileSystemID}}
	srcs := []lastused.LastUsedSource{
		sources.Static("efs.last-modified", in.LastModifiedTime),
		sources.Metric(ctx, "cw.io-bytes", m, sources.MetricQuery{
			Namespace: "AWS/EFS", Metric: "TotalIOBytes",
			Dimensions: dim, Statistic: "Sum", Lookback: lookback, Period: time.Hour,
		}),
		sources.Metric(ctx, "cw.client-conns", m, sources.MetricQuery{
			Namespace: "AWS/EFS", Metric: "ClientConnections",
			Dimensions: dim, Statistic: "Maximum", Lookback: lookback, Period: time.Hour,
		}),
	}

	rule := func(ss []lastused.LastUsedSource, best, now time.Time) (lastused.Confidence, string) {
		io := lastused.SourceByName(ss, "cw.io-bytes")
		conns := lastused.SourceByName(ss, "cw.client-conns")
		ioRecent := io != nil && io.HasValue() && lastused.Within(*io.Value, now, lookback)
		connsRecent := conns != nil && conns.HasValue() && lastused.Within(*conns.Value, now, lookback)

		switch {
		case ioRecent || connsRecent:
			return lastused.High, ""
		case best.IsZero():
			return lastused.Unknown, ""
		case (io == nil || !io.HasValue()) && (conns == nil || !conns.HasValue()):
			return lastused.Low, "Zero IO and zero client connections for ≥30 d — likely idle."
		default:
			return lastused.Medium, "Indirect signals only — no recent IO or connections."
		}
	}

	return lastused.Compose(srcs, rule, in.Now)
}
