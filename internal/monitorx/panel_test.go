package monitorx

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwltypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

// fakeMetrics is the fake for the MetricsAPI surface. The pattern mirrors
// awsx/ec2_test.go: canned outputs are dequeued in call order, with
// errNoMoreResponses surfacing the moment a test asks for one extra call.
// `last` is captured so tests can assert on the request shape we built.
type fakeMetrics struct {
	outs   []*cloudwatch.GetMetricDataOutput
	last   *cloudwatch.GetMetricDataInput
	failOn error
}

func (f *fakeMetrics) GetMetricData(_ context.Context, in *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	f.last = in
	if f.failOn != nil {
		err := f.failOn
		f.failOn = nil
		return nil, err
	}
	if len(f.outs) == 0 {
		return nil, errNoMoreResponses
	}
	out := f.outs[0]
	f.outs = f.outs[1:]
	return out, nil
}

// fakeLogs is the fake for the LogsAPI surface.
type fakeLogs struct {
	outs   []*cloudwatchlogs.FilterLogEventsOutput
	last   *cloudwatchlogs.FilterLogEventsInput
	failOn error
}

func (f *fakeLogs) FilterLogEvents(_ context.Context, in *cloudwatchlogs.FilterLogEventsInput, _ ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.FilterLogEventsOutput, error) {
	f.last = in
	if f.failOn != nil {
		err := f.failOn
		f.failOn = nil
		return nil, err
	}
	if len(f.outs) == 0 {
		return nil, errNoMoreResponses
	}
	out := f.outs[0]
	f.outs = f.outs[1:]
	return out, nil
}

var errNoMoreResponses = errors.New("test fake: no more canned responses")

// fixedNow returns a deterministic Now function used by every test that
// asserts on the time window we send to AWS.
func fixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestRegisterKindsRegistered(t *testing.T) {
	want := map[string]bool{
		"cloudwatch/metric":    true,
		"cloudwatch/logs-tail": true,
		"ecs/cpu":              true,
	}
	for _, k := range Kinds() {
		delete(want, k)
	}
	if len(want) > 0 {
		t.Fatalf("missing kinds in registry: %v", want)
	}
}

func TestBuildUnknownKind(t *testing.T) {
	if _, err := Build("nope/none", map[string]any{}); err == nil {
		t.Fatal("Build(unknown): err = nil, want one")
	}
}

func TestCWMetricValidateAndRefresh(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	spec := map[string]any{
		"namespace": "AWS/EC2",
		"metric":    "CPUUtilization",
		"statistic": "Average",
		"period":    60,
		"lookback":  "1h",
		"unit":      "Percent",
		"dimensions": map[string]any{
			"InstanceId": "i-0123456789abcdef0",
		},
	}
	p, err := Build("cloudwatch/metric", spec)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if p.Kind() != "cloudwatch/metric" {
		t.Fatalf("Kind = %q", p.Kind())
	}

	cw := &fakeMetrics{outs: []*cloudwatch.GetMetricDataOutput{
		{MetricDataResults: []cwtypes.MetricDataResult{{
			Id:         aws.String("m1"),
			Timestamps: []time.Time{now.Add(-3 * time.Minute), now.Add(-2 * time.Minute), now.Add(-1 * time.Minute)},
			Values:     []float64{1, 2, 3},
		}}},
	}}

	data, err := p.Refresh(context.Background(), Deps{Metrics: cw, Now: fixedNow(now)})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	sd, ok := data.(SeriesData)
	if !ok {
		t.Fatalf("data type = %T, want SeriesData", data)
	}
	if len(sd.Series) != 1 {
		t.Fatalf("series count = %d, want 1", len(sd.Series))
	}
	if got := len(sd.Series[0].Points); got != 3 {
		t.Fatalf("points = %d, want 3", got)
	}
	for i := 1; i < len(sd.Series[0].Points); i++ {
		if !sd.Series[0].Points[i-1].Time.Before(sd.Series[0].Points[i].Time) {
			t.Fatalf("points not sorted ascending: %v", sd.Series[0].Points)
		}
	}
	if cw.last == nil || aws.ToTime(cw.last.StartTime) != now.Add(-time.Hour) {
		t.Fatalf("start = %v, want %v", aws.ToTime(cw.last.StartTime), now.Add(-time.Hour))
	}
	if aws.ToTime(cw.last.EndTime) != now {
		t.Fatalf("end = %v, want %v", aws.ToTime(cw.last.EndTime), now)
	}
	q := cw.last.MetricDataQueries[0]
	if aws.ToString(q.MetricStat.Metric.Namespace) != "AWS/EC2" {
		t.Fatalf("namespace = %q", aws.ToString(q.MetricStat.Metric.Namespace))
	}
	if aws.ToString(q.MetricStat.Stat) != "Average" {
		t.Fatalf("stat = %q", aws.ToString(q.MetricStat.Stat))
	}
}

