package perkind

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
	"github.com/bannaarr01/packwright/internal/audit/lastused/sources"
)

// EC2InstanceInput collects the per-instance facts the scanner has
// already extracted (instance ID, launch time, the ENIs attached) so
// the composer doesn't need to re-fetch the DescribeInstances payload.
type EC2InstanceInput struct {
	// InstanceID is the i-XXXXXXXX identifier used as the CloudWatch
	// dimension and the ENI lookup key.
	InstanceID string
	// LaunchTime is State.LaunchTime from DescribeInstances. nil when
	// the scanner couldn't read it.
	LaunchTime *time.Time
	// ENIIDs is every NetworkInterfaceId attached to the instance.
	// Empty when the scanner did not extract them; the ENI source is
	// then skipped.
	ENIIDs []string
	// LookbackDays overrides [lastused.DefaultLookbackDays] for the
	// CPU metric scan. Zero falls back to the default.
	LookbackDays int
	// Now is the reference time for "within N days" confidence checks.
	// Zero is replaced with time.Now() at call time.
	Now time.Time
}

// EC2Instance composes the ADR-0041 signals for an ec2/instance:
// LaunchTime (static), CPUUtilization (CW, lookback configurable), and
// the most-recent ENI status change. Confidence is High when CPU has a
// datapoint within the lookback, Medium when only the indirect signals
// landed, and Low when every signal is older than 90 d.
func EC2Instance(ctx context.Context, m sources.MetricsClient, e sources.ENIClient, in EC2InstanceInput) lastused.LastUsedSignal {
	if in.LookbackDays == 0 {
		in.LookbackDays = lastused.DefaultLookbackDays
	}
	if in.Now.IsZero() {
		in.Now = time.Now()
	}

	srcs := []lastused.LastUsedSource{
		sources.Static("ec2.launch-time", in.LaunchTime),
		sources.Metric(ctx, "cw.cpu", m, sources.MetricQuery{
			Namespace:  "AWS/EC2",
			Metric:     "CPUUtilization",
			Dimensions: []sources.Dimension{{Name: "InstanceId", Value: in.InstanceID}},
			Statistic:  "Maximum",
			Lookback:   lastused.Days(in.LookbackDays),
			Period:     time.Hour,
		}),
		sources.ENILastStatusChange(ctx, "eni.status", e, in.ENIIDs),
	}

	rule := func(ss []lastused.LastUsedSource, best, now time.Time) (lastused.Confidence, string) {
		cpu := lastused.SourceByName(ss, "cw.cpu")
		if cpu != nil && cpu.HasValue() && lastused.Within(*cpu.Value, now, lastused.Days(in.LookbackDays)) {
			return lastused.High, ""
		}
		if best.IsZero() {
			return lastused.Unknown, ""
		}
		if !lastused.Within(best, now, lastused.Days(90)) {
			return lastused.Low, "No signals within the last 90 d — likely idle."
		}
		return lastused.Medium, ""
	}

	return lastused.Compose(srcs, rule, in.Now)
}
