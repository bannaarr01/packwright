package sources

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

// LogsClient is the narrow CloudWatch Logs surface [LogGroupLastEvent]
// uses. MostRecentEventTime returns the timestamp of the most-recent
// stored event across every log stream in the group, or nil when the
// group has no events.
type LogsClient interface {
	MostRecentEventTime(ctx context.Context, logGroupName string) (*time.Time, error)
}

// LogGroupLastEvent returns a source named n built from the most-recent
// stored event in logGroupName.
//
// Errors from the client are swallowed and result in a nil Value
// (matching the "no events in window" outcome) — a partial scan should
// not fail the whole composer per ADR-0041. Caller-side logging is
// the client implementation's responsibility.
//
// Cost is 1 (one DescribeLogStreams --order-by LastEventTime call).
func LogGroupLastEvent(ctx context.Context, n string, c LogsClient, logGroupName string) lastused.LastUsedSource {
	src := lastused.LastUsedSource{Name: n, Cost: 1}
	if c == nil {
		return src
	}
	if t, err := c.MostRecentEventTime(ctx, logGroupName); err == nil {
		src.Value = CopyTimePtr(t)
	}
	return src
}
