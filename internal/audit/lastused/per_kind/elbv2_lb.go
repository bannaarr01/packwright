package perkind

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
	"github.com/bannaarr01/packwright/internal/audit/lastused/sources"
)

// ELBv2AccessLogsClient is the narrow surface [ELBv2LoadBalancer] uses
// when access logs are enabled for the load balancer. LatestAccessLog
// returns the LastModified of the newest object under bucket/prefix,
// or nil with nil error when no objects exist in window.
type ELBv2AccessLogsClient interface {
	LatestAccessLog(ctx context.Context, bucket, prefix string) (*time.Time, error)
}

// ELBv2LoadBalancerInput collects the per-LB facts the scanner already
// has from DescribeLoadBalancers + DescribeLoadBalancerAttributes.
type ELBv2LoadBalancerInput struct {
	// LoadBalancerName is the suffix CloudWatch dimensions use, e.g.
	// "app/my-lb/50dc6c495c0c9188".
	LoadBalancerName string
	// AccessLogsBucket and AccessLogsPrefix come from the LB's
	// attributes. AccessLogsBucket empty means access logs are not
	// enabled — the S3 source is skipped, no AWS call is made.
	AccessLogsBucket string
	AccessLogsPrefix string
	// LookbackDays overrides [lastused.DefaultLookbackDays] for the CW
	// RequestCount scan.
	LookbackDays int
	// Now is the reference time.
	Now time.Time
}

// ELBv2LoadBalancer composes the ADR-0041 signals for an
// elbv2/load-balancer: CloudWatch RequestCount and, when enabled, the
// most-recent access-log object on S3. Confidence is High when requests
// landed in the lookback window, Low otherwise.
func ELBv2LoadBalancer(ctx context.Context, m sources.MetricsClient, a ELBv2AccessLogsClient, in ELBv2LoadBalancerInput) lastused.LastUsedSignal {
	if in.LookbackDays == 0 {
		in.LookbackDays = lastused.DefaultLookbackDays
	}
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	lookback := lastused.Days(in.LookbackDays)

	srcs := []lastused.LastUsedSource{
		sources.Metric(ctx, "cw.request-count", m, sources.MetricQuery{
			Namespace: "AWS/ApplicationELB",
			Metric:    "RequestCount",
			Dimensions: []sources.Dimension{
				{Name: "LoadBalancer", Value: in.LoadBalancerName},
			},
			Statistic: "Sum",
			Lookback:  lookback,
			Period:    time.Hour,
		}),
		accessLogsSource(ctx, a, in.AccessLogsBucket, in.AccessLogsPrefix),
	}

	rule := func(ss []lastused.LastUsedSource, best, now time.Time) (lastused.Confidence, string) {
		req := lastused.SourceByName(ss, "cw.request-count")
		if req != nil && req.HasValue() && lastused.Within(*req.Value, now, lookback) {
			return lastused.High, ""
		}
		if best.IsZero() {
			return lastused.Low, "No requests in the lookback window — likely idle."
		}
		return lastused.Medium, "No recent requests, but other signals exist."
	}

	return lastused.Compose(srcs, rule, in.Now)
}

// accessLogsSource returns the LB's access-log timestamp source.
// When bucket is empty the source is "we did not look" (nil value,
// zero cost) — this is the normal case for LBs that don't enable
// access logging.
func accessLogsSource(ctx context.Context, a ELBv2AccessLogsClient, bucket, prefix string) lastused.LastUsedSource {
	src := lastused.LastUsedSource{Name: "elb.access-log"}
	if bucket == "" || a == nil {
		return src
	}
	src.Cost = 1
	if t, err := a.LatestAccessLog(ctx, bucket, prefix); err == nil {
		src.Value = sources.CopyTimePtr(t)
	}
	return src
}
