package perkind

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused/sources"
)

// fixedNow is the deterministic "now" every per-kind test pins to so
// comparisons across days/months stay stable across runs.
var fixedNow = time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)

// tp returns a pointer to t — used to populate optional *time.Time
// fields concisely in table cases.
func tp(t time.Time) *time.Time { return &t }

// ago returns fixedNow minus d.
func ago(d time.Duration) time.Time { return fixedNow.Add(-d) }

// agoP returns a pointer to ago(d).
func agoP(d time.Duration) *time.Time {
	t := ago(d)
	return &t
}

// fakeMetrics is a sources.MetricsClient implementation that returns a
// per-metric value keyed by metric name. Queries that miss the map
// return nil (the "no datapoint in window" outcome).
type fakeMetrics struct {
	values  map[string]*time.Time
	queries []sources.MetricQuery
}

func (f *fakeMetrics) LastNonZero(_ context.Context, q sources.MetricQuery) (*time.Time, error) {
	f.queries = append(f.queries, q)
	if v, ok := f.values[q.Metric]; ok {
		return v, nil
	}
	return nil, nil
}

// fakeENI implements sources.ENIClient.
type fakeENI struct {
	value *time.Time
}

func (f *fakeENI) LastStatusChange(_ context.Context, _ []string) (*time.Time, error) {
	return f.value, nil
}

// fakeLogs implements sources.LogsClient.
type fakeLogs struct {
	value *time.Time
}

func (f *fakeLogs) MostRecentEventTime(_ context.Context, _ string) (*time.Time, error) {
	return f.value, nil
}
