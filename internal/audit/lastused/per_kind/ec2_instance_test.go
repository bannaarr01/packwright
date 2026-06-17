package perkind

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

func TestEC2Instance(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		cpu        *time.Time
		launch     *time.Time
		eni        *time.Time
		wantConf   lastused.Confidence
		wantBest   time.Time
		wantNoteRE string // substring
	}{
		{
			name:     "recent CPU drives High confidence",
			cpu:      agoP(2 * time.Hour),
			launch:   agoP(5 * 24 * time.Hour),
			eni:      agoP(48 * time.Hour),
			wantConf: lastused.High,
			wantBest: ago(2 * time.Hour),
		},
		{
			name:       "stale signals → Low + 90 d note",
			cpu:        nil,
			launch:     agoP(200 * 24 * time.Hour),
			eni:        agoP(180 * 24 * time.Hour),
			wantConf:   lastused.Low,
			wantBest:   ago(180 * 24 * time.Hour),
			wantNoteRE: "90",
		},
		{
			name:       "conflicting: launch recent but CPU absent + ENI 60 d ago",
			cpu:        nil,
			launch:     agoP(1 * 24 * time.Hour),
			eni:        agoP(60 * 24 * time.Hour),
			wantConf:   lastused.Low, // Medium decremented by disagreement detector (>30 d spread)
			wantBest:   ago(1 * 24 * time.Hour),
			wantNoteRE: "disagree",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := &fakeMetrics{values: map[string]*time.Time{"CPUUtilization": tc.cpu}}
			eni := &fakeENI{value: tc.eni}

			sig := EC2Instance(context.Background(), m, eni, EC2InstanceInput{
				InstanceID: "i-aaa",
				LaunchTime: tc.launch,
				ENIIDs:     []string{"eni-1"},
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
