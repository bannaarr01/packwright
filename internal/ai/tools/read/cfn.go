package read

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// cfnClientFactory builds a CloudFormation client bound to the same profile /
// region as the toolset's bound awsx.Client. Pulled out as a var so tests can
// replace it without hitting the network.
var cfnClientFactory = func(ctx context.Context, toolName string) (cfnAPI, error) {
	c, err := tools.RequireAWSClient(ctx, toolName)
	if err != nil {
		return nil, err
	}
	cfg, err := tools.LoadAWSConfig(ctx, toolName, c)
	if err != nil {
		return nil, err
	}
	return cloudformation.NewFromConfig(cfg), nil
}

// cfnAPI is the subset of CloudFormation operations the read tools call. The
// SDK client satisfies it structurally; tests inject a fake.
type cfnAPI interface {
	DescribeStacks(ctx context.Context, in *cloudformation.DescribeStacksInput, opts ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error)
	DescribeStackEvents(ctx context.Context, in *cloudformation.DescribeStackEventsInput, opts ...func(*cloudformation.Options)) (*cloudformation.DescribeStackEventsOutput, error)
	DescribeStackResources(ctx context.Context, in *cloudformation.DescribeStackResourcesInput, opts ...func(*cloudformation.Options)) (*cloudformation.DescribeStackResourcesOutput, error)
	ListStacks(ctx context.Context, in *cloudformation.ListStacksInput, opts ...func(*cloudformation.Options)) (*cloudformation.ListStacksOutput, error)
}

// activeStackStatuses are the CFN stack states ListStacks filters to. Per
// ADR-0035 we exclude deleted and rolled-back-then-deleted stacks so the
// listing matches what an operator would consider "live".
var activeStackStatuses = []cfntypes.StackStatus{
	cfntypes.StackStatusCreateInProgress,
	cfntypes.StackStatusCreateFailed,
	cfntypes.StackStatusCreateComplete,
	cfntypes.StackStatusRollbackInProgress,
	cfntypes.StackStatusRollbackFailed,
	cfntypes.StackStatusRollbackComplete,
	cfntypes.StackStatusDeleteInProgress,
	cfntypes.StackStatusDeleteFailed,
	cfntypes.StackStatusUpdateInProgress,
	cfntypes.StackStatusUpdateCompleteCleanupInProgress,
	cfntypes.StackStatusUpdateComplete,
	cfntypes.StackStatusUpdateFailed,
	cfntypes.StackStatusUpdateRollbackInProgress,
	cfntypes.StackStatusUpdateRollbackFailed,
	cfntypes.StackStatusUpdateRollbackCompleteCleanupInProgress,
	cfntypes.StackStatusUpdateRollbackComplete,
	cfntypes.StackStatusReviewInProgress,
	cfntypes.StackStatusImportInProgress,
	cfntypes.StackStatusImportComplete,
	cfntypes.StackStatusImportRollbackInProgress,
	cfntypes.StackStatusImportRollbackFailed,
	cfntypes.StackStatusImportRollbackComplete,
}

// describeStack returns a snapshot of one stack's identity, status, and
// declared parameters / outputs / tags. The LLM uses this to ground answers
// like "what's the database password parameter set to" without scraping the
// full template.
type describeStack struct{}

// Name reports the catalogue name.
func (describeStack) Name() string { return "cfn/describe-stack" }

// Permission returns a literal — the compiler enforces the boundary.
func (describeStack) Permission() tools.Permission { return tools.PermissionRead }

// Schema describes the args the LLM passes.
func (t describeStack) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Describe a single CloudFormation stack: status, parameters, outputs, tags. Pass stack_name (the stack's name or full ARN).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"stack_name": map[string]any{
					"type":        "string",
					"description": "The stack name or full ARN to describe.",
				},
			},
			"required": []string{"stack_name"},
		},
	}
}

