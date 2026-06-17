package perkind

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

type fakeRDSDBExists struct{ exists bool }

func (f *fakeRDSDBExists) DBInstanceExists(_ context.Context, _ string) (bool, error) {
	return f.exists, nil
}

func TestRDSDBSnapshot(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		createTime *time.Time
		dbExists   bool
		sourceID   string
		wantConf   lastused.Confidence
		wantBest   time.Time
		wantNoteRE string
	}{
		{
			name:       "recent snapshot, source DB present → Medium",
			createTime: agoP(10 * 24 * time.Hour),
			dbExists:   true,
			sourceID:   "db-1",
			wantConf:   lastused.Medium,
			wantBest:   ago(10 * 24 * time.Hour),
		},
		{
			name:       "old snapshot + source DB deleted → Low + stale flag",
			createTime: agoP(120 * 24 * time.Hour),
			dbExists:   false,
			sourceID:   "db-deleted",
			wantConf:   lastused.Low,
			wantBest:   ago(120 * 24 * time.Hour),
			wantNoteRE: "stale",
		},
		{
			name:       "old snapshot + source DB still present → Low (>90 d)",
			createTime: agoP(120 * 24 * time.Hour),
			dbExists:   true,
			sourceID:   "db-1",
			wantConf:   lastused.Low,
			wantBest:   ago(120 * 24 * time.Hour),
			wantNoteRE: "> 90 d",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &fakeRDSDBExists{exists: tc.dbExists}
			sig := RDSDBSnapshot(context.Background(), c, RDSDBSnapshotInput{
				DBSnapshotIdentifier:       "snap-aaa",
				SourceDBInstanceIdentifier: tc.sourceID,
				SnapshotCreateTime:         tc.createTime,
				Now:                        fixedNow,
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
