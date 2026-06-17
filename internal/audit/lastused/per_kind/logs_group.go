package perkind

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
	"github.com/bannaarr01/packwright/internal/audit/lastused/sources"
)

// LogsLogGroupInput collects the per-group facts the scanner has from
// DescribeLogGroups.
type LogsLogGroupInput struct {
	// LogGroupName is the group's full name, e.g.
	// "/aws/lambda/my-handler".
	LogGroupName string
	// CreationTime is the group's creation timestamp.
	CreationTime *time.Time
	// RetentionInDays is the group's retention policy. A nil value
	// means "never expire" and triggers a warning regardless of
	// activity (per ADR-0041).
	RetentionInDays *int
	// Now is the reference time.
	Now time.Time
}

// LogsLogGroup composes the ADR-0041 signals for a logs/log-group:
// most-recent stored event timestamp, CreationTime (used when no
// streams exist), and a "never-expire" retention warning. Groups with
// no events for ≥ 30 d and retention=null are flagged.
func LogsLogGroup(ctx context.Context, l sources.LogsClient, in LogsLogGroupInput) lastused.LastUsedSignal {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}

	srcs := []lastused.LastUsedSource{
		sources.LogGroupLastEvent(ctx, "logs.last-event", l, in.LogGroupName),
		sources.Static("logs.create-time", in.CreationTime),
	}

	rule := func(ss []lastused.LastUsedSource, best, now time.Time) (lastused.Confidence, string) {
		event := lastused.SourceByName(ss, "logs.last-event")
		hasEvent := event != nil && event.HasValue()
		recentEvent := hasEvent && lastused.Within(*event.Value, now, lastused.Days(30))
		neverExpires := in.RetentionInDays == nil

		switch {
		case neverExpires && !recentEvent:
			return lastused.Low, "Retention is never-expire and no events in ≥30 d — set a retention or delete."
		case neverExpires && recentEvent:
			// ADR-0041: a null retention gets a warning regardless of activity.
			return lastused.High, "Retention is never-expire — set a retention policy."
		case recentEvent:
			return lastused.High, ""
		case !hasEvent && best.IsZero():
			return lastused.Unknown, ""
		case !hasEvent:
			return lastused.Low, "No stored events — group is empty (only creation time is known)."
		default:
			return lastused.Medium, "Events exist but none recent."
		}
	}

	return lastused.Compose(srcs, rule, in.Now)
}
