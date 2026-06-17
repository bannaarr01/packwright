package read

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// filterLogEventsMaxBytes caps the total message payload returned by the
// filter tool so a noisy log group cannot push megabytes of text into the LLM
// context. Per ADR-0035 the read tool is size-capped.
const filterLogEventsMaxBytes = 64 * 1024

// cwLogsClientFactory builds a CloudWatch Logs client bound to the toolset's
// awsx.Client. Replaceable in tests.
var cwLogsClientFactory = func(ctx context.Context, toolName string) (cwLogsAPI, error) {
	c, err := tools.RequireAWSClient(ctx, toolName)
	if err != nil {
		return nil, err
	}
	cfg, err := tools.LoadAWSConfig(ctx, toolName, c)
	if err != nil {
		return nil, err
	}
	return cloudwatchlogs.NewFromConfig(cfg), nil
}

// cwLogsAPI is the subset of CWL operations the read tools call.
type cwLogsAPI interface {
	StartQuery(ctx context.Context, in *cloudwatchlogs.StartQueryInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.StartQueryOutput, error)
	GetQueryResults(ctx context.Context, in *cloudwatchlogs.GetQueryResultsInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetQueryResultsOutput, error)
	FilterLogEvents(ctx context.Context, in *cloudwatchlogs.FilterLogEventsInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.FilterLogEventsOutput, error)
}

// startQuery starts a CloudWatch Logs Insights query and returns the query
// id. The AI then polls with getQueryResults until status == Complete.
type startQuery struct{}

// Name reports the catalogue name.
func (startQuery) Name() string { return "cw-logs/start-query" }

// Permission returns the const PermissionRead.
func (startQuery) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t startQuery) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Start a CloudWatch Logs Insights query. Returns a query_id to pass to cw-logs/get-query-results.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"log_group_names": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "One or more log group names to query (CWL Insights supports multi-group queries).",
				},
				"query": map[string]any{"type": "string", "description": "Insights query string."},
				"start_time_iso": map[string]any{
					"type":        "string",
					"description": "RFC3339 start time. Default 1 hour ago.",
				},
				"end_time_iso": map[string]any{
					"type":        "string",
					"description": "RFC3339 end time. Default now.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Max results (default 1000, max 10000).",
				},
			},
			"required": []string{"log_group_names", "query"},
		},
	}
}

// Execute issues StartQuery and returns the query id.
func (t startQuery) Execute(ctx context.Context, args map[string]any) (any, error) {
	groups, err := tools.ArgStringSlice(t.Name(), args, "log_group_names", true)
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBadArgs, Tool: t.Name(),
			Message: "log_group_names must contain at least one entry",
		}
	}
	q, err := tools.ArgString(t.Name(), args, "query", true)
	if err != nil {
		return nil, err
	}
	startStr, err := tools.ArgString(t.Name(), args, "start_time_iso", false)
	if err != nil {
		return nil, err
	}
	endStr, err := tools.ArgString(t.Name(), args, "end_time_iso", false)
	if err != nil {
		return nil, err
	}
	end := time.Now().UTC()
	start := end.Add(-1 * time.Hour)
	if startStr != "" {
		start, err = parseTimeArg(t.Name(), "start_time_iso", startStr)
		if err != nil {
			return nil, err
		}
	}
	if endStr != "" {
		end, err = parseTimeArg(t.Name(), "end_time_iso", endStr)
		if err != nil {
			return nil, err
		}
	}
	limit, err := tools.ArgInt(t.Name(), args, "limit", false)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 1000
	}
	if limit > 10000 {
		limit = 10000
	}

	api, err := cwLogsClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	out, err := api.StartQuery(ctx, &cloudwatchlogs.StartQueryInput{
		LogGroupNames: groups,
		QueryString:   aws.String(q),
		StartTime:     aws.Int64(start.Unix()),
		EndTime:       aws.Int64(end.Unix()),
		Limit:         aws.Int32(int32(limit)),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"query_id": aws.ToString(out.QueryId)}, nil
}

// getQueryResults retrieves the results (or current state) of an Insights
// query started by startQuery.
type getQueryResults struct{}

// Name reports the catalogue name.
func (getQueryResults) Name() string { return "cw-logs/get-query-results" }

// Permission returns the const PermissionRead.
func (getQueryResults) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t getQueryResults) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Fetch results (or current status) of a CloudWatch Logs Insights query.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query_id": map[string]any{"type": "string", "description": "Query ID returned by cw-logs/start-query."},
			},
			"required": []string{"query_id"},
		},
	}
}

