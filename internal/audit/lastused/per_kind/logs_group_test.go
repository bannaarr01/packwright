package perkind

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

func intPtr(i int) *int { return &i }

func TestLogsLogGroup(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		event      *time.Time
		create     *time.Time
		retention  *int
		wantConf   lastused.Confidence
		wantBest   time.Time
		wantNoteRE string
	}{
		{
			name:      "recent event + retention set → High",
			event:     agoP(2 * time.Hour),
			create:    agoP(60 * 24 * time.Hour),
			retention: intPtr(14),
			wantConf:  lastused.Medium, // 60 d gap triggers disagreement, High→Medium
			wantBest:  ago(2 * time.Hour),
		},
		{
			name:       "never-expire + recent events → High with retention warning (ADR rule: null retention always warns)",
			event:      agoP(2 * time.Hour),
			create:     agoP(5 * 24 * time.Hour),
			retention:  nil,
			wantConf:   lastused.High,
			wantBest:   ago(2 * time.Hour),
			wantNoteRE: "never-expire",
		},
		{
			name:       "never-expire + no recent events → Low + warning",
			event:      nil,
			create:     agoP(200 * 24 * time.Hour),
			retention:  nil,
			wantConf:   lastused.Low,
			wantBest:   ago(200 * 24 * time.Hour),
			wantNoteRE: "retention",
		},
		{
			name:       "conflicting: very old event + recent create (impossible IRL, but tests detector)",
			event:      agoP(200 * 24 * time.Hour),
			create:     agoP(5 * 24 * time.Hour),
			retention:  intPtr(7),
			wantConf:   lastused.Low, // Medium (events exist, none recent) decremented by disagreement
			wantBest:   ago(5 * 24 * time.Hour),
			wantNoteRE: "disagree",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := &fakeLogs{value: tc.event}
			sig := LogsLogGroup(context.Background(), l, LogsLogGroupInput{
				LogGroupName:    "/aws/lambda/foo",
				CreationTime:    tc.create,
				RetentionInDays: tc.retention,
				Now:             fixedNow,
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