func TestCWMetricValidateRejectsMissingFields(t *testing.T) {
	cases := []struct {
		name string
		spec map[string]any
	}{
		{"no namespace", map[string]any{"metric": "X", "statistic": "Average", "lookback": "1h"}},
		{"no metric", map[string]any{"namespace": "AWS/EC2", "statistic": "Average", "lookback": "1h"}},
		{"no statistic", map[string]any{"namespace": "AWS/EC2", "metric": "X", "lookback": "1h"}},
		{"bad statistic", map[string]any{"namespace": "AWS/EC2", "metric": "X", "statistic": "Bogus", "lookback": "1h"}},
		{"no lookback", map[string]any{"namespace": "AWS/EC2", "metric": "X", "statistic": "Average"}},
		{"bad lookback", map[string]any{"namespace": "AWS/EC2", "metric": "X", "statistic": "Average", "lookback": "five-minutes"}},
		{"bad period", map[string]any{"namespace": "AWS/EC2", "metric": "X", "statistic": "Average", "lookback": "1h", "period": 0}},
		{"bad dimensions type", map[string]any{"namespace": "AWS/EC2", "metric": "X", "statistic": "Average", "lookback": "1h", "dimensions": "wrong"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Build("cloudwatch/metric", tc.spec); err == nil {
				t.Fatal("Build err = nil, want one")
			}
		})
	}
}