// Execute calls CloudFormation:DescribeStacks for one stack.
func (t describeStack) Execute(ctx context.Context, args map[string]any) (any, error) {
	name, err := tools.ArgString(t.Name(), args, "stack_name", true)
	if err != nil {
		return nil, err
	}
	api, err := cfnClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	out, err := api.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(name)})
	if err != nil {
		return nil, err
	}
	stacks := make([]map[string]any, 0, len(out.Stacks))
	for _, s := range out.Stacks {
		stacks = append(stacks, summariseStack(s))
	}
	return map[string]any{"stacks": stacks}, nil
}

// summariseStack picks the fields the LLM finds useful and drops SDK-internal
// metadata so the result is small, JSON-clean, and free of *time.Time
// pointers.
func summariseStack(s cfntypes.Stack) map[string]any {
	out := map[string]any{
		"name":   aws.ToString(s.StackName),
		"status": string(s.StackStatus),
	}
	if s.StackId != nil {
		out["id"] = aws.ToString(s.StackId)
	}
	if s.StackStatusReason != nil {
		out["status_reason"] = aws.ToString(s.StackStatusReason)
	}
	if s.Description != nil {
		out["description"] = aws.ToString(s.Description)
	}
	if s.CreationTime != nil {
		out["created_at"] = s.CreationTime.UTC().Format("2006-01-02T15:04:05Z")
	}
	if s.LastUpdatedTime != nil {
		out["updated_at"] = s.LastUpdatedTime.UTC().Format("2006-01-02T15:04:05Z")
	}
	if len(s.Parameters) > 0 {
		params := make(map[string]string, len(s.Parameters))
		for _, p := range s.Parameters {
			params[aws.ToString(p.ParameterKey)] = aws.ToString(p.ParameterValue)
		}
		out["parameters"] = params
	}
	if len(s.Outputs) > 0 {
		outs := make(map[string]string, len(s.Outputs))
		for _, o := range s.Outputs {
			outs[aws.ToString(o.OutputKey)] = aws.ToString(o.OutputValue)
		}
		out["outputs"] = outs
	}
	if len(s.Tags) > 0 {
		tags := make(map[string]string, len(s.Tags))
		for _, tag := range s.Tags {
			tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
		out["tags"] = tags
	}
	return out
}

// describeStackEvents returns the most recent CFN events for a stack — the AI
// reads these when an update fails to point at the offending resource and the
// reason CloudFormation emitted.
type describeStackEvents struct{}

// Name reports the catalogue name.
func (describeStackEvents) Name() string { return "cfn/describe-stack-events" }

// Permission returns the const PermissionRead.
func (describeStackEvents) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t describeStackEvents) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Return the most recent CloudFormation stack events for the named stack, newest first. Useful for diagnosing why an update or create failed.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"stack_name": map[string]any{
					"type":        "string",
					"description": "The stack name or full ARN.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of events to return (default 50, hard cap 200).",
				},
			},
			"required": []string{"stack_name"},
		},
	}
}

// Execute calls DescribeStackEvents and truncates to the requested limit.
func (t describeStackEvents) Execute(ctx context.Context, args map[string]any) (any, error) {
	name, err := tools.ArgString(t.Name(), args, "stack_name", true)
	if err != nil {
		return nil, err
	}
	limit, err := tools.ArgInt(t.Name(), args, "limit", false)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	api, err := cfnClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	out, err := api.DescribeStackEvents(ctx, &cloudformation.DescribeStackEventsInput{StackName: aws.String(name)})
	if err != nil {
		return nil, err
	}
	events := out.StackEvents
	if len(events) > limit {
		events = events[:limit]
	}
	summary := make([]map[string]any, 0, len(events))
	for _, e := range events {
		summary = append(summary, summariseEvent(e))
	}
	return map[string]any{"events": summary}, nil
}