// Execute issues GetQueryResults and shapes the per-event field map.
func (t getQueryResults) Execute(ctx context.Context, args map[string]any) (any, error) {
	id, err := tools.ArgString(t.Name(), args, "query_id", true)
	if err != nil {
		return nil, err
	}
	api, err := cwLogsClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	out, err := api.GetQueryResults(ctx, &cloudwatchlogs.GetQueryResultsInput{
		QueryId: aws.String(id),
	})
	if err != nil {
		return nil, err
	}
	events := make([]map[string]string, 0, len(out.Results))
	for _, row := range out.Results {
		entry := make(map[string]string, len(row))
		for _, f := range row {
			entry[aws.ToString(f.Field)] = aws.ToString(f.Value)
		}
		events = append(events, entry)
	}
	return map[string]any{
		"status":  string(out.Status),
		"results": events,
	}, nil
}

// filterLogEvents is the plain (non-Insights) log search. The AI uses it when
// a free-text grep is enough — Insights is overkill for a single grep.
type filterLogEvents struct{}

// Name reports the catalogue name.
func (filterLogEvents) Name() string { return "cw-logs/filter-log-events" }

// Permission returns the const PermissionRead.
func (filterLogEvents) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t filterLogEvents) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Filter raw CloudWatch Logs events by pattern. Output is byte-capped at 64 KB total payload; truncated results return truncated=true.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"log_group_name": map[string]any{"type": "string", "description": "Log group name."},
				"filter_pattern": map[string]any{"type": "string", "description": "Filter pattern (CWL filter syntax). Empty matches everything."},
				"start_time_iso": map[string]any{"type": "string", "description": "RFC3339 start time. Default 1 hour ago."},
				"end_time_iso":   map[string]any{"type": "string", "description": "RFC3339 end time. Default now."},
				"limit":          map[string]any{"type": "integer", "description": "Max events to return (default 100, max 1000)."},
			},
			"required": []string{"log_group_name"},
		},
	}
}

// Execute issues FilterLogEvents and applies the byte cap.
func (t filterLogEvents) Execute(ctx context.Context, args map[string]any) (any, error) {
	group, err := tools.ArgString(t.Name(), args, "log_group_name", true)
	if err != nil {
		return nil, err
	}
	pattern, err := tools.ArgString(t.Name(), args, "filter_pattern", false)
	if err != nil {
		return nil, err
	}
	startStr, err := tools.ArgString(t.Name(), args, "start_time_iso", false)
	if err != nil {
		return nil, err
	}
	endStr, err := tools.ArgString(t.Name(), args, "end_time_iso", false)
	if err != nil {
		return nil, err
	}
	end := time.Now().UTC()
	start := end.Add(-1 * time.Hour)
	if startStr != "" {
		start, err = parseTimeArg(t.Name(), "start_time_iso", startStr)
		if err != nil {
			return nil, err
		}
	}
	if endStr != "" {
		end, err = parseTimeArg(t.Name(), "end_time_iso", endStr)
		if err != nil {
			return nil, err
		}
	}
	limit, err := tools.ArgInt(t.Name(), args, "limit", false)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	in := &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String(group),
		StartTime:    aws.Int64(start.UnixMilli()),
		EndTime:      aws.Int64(end.UnixMilli()),
		Limit:        aws.Int32(int32(limit)),
	}
	if pattern != "" {
		in.FilterPattern = aws.String(pattern)
	}

	api, err := cwLogsClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	out, err := api.FilterLogEvents(ctx, in)
	if err != nil {
		return nil, err
	}

	events := make([]map[string]any, 0, len(out.Events))
	total := 0
	truncated := false
	for _, e := range out.Events {
		msg := aws.ToString(e.Message)
		if total+len(msg) > filterLogEventsMaxBytes {
			truncated = true
			break
		}
		total += len(msg)
		entry := map[string]any{
			"message":    msg,
			"log_stream": aws.ToString(e.LogStreamName),
		}
		if e.Timestamp != nil {
			entry["timestamp"] = time.UnixMilli(*e.Timestamp).UTC().Format(time.RFC3339)
		}
		events = append(events, entry)
	}
	return map[string]any{
		"events":    events,
		"truncated": truncated,
	}, nil
}

func init() {
	tools.MustRegister(tools.Default, startQuery{})
	tools.MustRegister(tools.Default, getQueryResults{})
	tools.MustRegister(tools.Default, filterLogEvents{})
}
