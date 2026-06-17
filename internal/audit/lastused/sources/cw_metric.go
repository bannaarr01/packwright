package sources

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

// Dimension is one CloudWatch metric dimension. Mirrors the SDK's
// cloudwatchtypes.Dimension but lives here so this package stays free
// of the AWS SDK.
type Dimension struct {
	Name  string
	Value string
}

// MetricQuery describes a CloudWatch metric scan: which metric, which
// resource, how far back, and the coarsest period acceptable. ADR-0041
// prefers GetMetricStatistics with a coarse period over GetMetricData
// because the audit only cares about "was there ever non-zero activity
// in the window."
type MetricQuery struct {
	// Namespace is the CloudWatch namespace, e.g. "AWS/EC2".
	Namespace string
	// Metric is the metric name, e.g. "CPUUtilization".
	Metric string
	// Dimensions narrows the query to a single resource.
	Dimensions []Dimension
	// Statistic is the CloudWatch statistic, e.g. "Sum" or "Maximum".
	// Defaults to "Maximum" when empty.
	Statistic string
	// Lookback is how far back the source should query.
	Lookback time.Duration
	// Period is the bucket period. Defaults to one hour when zero.
	Period time.Duration
}

// MetricsClient is the narrow CloudWatch surface [Metric] uses.
//
// LastNonZero returns the timestamp of the most-recent datapoint with a
// value greater than zero in the window described by q. A nil return
// (with nil error) means "no non-zero datapoint in window" — a normal
// outcome, not an error. The implementation may use GetMetricStatistics
// or GetMetricData; ADR-0041 prefers the former for cost.
type MetricsClient interface {
	LastNonZero(ctx context.Context, q MetricQuery) (*time.Time, error)
}

// Metric runs the query and returns it as a [lastused.LastUsedSource]
// named n. Errors from the client are swallowed and result in a nil
// Value, mirroring the "no datapoint" outcome — a partial scan should
// not fail the whole composer. The caller is expected to log via the
// client implementation if they want to surface metric-fetch errors.
//
// Cost is 1 (one GetMetricStatistics call).
func Metric(ctx context.Context, n string, c MetricsClient, q MetricQuery) lastused.LastUsedSource {
	src := lastused.LastUsedSource{
		Name:         n,
		LookbackDays: int(q.Lookback / (24 * time.Hour)),
		Cost:         1,
	}
	if c == nil {
		return src
	}
	if t, err := c.LastNonZero(ctx, q); err == nil {
		src.Value = CopyTimePtr(t)
	}
	return src
}
