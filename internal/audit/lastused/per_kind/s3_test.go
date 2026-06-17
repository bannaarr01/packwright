package perkind

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

type fakeS3Sample struct {
	value  *time.Time
	probed int
}

func (f *fakeS3Sample) SampleLatestObject(_ context.Context, _ string, _ int) (*time.Time, int, error) {
	return f.value, f.probed, nil
}

func TestS3Bucket(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		object     *time.Time
		probed     int
		bucketSize *time.Time
		wantConf   lastused.Confidence
		wantBest   time.Time
		wantNoteRE string
	}{
		{
			name:       "recent object + full sample → Medium (read-tier cap)",
			object:     agoP(5 * 24 * time.Hour),
			probed:     100,
			bucketSize: agoP(2 * 24 * time.Hour),
			wantConf:   lastused.Medium,
			wantBest:   ago(2 * 24 * time.Hour),
		},
		{
			name:       "stale signals → Low + no activity note",
			object:     agoP(120 * 24 * time.Hour),
			probed:     100,
			bucketSize: nil,
			wantConf:   lastused.Low,
			wantBest:   ago(120 * 24 * time.Hour),
			wantNoteRE: "No activity",
		},
		{
			name:       "conflicting: recent object + stale bucket-size + incomplete sample",
			object:     agoP(1 * 24 * time.Hour),
			probed:     12, // smaller than DefaultS3SampleSize=100
			bucketSize: agoP(60 * 24 * time.Hour),
			wantConf:   lastused.Low, // Medium decremented by disagreement
			wantBest:   ago(1 * 24 * time.Hour),
			wantNoteRE: "incomplete",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := &fakeMetrics{values: map[string]*time.Time{"BucketSizeBytes": tc.bucketSize}}
			s := &fakeS3Sample{value: tc.object, probed: tc.probed}
			sig := S3Bucket(context.Background(), m, s, S3BucketInput{
				BucketName: "my-bucket",
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
