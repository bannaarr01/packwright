package monitorx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

// cwMetric implements the "cloudwatch/metric" panel kind. The YAML shape is:
//
//	kind: cloudwatch/metric
//	spec:
//	  namespace: AWS/EC2
//	  metric: CPUUtilization
//	  statistic: Average             # Average | Sum | Maximum | Minimum | SampleCount
//	  period: 60                     # seconds; CW enforces a 60s minimum for fine-grained metrics
//	  lookback: 1h                   # any time.Duration string
//	  unit: Percent                  # optional; rendered on the chart axis
//	  dimensions:                    # optional; CW dimensions filter
//	    InstanceId: i-0123456789abcdef0
//
// Validate captures the spec into the receiver's fields; Refresh issues a
// single GetMetricData call covering the configured lookback window.
type cwMetric struct {
	namespace  string
	metric     string
	statistic  string
	unit       string
	period     int32
	lookback   time.Duration
	dimensions []cwtypes.Dimension
}

func init() {
	Register("cloudwatch/metric", func() Panel { return &cwMetric{} })
}

// Kind reports the registered panel kind.
func (c *cwMetric) Kind() string { return "cloudwatch/metric" }

// Validate captures spec into the receiver. Errors here surface at manifest
// load time, so they spell out the missing or malformed key.
func (c *cwMetric) Validate(spec map[string]any) error {
	ns, err := requireString(spec, "namespace")
	if err != nil {
		return err
	}
	metric, err := requireString(spec, "metric")
	if err != nil {
		return err
	}
	stat, err := requireString(spec, "statistic")
	if err != nil {
		return err
	}
	if !validStatistic(stat) {
		return fmt.Errorf("statistic %q is not a CloudWatch statistic", stat)
	}

	period, err := optionalInt(spec, "period", 60)
	if err != nil {
		return err
	}
	if period < 1 {
		return fmt.Errorf("period must be a positive number of seconds (got %d)", period)
	}

	lookback, err := requireDuration(spec, "lookback")
	if err != nil {
		return err
	}
	if lookback <= 0 {
		return errors.New("lookback must be positive")
	}

	dims, err := parseDimensions(spec)
	if err != nil {
		return err
	}

	c.namespace = ns
	c.metric = metric
	c.statistic = stat
	c.unit, _ = spec["unit"].(string)
	c.period = int32(period)
	c.lookback = lookback
	c.dimensions = dims
	return nil
}

// Refresh issues a single GetMetricData query for the configured window and
// returns one SeriesData with one Series. CloudWatch returns timestamps and
// values in descending order; we sort ascending so the chart renderer can
// stream the points left-to-right without re-sorting.
func (c *cwMetric) Refresh(ctx context.Context, deps Deps) (PanelData, error) {
	if deps.Metrics == nil {
		return nil, errors.New("cloudwatch/metric: Deps.Metrics is nil")
	}
	now := nowFunc(deps)
	start := now.Add(-c.lookback)
	id := "m1"

	in := &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(start),
		EndTime:   aws.Time(now),
		MetricDataQueries: []cwtypes.MetricDataQuery{{
			Id: aws.String(id),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace:  aws.String(c.namespace),
					MetricName: aws.String(c.metric),
					Dimensions: c.dimensions,
				},
				Period: aws.Int32(c.period),
				Stat:   aws.String(c.statistic),
			},
			ReturnData: aws.Bool(true),
		}},
		ScanBy: cwtypes.ScanByTimestampAscending,
	}

	out, err := deps.Metrics.GetMetricData(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("cloudwatch/metric: GetMetricData: %w", err)
	}

	pts, err := pointsFromResult(out.MetricDataResults, id, deps.Log)
	if err != nil {
		return nil, fmt.Errorf("cloudwatch/metric: %w", err)
	}
	return SeriesData{Series: []Series{{
		Label:  labelFor(c.metric, c.dimensions),
		Unit:   c.unit,
		Points: pts,
	}}}, nil
}

