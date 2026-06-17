package perkind

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

type fakeAccessLogs struct{ value *time.Time }

func (f *fakeAccessLogs) LatestAccessLog(_ context.Context, _, _ string) (*time.Time, error) {
	return f.value, nil
}

func TestELBv2LoadBalancer(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		req        *time.Time
		access     *time.Time
		bucket     string
		wantConf   lastused.Confidence
		wantBest   time.Time
		wantNoteRE string
	}{
		{
			name:     "recent requests → High",
			req:      agoP(2 * time.Hour),
			access:   agoP(3 * time.Hour),
			bucket:   "logs-bucket",
			wantConf: lastused.High,
			wantBest: ago(2 * time.Hour),
		},
		{
			name:       "no requests, no access logs configured → Low + idle note",
			req:        nil,
			bucket:     "",
			wantConf:   lastused.Low,
			wantBest:   time.Time{},
			wantNoteRE: "idle",
		},
		{
			name:       "conflicting: recent requests + stale access log",
			req:        agoP(1 * time.Hour),
			access:     agoP(60 * 24 * time.Hour),
			bucket:     "logs-bucket",
			wantConf:   lastused.Medium, // High decremented by disagreement
			wantBest:   ago(1 * time.Hour),
			wantNoteRE: "disagree",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := &fakeMetrics{values: map[string]*time.Time{"RequestCount": tc.req}}
			a := &fakeAccessLogs{value: tc.access}
			sig := ELBv2LoadBalancer(context.Background(), m, a, ELBv2LoadBalancerInput{
				LoadBalancerName: "app/my-lb/abc",
				AccessLogsBucket: tc.bucket,
				Now:              fixedNow,
			})
			if sig.Confidence != tc.wantConf {
				t.Fatalf("Confidence = %s, want %s (notes=%q)", sig.Confidence, tc.wantConf, sig.Notes)
			}
			if !sig.Best.Equal(tc.wantBest) {
				t.Fatalf("Best = %s, want %s", sig.Best, tc.wantBest)
			}
			if tc.wantNoteRE != "" && !strings.Contains(sig.Notes, tc.wantNoteRE) {
				t.Fatalf("Notes = %q, want substring %q", sig.Notes, tc.wantNoteRE)
			}
		})
	}
}
