package perkind

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
	"github.com/bannaarr01/packwright/internal/audit/lastused/sources"
)

// ELBv2TargetGroupInput collects the per-TG facts the scanner already
// has. The HealthyTargets count is read from DescribeTargetHealth at
// scan time.
type ELBv2TargetGroupInput struct {
	// TargetGroupFullName is the suffix CloudWatch dimensions use, e.g.
	// "targetgroup/my-tg/73e2d6bc24d8a067".
	TargetGroupFullName string
	// LoadBalancerName is the LB the TG is attached to, used as the
	// LoadBalancer CloudWatch dimension. Empty when the TG is not
	// attached to any LB.
	LoadBalancerName string
	// HealthyTargets is the count of healthy targets at scan time. Zero
	// + no recent requests is the "orphan" signal.
	HealthyTargets int
	// LookbackDays overrides [lastused.DefaultLookbackDays] for the CW
	// RequestCount scan.
	LookbackDays int
	// Now is the reference time.
	Now time.Time
}

// ELBv2TargetGroup composes the ADR-0041 signals for an
// elbv2/target-group: per-TG RequestCount + healthy target count.
// A target group with no targets and no requests for ≥ 30 d is flagged
// as an orphan.
func ELBv2TargetGroup(ctx context.Context, m sources.MetricsClient, in ELBv2TargetGroupInput) lastused.LastUsedSignal {
	if in.LookbackDays == 0 {
		in.LookbackDays = lastused.DefaultLookbackDays
	}
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	lookback := lastused.Days(in.LookbackDays)

	dims := []sources.Dimension{{Name: "TargetGroup", Value: in.TargetGroupFullName}}
	if in.LoadBalancerName != "" {
		dims = append(dims, sources.Dimension{Name: "LoadBalancer", Value: in.LoadBalancerName})
	}
	srcs := []lastused.LastUsedSource{
		sources.Metric(ctx, "cw.request-count", m, sources.MetricQuery{
			Namespace:  "AWS/ApplicationELB",
			Metric:     "RequestCount",
			Dimensions: dims,
			Statistic:  "Sum",
			Lookback:   lookback,
			Period:     time.Hour,
		}),
	}

	rule := func(ss []lastused.LastUsedSource, best, now time.Time) (lastused.Confidence, string) {
		req := lastused.SourceByName(ss, "cw.request-count")
		hasRequests := req != nil && req.HasValue() && lastused.Within(*req.Value, now, lookback)

		switch {
		case in.HealthyTargets == 0 && !hasRequests:
			return lastused.Low, "No targets and no requests for ≥30 d — orphan target group."
		case hasRequests:
			return lastused.High, ""
		case in.HealthyTargets > 0:
			return lastused.Medium, "Targets healthy but no recent requests."
		default:
			return lastused.Low, "No recent activity."
		}
	}

	return lastused.Compose(srcs, rule, in.Now)
}
