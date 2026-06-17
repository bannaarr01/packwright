package perkind

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
	"github.com/bannaarr01/packwright/internal/audit/lastused/sources"
)

// EC2VolumeInput collects the per-volume facts the scanner has already
// extracted from DescribeVolumes.
type EC2VolumeInput struct {
	// VolumeID is the vol-XXXXXXXX identifier used as the CloudWatch
	// dimension.
	VolumeID string
	// State is the volume state string ("in-use", "available", ...).
	// "in-use" is treated as attached for confidence purposes.
	State string
	// AttachTime is the most-recent Attachment.AttachTime, or nil when
	// the volume is unattached.
	AttachTime *time.Time
	// CreateTime is the volume's CreateTime. nil when the scanner
	// couldn't read it.
	CreateTime *time.Time
	// LookbackDays overrides [lastused.DefaultLookbackDays] for the CW
	// VolumeWriteOps / VolumeReadOps scans.
	LookbackDays int
	// Now is the reference time for "within N days" confidence checks.
	Now time.Time
}

// EC2Volume composes the ADR-0041 signals for an ec2/volume: AttachTime
// (if attached), CreateTime, and the most-recent non-zero datapoint of
// VolumeWriteOps and VolumeReadOps. Confidence is High when the volume
// is attached and either IO metric is recent; Low (orphan flag) when
// the volume is available and has no IO for ≥ the lookback.
func EC2Volume(ctx context.Context, m sources.MetricsClient, in EC2VolumeInput) lastused.LastUsedSignal {
	if in.LookbackDays == 0 {
		in.LookbackDays = lastused.DefaultLookbackDays
	}
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	lookback := lastused.Days(in.LookbackDays)

	dim := []sources.Dimension{{Name: "VolumeId", Value: in.VolumeID}}
	srcs := []lastused.LastUsedSource{
		sources.Static("ebs.attach-time", in.AttachTime),
		sources.Static("ebs.create-time", in.CreateTime),
		sources.Metric(ctx, "cw.write-iops", m, sources.MetricQuery{
			Namespace: "AWS/EBS", Metric: "VolumeWriteOps",
			Dimensions: dim, Statistic: "Sum", Lookback: lookback, Period: time.Hour,
		}),
		sources.Metric(ctx, "cw.read-iops", m, sources.MetricQuery{
			Namespace: "AWS/EBS", Metric: "VolumeReadOps",
			Dimensions: dim, Statistic: "Sum", Lookback: lookback, Period: time.Hour,
		}),
	}

	rule := func(ss []lastused.LastUsedSource, best, now time.Time) (lastused.Confidence, string) {
		write := lastused.SourceByName(ss, "cw.write-iops")
		read := lastused.SourceByName(ss, "cw.read-iops")
		ioRecent := (write != nil && write.HasValue() && lastused.Within(*write.Value, now, lookback)) ||
			(read != nil && read.HasValue() && lastused.Within(*read.Value, now, lookback))
		attached := in.State == "in-use"

		switch {
		case attached && ioRecent:
			return lastused.High, ""
		case !attached && !ioRecent:
			return lastused.Low, "Available with no IO in lookback window — likely orphan."
		case attached:
			return lastused.Medium, "Attached but no IO recently — possibly idle."
		default:
			return lastused.Low, "Available volume — no recent activity."
		}
	}

	return lastused.Compose(srcs, rule, in.Now)
}
