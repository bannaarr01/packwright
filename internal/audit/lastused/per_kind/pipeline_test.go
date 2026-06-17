package perkind

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

type fakePipeline struct{ value *time.Time }

func (f *fakePipeline) LatestExecutionStart(_ context.Context, _ string) (*time.Time, error) {
	return f.value, nil
}

func TestPipeline(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		exec       *time.Time
		wantConf   lastused.Confidence
		wantBest   time.Time
		wantNoteRE string
	}{
		{
			name:     "recent execution within 60 d → High",
			exec:     agoP(10 * 24 * time.Hour),
			wantConf: lastused.High,
			wantBest: ago(10 * 24 * time.Hour),
		},
		{
			name:       "stale execution ≥60 d → Low + unused note",
			exec:       agoP(120 * 24 * time.Hour),
			wantConf:   lastused.Low,
			wantBest:   ago(120 * 24 * time.Hour),
			wantNoteRE: "unused",
		},
		{
			name:     "no executions at all → Unknown",
			exec:     nil,
			wantConf: lastused.Unknown,
			wantBest: time.Time{},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sig := Pipeline(context.Background(), &fakePipeline{value: tc.exec}, PipelineInput{
				PipelineName: "my-pipeline",
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
