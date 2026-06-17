package perkind

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

func TestEC2NATGateway(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		bytesOut   *time.Time
		create     *time.Time
		wantConf   lastused.Confidence
		wantBest   time.Time
		wantNoteRE string
	}{
		{
			name:     "recent bytes-out → High",
			bytesOut: agoP(6 * time.Hour),
			create:   agoP(10 * 24 * time.Hour),
			wantConf: lastused.High,
			wantBest: ago(6 * time.Hour),
		},
		{
			name:       "no traffic + old gateway → Low + idle flag",
			bytesOut:   nil,
			create:     agoP(120 * 24 * time.Hour),
			wantConf:   lastused.Low,
			wantBest:   ago(120 * 24 * time.Hour),
			wantNoteRE: "idle",
		},
		{
			name:       "conflicting: traffic 10 d ago + create 100 d ago",
			bytesOut:   agoP(10 * 24 * time.Hour),
			create:     agoP(100 * 24 * time.Hour),
			wantConf:   lastused.Low, // Medium (traffic outside 7 d) decremented by disagreement (>30 d spread)
			wantBest:   ago(10 * 24 * time.Hour),
			wantNoteRE: "disagree",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := &fakeMetrics{values: map[string]*time.Time{"BytesOutToDestination": tc.bytesOut}}
			sig := EC2NATGateway(context.Background(), m, EC2NATGatewayInput{
				NATGatewayID: "nat-aaa",
				CreateTime:   tc.create,
				Now:          fixedNow,
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
