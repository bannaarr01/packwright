package perkind

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

func TestEFSFileSystem(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		io           *time.Time
		conns        *time.Time
		lastModified *time.Time
		wantConf     lastused.Confidence
		wantBest     time.Time
		wantNoteRE   string
	}{
		{
			name:         "recent IO + connections → High",
			io:           agoP(2 * time.Hour),
			conns:        agoP(3 * time.Hour),
			lastModified: agoP(5 * 24 * time.Hour),
			wantConf:     lastused.High,
			wantBest:     ago(2 * time.Hour),
		},
		{
			name:         "zero IO + zero connections → Low + idle",
			io:           nil,
			conns:        nil,
			lastModified: agoP(45 * 24 * time.Hour),
			wantConf:     lastused.Low,
			wantBest:     ago(45 * 24 * time.Hour),
			wantNoteRE:   "idle",
		},
		{
			name:         "conflicting: recent IO + stale connections + stale lastModified",
			io:           agoP(1 * time.Hour),
			conns:        agoP(45 * 24 * time.Hour),
			lastModified: agoP(60 * 24 * time.Hour),
			wantConf:     lastused.Medium, // High decremented by disagreement
			wantBest:     ago(1 * time.Hour),
			wantNoteRE:   "disagree",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := &fakeMetrics{values: map[string]*time.Time{
				"TotalIOBytes":      tc.io,
				"ClientConnections": tc.conns,
			}}
			sig := EFSFileSystem(context.Background(), m, EFSFileSystemInput{
				FileSystemID:     "fs-aaa",
				LastModifiedTime: tc.lastModified,
				Now:              fixedNow,
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
