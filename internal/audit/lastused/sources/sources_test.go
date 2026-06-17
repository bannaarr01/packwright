package sources

import (
	"context"
	"errors"
	"testing"
	"time"
)

func tp(t time.Time) *time.Time { return &t }

// fakeMetrics implements MetricsClient. queries records every call so
// table tests can assert their composer routed the right query.
type fakeMetrics struct {
	value   *time.Time
	err     error
	queries []MetricQuery
}

func (f *fakeMetrics) LastNonZero(_ context.Context, q MetricQuery) (*time.Time, error) {
	f.queries = append(f.queries, q)
	return f.value, f.err
}

func TestMetric_WithValue(t *testing.T) {
	t.Parallel()

	want := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	c := &fakeMetrics{value: tp(want)}
	q := MetricQuery{
		Namespace:  "AWS/EC2",
		Metric:     "CPUUtilization",
		Dimensions: []Dimension{{Name: "InstanceId", Value: "i-1"}},
		Lookback:   30 * 24 * time.Hour,
	}

	src := Metric(context.Background(), "cw.cpu", c, q)

	if src.Value == nil || !src.Value.Equal(want) {
		t.Fatalf("Value = %v, want %v", src.Value, want)
	}
	if src.LookbackDays != 30 {
		t.Fatalf("LookbackDays = %d, want 30", src.LookbackDays)
	}
	if src.Cost != 1 {
		t.Fatalf("Cost = %d, want 1", src.Cost)
	}
	if len(c.queries) != 1 || c.queries[0].Metric != "CPUUtilization" {
		t.Fatalf("expected one CPUUtilization query, got %+v", c.queries)
	}
}

func TestMetric_NilOnNoData(t *testing.T) {
	t.Parallel()

	c := &fakeMetrics{value: nil}
	src := Metric(context.Background(), "cw.cpu", c, MetricQuery{Lookback: 7 * 24 * time.Hour})
	if src.Value != nil {
		t.Fatalf("Value = %v, want nil", *src.Value)
	}
	if src.LookbackDays != 7 {
		t.Fatalf("LookbackDays = %d, want 7", src.LookbackDays)
	}
}

func TestMetric_ErrorSwallowed(t *testing.T) {
	t.Parallel()

	c := &fakeMetrics{err: errors.New("throttled")}
	src := Metric(context.Background(), "cw.cpu", c, MetricQuery{})
	if src.Value != nil {
		t.Fatalf("Value should be nil on error, got %v", *src.Value)
	}
	if src.Cost != 1 {
		t.Fatalf("Cost still 1 even on error, got %d", src.Cost)
	}
}

type fakeLogs struct {
	value *time.Time
	err   error
	name  string
}

func (f *fakeLogs) MostRecentEventTime(_ context.Context, n string) (*time.Time, error) {
	f.name = n
	return f.value, f.err
}

func TestLogGroupLastEvent(t *testing.T) {
	t.Parallel()

	want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c := &fakeLogs{value: tp(want)}
	src := LogGroupLastEvent(context.Background(), "logs.last-event", c, "/aws/lambda/foo")
	if src.Value == nil || !src.Value.Equal(want) {
		t.Fatalf("Value = %v, want %v", src.Value, want)
	}
	if c.name != "/aws/lambda/foo" {
		t.Fatalf("client received name=%q", c.name)
	}
	if src.Cost != 1 {
		t.Fatalf("Cost = %d, want 1", src.Cost)
	}

	empty := LogGroupLastEvent(context.Background(), "logs.last-event", &fakeLogs{}, "/aws/lambda/bar")
	if empty.Value != nil {
		t.Fatalf("empty group should produce nil Value, got %v", *empty.Value)
	}
}

type fakeENI struct {
	value *time.Time
	err   error
	ids   []string
}

func (f *fakeENI) LastStatusChange(_ context.Context, ids []string) (*time.Time, error) {
	f.ids = ids
	return f.value, f.err
}

func TestENILastStatusChange_WithIDs(t *testing.T) {
	t.Parallel()

	want := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	c := &fakeENI{value: tp(want)}
	src := ENILastStatusChange(context.Background(), "eni.status", c, []string{"eni-1", "eni-2"})
	if src.Value == nil || !src.Value.Equal(want) {
		t.Fatalf("Value = %v, want %v", src.Value, want)
	}
	if src.Cost != 1 {
		t.Fatalf("Cost = %d, want 1", src.Cost)
	}
	if len(c.ids) != 2 {
		t.Fatalf("client received ids=%v", c.ids)
	}
}

func TestENILastStatusChange_NoIDsSkipsCall(t *testing.T) {
	t.Parallel()

	c := &fakeENI{value: tp(time.Now())} // value would be returned IF we called
	src := ENILastStatusChange(context.Background(), "eni.status", c, nil)
	if src.Value != nil {
		t.Fatalf("Value should stay nil when no ENI IDs, got %v", *src.Value)
	}
	if src.Cost != 0 {
		t.Fatalf("Cost should be 0 when no AWS call is made, got %d", src.Cost)
	}
	if c.ids != nil {
		t.Fatalf("client should not have been called, got ids=%v", c.ids)
	}
}

func TestStatic(t *testing.T) {
	t.Parallel()

	want := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	src := Static("create", &want)
	if src.Value == nil || !src.Value.Equal(want) {
		t.Fatalf("Value = %v, want %v", src.Value, want)
	}
	if src.Cost != 0 {
		t.Fatalf("Cost should be 0 for static sources, got %d", src.Cost)
	}

	nilSrc := Static("create", nil)
	if nilSrc.Value != nil {
		t.Fatalf("Static(nil) should produce nil Value")
	}
}
