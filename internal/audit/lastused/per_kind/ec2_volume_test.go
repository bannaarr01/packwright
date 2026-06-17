package perkind

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

func TestEC2Volume(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		state      string
		attach     *time.Time
		create     *time.Time
		writeIO    *time.Time
		readIO     *time.Time
		wantConf   lastused.Confidence
		wantBest   time.Time
		wantNoteRE string
	}{
		{
			name:     "attached + recent IO → High",
			state:    "in-use",
			attach:   agoP(3 * 24 * time.Hour),
			create:   agoP(60 * 24 * time.Hour), // 60 d back, within disagreement threshold of attach (still <30 d? no, 57 d apart)
			writeIO:  agoP(2 * time.Hour),
			readIO:   agoP(1 * time.Hour),
			wantConf: lastused.Medium, // High decremented once by disagreement (create vs IO 60 d apart)
			wantBest: ago(1 * time.Hour),
		},
		{
			name:       "available + no IO → Low/orphan",
			state:      "available",
			attach:     nil,
			create:     agoP(120 * 24 * time.Hour),
			writeIO:    nil,
			readIO:     nil,
			wantConf:   lastused.Low,
			wantBest:   ago(120 * 24 * time.Hour),
			wantNoteRE: "orphan",
		},
		{
			name:       "conflicting: attached but stale IO",
			state:      "in-use",
			attach:     agoP(1 * 24 * time.Hour),
			create:     agoP(2 * 24 * time.Hour),
			writeIO:    agoP(45 * 24 * time.Hour),
			readIO:     nil,
			wantConf:   lastused.Low, // Medium decremented by disagreement (1 d vs 45 d → 44 d spread)
			wantBest:   ago(1 * 24 * time.Hour),
			wantNoteRE: "disagree",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := &fakeMetrics{values: map[string]*time.Time{
				"VolumeWriteOps": tc.writeIO,
				"VolumeReadOps":  tc.readIO,
			}}
			sig := EC2Volume(context.Background(), m, EC2VolumeInput{
				VolumeID:   "vol-aaa",
				State:      tc.state,
				AttachTime: tc.attach,
				CreateTime: tc.create,
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
