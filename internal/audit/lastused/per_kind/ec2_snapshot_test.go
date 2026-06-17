package perkind

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

type fakeAMI struct{ value *time.Time }

func (f *fakeAMI) LatestAMIReferencing(_ context.Context, _ string) (*time.Time, error) {
	return f.value, nil
}

func TestEC2Snapshot(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		start      *time.Time
		ami        *time.Time
		wantConf   lastused.Confidence
		wantBest   time.Time
		wantNoteRE string
	}{
		{
			name:     "recent AMI references snapshot → High",
			start:    agoP(10 * 24 * time.Hour),
			ami:      agoP(3 * 24 * time.Hour),
			wantConf: lastused.High,
			wantBest: ago(3 * 24 * time.Hour),
		},
		{
			name:       "stale snapshot, no AMI → Low + waste flag",
			start:      agoP(180 * 24 * time.Hour),
			ami:        nil,
			wantConf:   lastused.Low,
			wantBest:   ago(180 * 24 * time.Hour),
			wantNoteRE: "waste",
		},
		{
			name:       "conflicting: ancient snapshot + ancient AMI (>90 d spread vs each other modest, both > 90 d → Low, no disagreement)",
			start:      agoP(200 * 24 * time.Hour),
			ami:        agoP(210 * 24 * time.Hour),
			wantConf:   lastused.Low,
			wantBest:   ago(200 * 24 * time.Hour),
			wantNoteRE: "waste",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sig := EC2Snapshot(context.Background(), &fakeAMI{value: tc.ami}, EC2SnapshotInput{
				SnapshotID: "snap-aaa",
				StartTime:  tc.start,
				Now:        fixedNow,
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