// pointsFromResult flattens the result with the matching Id into ascending
// time order. CloudWatch may return timestamps in either order depending on
// ScanBy; we sort unconditionally to keep the renderer's life simple. A
// missing matched result or a Forbidden / InternalError status surface as
// errors so the renderer paints the error card rather than a silently-blank
// chart; PartialData is logged but allowed through.
func pointsFromResult(results []cwtypes.MetricDataResult, id string, log *slog.Logger) ([]Point, error) {
	for _, r := range results {
		if aws.ToString(r.Id) != id {
			continue
		}
		switch r.StatusCode {
		case cwtypes.StatusCodeForbidden:
			return nil, fmt.Errorf("query %q: access denied", id)
		case cwtypes.StatusCodeInternalError:
			return nil, fmt.Errorf("query %q: CloudWatch internal error", id)
		case cwtypes.StatusCodePartialData:
			if log != nil {
				log.Debug("cloudwatch/metric: partial data; consider increasing period or shortening lookback",
					"query", id)
			}
		}
		pts := make([]Point, len(r.Timestamps))
		for i, t := range r.Timestamps {
			pts[i] = Point{Time: t, Value: r.Values[i]}
		}
		sort.Slice(pts, func(i, j int) bool { return pts[i].Time.Before(pts[j].Time) })
		return pts, nil
	}
	return nil, fmt.Errorf("query %q: no matching result returned", id)
}

// labelFor builds a human-readable series label. Without dimensions it's
// just the metric name; with dimensions we append a compact key=val list so
// the chart legend distinguishes overlaid series.
func labelFor(metric string, dims []cwtypes.Dimension) string {
	if len(dims) == 0 {
		return metric
	}
	pairs := make([]string, 0, len(dims))
	for _, d := range dims {
		pairs = append(pairs, fmt.Sprintf("%s=%s", aws.ToString(d.Name), aws.ToString(d.Value)))
	}
	sort.Strings(pairs)
	return metric + " {" + strings.Join(pairs, ", ") + "}"
}

// validStatistic mirrors the CloudWatch enum without dragging in the SDK's
// constants — keeping the YAML check loop-free and the error message focused.
func validStatistic(s string) bool {
	switch s {
	case "Average", "Sum", "Maximum", "Minimum", "SampleCount", "p50", "p90", "p95", "p99":
		return true
	}
	return false
}

// parseDimensions decodes the optional dimensions: {Name: Value, ...} block
// into a sorted slice; sorting keeps the GetMetricData query key (and so the
// CloudWatch internal cache) stable across reloads.
func parseDimensions(spec map[string]any) ([]cwtypes.Dimension, error) {
	raw, ok := spec["dimensions"]
	if !ok || raw == nil {
		return nil, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("dimensions: expected a map, got %T", raw)
	}
	out := make([]cwtypes.Dimension, 0, len(m))
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("dimensions[%s]: expected string, got %T", k, v)
		}
		out = append(out, cwtypes.Dimension{Name: aws.String(k), Value: aws.String(s)})
	}
	sort.Slice(out, func(i, j int) bool { return aws.ToString(out[i].Name) < aws.ToString(out[j].Name) })
	return out, nil
}

// requireString returns spec[key] as a non-empty string or an error.
func requireString(spec map[string]any, key string) (string, error) {
	v, ok := spec[key]
	if !ok {
		return "", fmt.Errorf("%s is required", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s: expected string, got %T", key, v)
	}
	if s == "" {
		return "", fmt.Errorf("%s must not be empty", key)
	}
	return s, nil
}

// optionalInt returns spec[key] as an int, defaulting to def if absent. YAML
// integer scalars decode to int (gopkg.in/yaml.v3 default), but we also
// accept int64 / float64 for resilience against alternate decoders.
func optionalInt(spec map[string]any, key string, def int) (int, error) {
	v, ok := spec[key]
	if !ok || v == nil {
		return def, nil
	}
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		if n != float64(int(n)) {
			return 0, fmt.Errorf("%s: expected integer, got fractional value %v", key, n)
		}
		return int(n), nil
	}
	return 0, fmt.Errorf("%s: expected integer, got %T", key, v)
}

// requireDuration parses spec[key] as a time.Duration string (e.g. "5m").
// The key is required.
func requireDuration(spec map[string]any, key string) (time.Duration, error) {
	v, ok := spec[key]
	if !ok {
		return 0, fmt.Errorf("%s is required", key)
	}
	s, ok := v.(string)
	if !ok {
		return 0, fmt.Errorf("%s: expected duration string, got %T", key, v)
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

// nowFunc returns deps.Now or falls back to time.Now.
func nowFunc(deps Deps) time.Time {
	if deps.Now != nil {
		return deps.Now()
	}
	return time.Now()
}