func TestCWMetricRefreshSDKError(t *testing.T) {
	p, err := Build("cloudwatch/metric", map[string]any{
		"namespace": "AWS/EC2", "metric": "X", "statistic": "Average", "lookback": "1h",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	cw := &fakeMetrics{failOn: errors.New("boom")}
	if _, err := p.Refresh(context.Background(), Deps{Metrics: cw, Now: fixedNow(time.Now())}); err == nil {
		t.Fatal("Refresh err = nil, want wrapping boom")
	}
}

func TestCWLogsTailValidateAndRefresh(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	p, err := Build("cloudwatch/logs-tail", map[string]any{
		"log_group": "/aws/lambda/api",
		"filter":    "ERROR",
		"lookback":  "5m",
		"limit":     3,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	t1 := now.Add(-1 * time.Minute)
	t2 := now.Add(-2 * time.Minute)
	t3 := now.Add(-3 * time.Minute)
	logs := &fakeLogs{outs: []*cloudwatchlogs.FilterLogEventsOutput{{
		Events: []cwltypes.FilteredLogEvent{
			{Timestamp: aws.Int64(t3.UnixMilli()), Message: aws.String("oldest"), LogStreamName: aws.String("s1")},
			{Timestamp: aws.Int64(t1.UnixMilli()), Message: aws.String("newest"), LogStreamName: aws.String("s1")},
			{Timestamp: aws.Int64(t2.UnixMilli()), Message: aws.String("middle"), LogStreamName: aws.String("s2")},
		},
	}}}

	data, err := p.Refresh(context.Background(), Deps{Logs: logs, Now: fixedNow(now)})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	ld, ok := data.(LogLinesData)
	if !ok {
		t.Fatalf("data type = %T, want LogLinesData", data)
	}
	if len(ld.Lines) != 3 {
		t.Fatalf("lines = %d, want 3", len(ld.Lines))
	}
	if ld.Lines[0].Message != "newest" || ld.Lines[2].Message != "oldest" {
		t.Fatalf("not newest-first: %+v", ld.Lines)
	}
	if logs.last == nil || aws.ToString(logs.last.LogGroupName) != "/aws/lambda/api" {
		t.Fatalf("log group = %q", aws.ToString(logs.last.LogGroupName))
	}
	if aws.ToString(logs.last.FilterPattern) != "ERROR" {
		t.Fatalf("filter = %q", aws.ToString(logs.last.FilterPattern))
	}
	if aws.ToInt32(logs.last.Limit) != 3 {
		t.Fatalf("limit = %d", aws.ToInt32(logs.last.Limit))
	}
}

func TestCWLogsTailRejectsInsightsKey(t *testing.T) {
	if _, err := Build("cloudwatch/logs-tail", map[string]any{
		"log_group": "/aws/lambda/api",
		"lookback":  "5m",
		"query":     "fields @message",
	}); err == nil {
		t.Fatal("Build(query): err = nil, want rejection")
	}
}

func TestECSCPUDelegatesToCWMetric(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	p, err := Build("ecs/cpu", map[string]any{
		"cluster":  "prod",
		"service":  "api",
		"lookback": "30m",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	cw := &fakeMetrics{outs: []*cloudwatch.GetMetricDataOutput{
		{MetricDataResults: []cwtypes.MetricDataResult{{
			Id:         aws.String("m1"),
			Timestamps: []time.Time{now.Add(-1 * time.Minute)},
			Values:     []float64{42.0},
		}}},
	}}

	data, err := p.Refresh(context.Background(), Deps{Metrics: cw, Now: fixedNow(now)})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	sd := data.(SeriesData)
	if sd.Series[0].Label != "api CPU" {
		t.Fatalf("label = %q, want \"api CPU\"", sd.Series[0].Label)
	}
	if sd.Series[0].Unit != "Percent" {
		t.Fatalf("unit = %q, want Percent", sd.Series[0].Unit)
	}
	q := cw.last.MetricDataQueries[0]
	if aws.ToString(q.MetricStat.Metric.Namespace) != "AWS/ECS" {
		t.Fatalf("namespace = %q, want AWS/ECS", aws.ToString(q.MetricStat.Metric.Namespace))
	}
	if aws.ToString(q.MetricStat.Metric.MetricName) != "CPUUtilization" {
		t.Fatalf("metric = %q, want CPUUtilization", aws.ToString(q.MetricStat.Metric.MetricName))
	}
	gotDims := map[string]string{}
	for _, d := range q.MetricStat.Metric.Dimensions {
		gotDims[aws.ToString(d.Name)] = aws.ToString(d.Value)
	}
	if gotDims["ClusterName"] != "prod" || gotDims["ServiceName"] != "api" {
		t.Fatalf("dims = %v, want ClusterName=prod ServiceName=api", gotDims)
	}
}

func TestECSCPURejectsMissingFields(t *testing.T) {
	cases := []map[string]any{
		{"service": "api", "lookback": "5m"},
		{"cluster": "prod", "lookback": "5m"},
		{"cluster": "prod", "service": "api"},
	}
	for i, spec := range cases {
		if _, err := Build("ecs/cpu", spec); err == nil {
			t.Fatalf("case %d: Build err = nil, want one", i)
		}
	}
}

func TestPanelDataIsTypedSum(t *testing.T) {
	// Compile-time witness: each concrete type satisfies PanelData.
	var _ PanelData = SeriesData{}
	var _ PanelData = LogLinesData{}
	var _ PanelData = HealthData{}
}
