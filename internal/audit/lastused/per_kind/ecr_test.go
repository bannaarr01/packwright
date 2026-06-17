package perkind

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

type fakeECR struct {
	pushed *time.Time
	pulled *time.Time
}

func (f *fakeECR) LatestImagePushed(_ context.Context, _ string) (*time.Time, error) {
	return f.pushed, nil
}

func (f *fakeECR) LatestImagePulled(_ context.Context, _ string) (*time.Time, error) {
	return f.pulled, nil
}

func TestECRRepository(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		pushed     *time.Time
		pulled     *time.Time
		wantConf   lastused.Confidence
		wantBest   time.Time
		wantNoteRE string
	}{
		{
			name:     "recent pull within 90 d → High",
			pushed:   agoP(10 * 24 * time.Hour),
			pulled:   agoP(5 * 24 * time.Hour),
			wantConf: lastused.High,
			wantBest: ago(5 * 24 * time.Hour),
		},
		{
			name:       "no pulls and very old push → Low + unused flag",
			pushed:     agoP(180 * 24 * time.Hour),
			pulled:     nil,
			wantConf:   lastused.Low,
			wantBest:   ago(180 * 24 * time.Hour),
			wantNoteRE: "unused",
		},
		{
			name:       "conflicting: very recent push + ancient pull",
			pushed:     agoP(1 * 24 * time.Hour),
			pulled:     agoP(120 * 24 * time.Hour),
			wantConf:   lastused.Unknown, // Low (>90 d no pulls) decremented by disagreement
			wantBest:   ago(1 * 24 * time.Hour),
			wantNoteRE: "disagree",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &fakeECR{pushed: tc.pushed, pulled: tc.pulled}
			sig := ECRRepository(context.Background(), c, ECRRepositoryInput{
				RepositoryName: "my-repo",
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
