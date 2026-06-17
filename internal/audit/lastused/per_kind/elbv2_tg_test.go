package perkind

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

func TestELBv2TargetGroup(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		req        *time.Time
		healthy    int
		wantConf   lastused.Confidence
		wantBest   time.Time
		wantNoteRE string
	}{
		{
			name:     "recent requests + healthy targets → High",
			req:      agoP(1 * time.Hour),
			healthy:  3,
			wantConf: lastused.High,
			wantBest: ago(1 * time.Hour),
		},
		{
			name:       "no targets + no requests → Low + orphan",
			req:        nil,
			healthy:    0,
			wantConf:   lastused.Low,
			wantBest:   time.Time{},
			wantNoteRE: "orphan",
		},
		{
			name:       "conflicting: healthy targets + no recent requests",
			req:        nil,
			healthy:    2,
			wantConf:   lastused.Medium,
			wantBest:   time.Time{},
			wantNoteRE: "Targets healthy",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := &fakeMetrics{values: map[string]*time.Time{"RequestCount": tc.req}}
			sig := ELBv2TargetGroup(context.Background(), m, ELBv2TargetGroupInput{
				TargetGroupFullName: "targetgroup/my-tg/abc",
				LoadBalancerName:    "app/my-lb/abc",
				HealthyTargets:      tc.healthy,
				Now:                 fixedNow,
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
