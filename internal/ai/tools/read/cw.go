package read

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// cwClientFactory builds a CloudWatch client bound to the same profile /
// region as the toolset's awsx.Client. Replaceable in tests.
var cwClientFactory = func(ctx context.Context, toolName string) (cwAPI, error) {
	c, err := tools.RequireAWSClient(ctx, toolName)
	if err != nil {
		return nil, err
	}
	cfg, err := tools.LoadAWSConfig(ctx, toolName, c)
	if err != nil {
		return nil, err
	}
	return cloudwatch.NewFromConfig(cfg), nil
}

// cwAPI is the subset of CloudWatch operations the read tools call.
type cwAPI interface {
	GetMetricData(ctx context.Context, in *cloudwatch.GetMetricDataInput, opts ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error)
}

// getMetricData wraps cloudwatch:GetMetricData. The AI uses it to pull recent
// values for a metric (CPU, request count, queue depth) when diagnosing why a
// service looks unhealthy.
type getMetricData struct{}

// Name reports the catalogue name.
func (getMetricData) Name() string { return "cw/get-metric-data" }

// Permission returns the const PermissionRead.
func (getMetricData) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t getMetricData) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Fetch CloudWatch metric data points for a single metric over a time range. Returns parallel timestamps[] and values[] arrays.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"namespace":   map[string]any{"type": "string", "description": "Metric namespace (e.g. AWS/EC2, AWS/ECS)."},
				"metric_name": map[string]any{"type": "string", "description": "Metric name (e.g. CPUUtilization, MemoryUtilization)."},
				"stat":        map[string]any{"type": "string", "description": "Statistic — Average, Sum, Maximum, Minimum, SampleCount. Default Average."},
				"period_seconds": map[string]any{
					"type":        "integer",
					"description": "Granularity in seconds. Must be a multiple of 60 for ranges over 3 hours. Default 60.",
				},
				"start_time_iso": map[string]any{"type": "string", "description": "RFC3339 start time. Default 1 hour ago."},
				"end_time_iso":   map[string]any{"type": "string", "description": "RFC3339 end time. Default now."},
				"dimensions": map[string]any{
					"type":        "object",
					"description": "Map of dimension name -> value (e.g. {\"ClusterName\": \"prod\"}).",
				},
			},
			"required": []string{"namespace", "metric_name"},
		},
	}
}

// Execute issues one GetMetricData query and returns the data points.
func (t getMetricData) Execute(ctx context.Context, args map[string]any) (any, error) {
	namespace, err := tools.ArgString(t.Name(), args, "namespace", true)
	if err != nil {
		return nil, err
	}
	metricName, err := tools.ArgString(t.Name(), args, "metric_name", true)
	if err != nil {
		return nil, err
	}
	stat, err := tools.ArgString(t.Name(), args, "stat", false)
	if err != nil {
		return nil, err
	}
	if stat == "" {
		stat = "Average"
	}
	period, err := tools.ArgInt(t.Name(), args, "period_seconds", false)
	if err != nil {
		return nil, err
	}
	if period <= 0 {
		period = 60
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
	dimsRaw, err := tools.ArgMap(t.Name(), args, "dimensions", false)
	if err != nil {
		return nil, err
	}
	dims := make([]cwtypes.Dimension, 0, len(dimsRaw))
	for k, v := range dimsRaw {
		s, ok := v.(string)
		if !ok {
			return nil, &tools.ToolError{
				Code: tools.ErrCodeBadArgs, Tool: t.Name(),
				Message: "dimensions values must be strings",
			}
		}
		dims = append(dims, cwtypes.Dimension{Name: aws.String(k), Value: aws.String(s)})
	}

	api, err := cwClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	out, err := api.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(start),
		EndTime:   aws.Time(end),
		MetricDataQueries: []cwtypes.MetricDataQuery{{
			Id:         aws.String("m1"),
			ReturnData: aws.Bool(true),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace:  aws.String(namespace),
					MetricName: aws.String(metricName),
					Dimensions: dims,
				},
				Period: aws.Int32(int32(period)),
				Stat:   aws.String(stat),
			},
		}},
	})
	if err != nil {
		return nil, err
	}

	results := make([]map[string]any, 0, len(out.MetricDataResults))
	for _, r := range out.MetricDataResults {
		ts := make([]string, 0, len(r.Timestamps))
		for _, t := range r.Timestamps {
			ts = append(ts, t.UTC().Format(time.RFC3339))
		}
		results = append(results, map[string]any{
			"id":         aws.ToString(r.Id),
			"label":      aws.ToString(r.Label),
			"status":     string(r.StatusCode),
			"timestamps": ts,
			"values":     r.Values,
		})
	}
	return map[string]any{"results": results}, nil
}

// parseTimeArg parses an RFC3339-ish timestamp from args and wraps a parse
// failure as ErrCodeBadArgs so the LLM gets a structured nudge.
func parseTimeArg(toolName, key, value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, &tools.ToolError{
			Code: tools.ErrCodeBadArgs, Tool: toolName,
			Message: "argument " + key + ` must be RFC3339 (e.g. "2026-06-17T15:04:05Z")`,
			Cause:   err,
		}
	}
	return t, nil
}

func init() {
	tools.MustRegister(tools.Default, getMetricData{})
}
