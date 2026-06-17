package read

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/codepipeline"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// codepipelineClientFactory builds a CodePipeline client bound to the
// toolset's awsx.Client.
var codepipelineClientFactory = func(ctx context.Context, toolName string) (codepipelineAPI, error) {
	c, err := tools.RequireAWSClient(ctx, toolName)
	if err != nil {
		return nil, err
	}
	cfg, err := tools.LoadAWSConfig(ctx, toolName, c)
	if err != nil {
		return nil, err
	}
	return codepipeline.NewFromConfig(cfg), nil
}

// codepipelineAPI is the subset of CodePipeline operations the read tool calls.
type codepipelineAPI interface {
	GetPipelineExecution(ctx context.Context, in *codepipeline.GetPipelineExecutionInput, opts ...func(*codepipeline.Options)) (*codepipeline.GetPipelineExecutionOutput, error)
	ListPipelineExecutions(ctx context.Context, in *codepipeline.ListPipelineExecutionsInput, opts ...func(*codepipeline.Options)) (*codepipeline.ListPipelineExecutionsOutput, error)
}

// getPipelineExecution returns a pipeline execution by id, or the most recent
// executions on the named pipeline when no id is supplied.
type getPipelineExecution struct{}

// Name reports the catalogue name.
func (getPipelineExecution) Name() string { return "pipeline/get-execution" }

// Permission returns the const PermissionRead.
func (getPipelineExecution) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t getPipelineExecution) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Get a CodePipeline execution by ID, or the most recent executions on the named pipeline when execution_id is empty.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pipeline_name": map[string]any{"type": "string", "description": "Pipeline name."},
				"execution_id":  map[string]any{"type": "string", "description": "Pipeline execution ID. Empty returns the recent executions list."},
				"limit": map[string]any{
					"type":        "integer",
					"description": "When execution_id is empty: max number of recent executions to list (default 5, max 50).",
				},
			},
			"required": []string{"pipeline_name"},
		},
	}
}

// Execute calls GetPipelineExecution or ListPipelineExecutions.
func (t getPipelineExecution) Execute(ctx context.Context, args map[string]any) (any, error) {
	name, err := tools.ArgString(t.Name(), args, "pipeline_name", true)
	if err != nil {
		return nil, err
	}
	execID, err := tools.ArgString(t.Name(), args, "execution_id", false)
	if err != nil {
		return nil, err
	}
	limit, err := tools.ArgInt(t.Name(), args, "limit", false)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}

	api, err := codepipelineClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	if execID != "" {
		out, err := api.GetPipelineExecution(ctx, &codepipeline.GetPipelineExecutionInput{
			PipelineName:        aws.String(name),
			PipelineExecutionId: aws.String(execID),
		})
		if err != nil {
			return nil, err
		}
		e := out.PipelineExecution
		if e == nil {
			return map[string]any{"execution": nil}, nil
		}
		return map[string]any{"execution": map[string]any{
			"pipeline_name":  aws.ToString(e.PipelineName),
			"execution_id":   aws.ToString(e.PipelineExecutionId),
			"status":         string(e.Status),
			"status_summary": aws.ToString(e.StatusSummary),
			"execution_mode": string(e.ExecutionMode),
			"execution_type": string(e.ExecutionType),
		}}, nil
	}
	out, err := api.ListPipelineExecutions(ctx, &codepipeline.ListPipelineExecutionsInput{
		PipelineName: aws.String(name),
		MaxResults:   aws.Int32(int32(limit)),
	})
	if err != nil {
		return nil, err
	}
	res := make([]map[string]any, 0, len(out.PipelineExecutionSummaries))
	for _, s := range out.PipelineExecutionSummaries {
		entry := map[string]any{
			"execution_id":   aws.ToString(s.PipelineExecutionId),
			"status":         string(s.Status),
			"status_summary": aws.ToString(s.StatusSummary),
		}
		if s.StartTime != nil {
			entry["start_time"] = s.StartTime.UTC().Format("2006-01-02T15:04:05Z")
		}
		if s.LastUpdateTime != nil {
			entry["last_update"] = s.LastUpdateTime.UTC().Format("2006-01-02T15:04:05Z")
		}
		res = append(res, entry)
	}
	return map[string]any{"executions": res}, nil
}

func init() {
	tools.MustRegister(tools.Default, getPipelineExecution{})
}
