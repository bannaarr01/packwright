package monitorx

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
)

// cwLogsTail implements the "cloudwatch/logs-tail" panel kind. The YAML shape is:
//
//	kind: cloudwatch/logs-tail
//	spec:
//	  log_group: /aws/lambda/api          # required
//	  filter: "ERROR"                     # optional CW Logs filter pattern
//	  lookback: 5m                        # any time.Duration string
//	  limit: 200                          # max events fetched per tick (caps payload size)
//
// PR-03 supports the FilterLogEvents path only. The Insights / StartQuery
// path mentioned in ADR-0015 lands in a later PR; the spec key for that
// (`query:`) is reserved here with an explicit error so authors get a
// useful diagnostic instead of silent ignorance.
type cwLogsTail struct {
	logGroup string
	filter   string
	lookback time.Duration
	limit    int32
}

// defaultLogsLimit caps log-tail responses when the manifest omits limit.
// CloudWatch Logs allows up to 10_000 events per FilterLogEvents call; we
// pick a much smaller default so the in-memory payload stays bounded and
// the UI does not have to virtualize ten thousand rows on the first tick.
const defaultLogsLimit int32 = 200

func init() {
	Register("cloudwatch/logs-tail", func() Panel { return &cwLogsTail{} })
}

// Kind reports the registered panel kind.
func (c *cwLogsTail) Kind() string { return "cloudwatch/logs-tail" }

// Validate captures spec into the receiver and rejects unsupported keys.
func (c *cwLogsTail) Validate(spec map[string]any) error {
	if _, ok := spec["query"]; ok {
		return errors.New("query: CloudWatch Logs Insights queries are not supported yet; use filter:")
	}
	lg, err := requireString(spec, "log_group")
	if err != nil {
		return err
	}
	lookback, err := requireDuration(spec, "lookback")
	if err != nil {
		return err
	}
	if lookback <= 0 {
		return errors.New("lookback must be positive")
	}

	limit, err := optionalInt(spec, "limit", int(defaultLogsLimit))
	if err != nil {
		return err
	}
	if limit < 1 {
		return fmt.Errorf("limit must be at least 1 (got %d)", limit)
	}

	if v, ok := spec["filter"]; ok && v != nil {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("filter: expected string, got %T", v)
		}
		c.filter = s
	}

	c.logGroup = lg
	c.lookback = lookback
	c.limit = int32(limit)
	return nil
}

// Refresh issues a single FilterLogEvents call for the configured lookback
// window. Pagination is deliberately not followed: the panel's job is "show
// the latest N events", not "drain the log group" — that's what Insights is
// for, and it lands behind the reserved `query:` key.
func (c *cwLogsTail) Refresh(ctx context.Context, deps Deps) (PanelData, error) {
	if deps.Logs == nil {
		return nil, errors.New("cloudwatch/logs-tail: Deps.Logs is nil")
	}
	now := nowFunc(deps)
	start := now.Add(-c.lookback)

	in := &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(c.logGroup),
		StartTime:    aws.Int64(start.UnixMilli()),
		EndTime:      aws.Int64(now.UnixMilli()),
		Limit:        aws.Int32(c.limit),
	}
	if c.filter != "" {
		in.FilterPattern = aws.String(c.filter)
	}

	out, err := deps.Logs.FilterLogEvents(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("cloudwatch/logs-tail: FilterLogEvents: %w", err)
	}

	lines := make([]LogLine, 0, len(out.Events))
	for _, ev := range out.Events {
		lines = append(lines, LogLine{
			Time:    time.UnixMilli(aws.ToInt64(ev.Timestamp)).UTC(),
			Stream:  aws.ToString(ev.LogStreamName),
			Message: aws.ToString(ev.Message),
		})
	}
	// Newest first — that's how the renderer scrolls them. FilterLogEvents
	// honours Limit server-side, so no further capping is needed here.
	sort.Slice(lines, func(i, j int) bool { return lines[i].Time.After(lines[j].Time) })
	return LogLinesData{Lines: lines}, nil
}
