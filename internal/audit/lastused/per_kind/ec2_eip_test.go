package perkind

import (
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

func TestEC2EIP(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		assocID    string
		allocTime  *time.Time
		wantConf   lastused.Confidence
		wantBest   time.Time
		wantNoteRE string
	}{
		{
			name:     "associated → High and Best = Now",
			assocID:  "eipassoc-abc",
			wantConf: lastused.High,
			wantBest: fixedNow,
		},
		{
			name:       "unassociated, no allocation time → Low + waste flag",
			assocID:    "",
			wantConf:   lastused.Low,
			wantBest:   time.Time{},
			wantNoteRE: "waste",
		},
		{
			name:       "unassociated with allocation time → still Low + waste flag",
			assocID:    "",
			allocTime:  agoP(45 * 24 * time.Hour),
			wantConf:   lastused.Low,
			wantBest:   ago(45 * 24 * time.Hour),
			wantNoteRE: "waste",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sig := EC2EIP(EC2EIPInput{
				AllocationID:   "eipalloc-aaa",
				AssociationID:  tc.assocID,
				AllocationTime: tc.allocTime,
				Now:            fixedNow,
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