// summariseEvent picks the fields the LLM cares about.
func summariseEvent(e cfntypes.StackEvent) map[string]any {
	out := map[string]any{
		"event_id":           aws.ToString(e.EventId),
		"logical_resource":   aws.ToString(e.LogicalResourceId),
		"physical_resource":  aws.ToString(e.PhysicalResourceId),
		"resource_type":      aws.ToString(e.ResourceType),
		"resource_status":    string(e.ResourceStatus),
		"resource_status_re": aws.ToString(e.ResourceStatusReason),
	}
	if e.Timestamp != nil {
		out["timestamp"] = e.Timestamp.UTC().Format("2006-01-02T15:04:05Z")
	}
	return out
}

// describeStackResources lists the resources currently in a stack.
type describeStackResources struct{}

// Name reports the catalogue name.
func (describeStackResources) Name() string { return "cfn/describe-stack-resources" }

// Permission returns the const PermissionRead.
func (describeStackResources) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t describeStackResources) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "List the resources in a CloudFormation stack with their logical IDs, physical IDs, types, and current status.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"stack_name": map[string]any{
					"type":        "string",
					"description": "The stack name or full ARN.",
				},
			},
			"required": []string{"stack_name"},
		},
	}
}

// Execute calls DescribeStackResources.
func (t describeStackResources) Execute(ctx context.Context, args map[string]any) (any, error) {
	name, err := tools.ArgString(t.Name(), args, "stack_name", true)
	if err != nil {
		return nil, err
	}
	api, err := cfnClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	out, err := api.DescribeStackResources(ctx, &cloudformation.DescribeStackResourcesInput{StackName: aws.String(name)})
	if err != nil {
		return nil, err
	}
	res := make([]map[string]any, 0, len(out.StackResources))
	for _, r := range out.StackResources {
		entry := map[string]any{
			"logical_id":      aws.ToString(r.LogicalResourceId),
			"physical_id":     aws.ToString(r.PhysicalResourceId),
			"resource_type":   aws.ToString(r.ResourceType),
			"resource_status": string(r.ResourceStatus),
		}
		if r.ResourceStatusReason != nil {
			entry["status_reason"] = aws.ToString(r.ResourceStatusReason)
		}
		res = append(res, entry)
	}
	return map[string]any{"resources": res}, nil
}

// listStacks returns active stacks in the bound account, filtered to live
// states (no DELETE_COMPLETE).
type listStacks struct{}

// Name reports the catalogue name.
func (listStacks) Name() string { return "cfn/list-stacks" }

// Permission returns the const PermissionRead.
func (listStacks) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t listStacks) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "List CloudFormation stacks in the active states (excludes DELETE_COMPLETE). Returns name, status, and last-updated time.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

// Execute calls ListStacks filtered to active statuses.
func (t listStacks) Execute(ctx context.Context, _ map[string]any) (any, error) {
	api, err := cfnClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	out, err := api.ListStacks(ctx, &cloudformation.ListStacksInput{
		StackStatusFilter: activeStackStatuses,
	})
	if err != nil {
		return nil, err
	}
	res := make([]map[string]any, 0, len(out.StackSummaries))
	for _, s := range out.StackSummaries {
		entry := map[string]any{
			"name":   aws.ToString(s.StackName),
			"status": string(s.StackStatus),
		}
		if s.LastUpdatedTime != nil {
			entry["updated_at"] = s.LastUpdatedTime.UTC().Format("2006-01-02T15:04:05Z")
		} else if s.CreationTime != nil {
			entry["updated_at"] = s.CreationTime.UTC().Format("2006-01-02T15:04:05Z")
		}
		res = append(res, entry)
	}
	return map[string]any{"stacks": res}, nil
}

func init() {
	tools.MustRegister(tools.Default, describeStack{})
	tools.MustRegister(tools.Default, describeStackEvents{})
	tools.MustRegister(tools.Default, describeStackResources{})
	tools.MustRegister(tools.Default, listStacks{})
}
