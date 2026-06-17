package perkind

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

func TestRDSDBInstance(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		conns      *time.Time
		cpu        *time.Time
		restorable *time.Time
		wantConf   lastused.Confidence
		wantBest   time.Time
		wantNoteRE string
	}{
		{
			name:       "recent connections → High",
			conns:      agoP(1 * time.Hour),
			cpu:        agoP(2 * time.Hour),
			restorable: agoP(2 * time.Hour),
			wantConf:   lastused.High,
			wantBest:   ago(1 * time.Hour),
		},
		{
			name:       "no connections or CPU → Low + idle",
			conns:      nil,
			cpu:        nil,
			restorable: agoP(45 * 24 * time.Hour),
			wantConf:   lastused.Low,
			wantBest:   ago(45 * 24 * time.Hour),
			wantNoteRE: "idle",
		},
		{
			name:       "conflicting: CPU recent, no connections, restorable old",
			conns:      nil,
			cpu:        agoP(3 * time.Hour),
			restorable: agoP(80 * 24 * time.Hour),
			wantConf:   lastused.Low, // Medium decremented by disagreement (80 d spread)
			wantBest:   ago(3 * time.Hour),
			wantNoteRE: "disagree",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := &fakeMetrics{values: map[string]*time.Time{
				"DatabaseConnections": tc.conns,
				"CPUUtilization":      tc.cpu,
			}}
			sig := RDSDBInstance(context.Background(), m, RDSDBInstanceInput{
				DBInstanceIdentifier: "db-aaa",
				LatestRestorableTime: tc.restorable,
				Now:                  fixedNow,
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
