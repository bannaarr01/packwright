package write

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// ecsClientFactory builds an ECS client bound to the toolset's awsx.Client.
var ecsClientFactory = func(ctx context.Context, toolName string) (ecsAPI, error) {
	c, err := tools.RequireAWSClient(ctx, toolName)
	if err != nil {
		return nil, err
	}
	cfg, err := tools.LoadAWSConfig(ctx, toolName, c)
	if err != nil {
		return nil, err
	}
	return ecs.NewFromConfig(cfg), nil
}

// ecsAPI is the subset of ECS write operations the tool calls.
type ecsAPI interface {
	UpdateService(ctx context.Context, in *ecs.UpdateServiceInput, opts ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error)
}

// updateService issues ecs:UpdateService. The AI uses it for "scale my
// service up" and "force-redeploy with the latest task definition" requests.
type updateService struct{}

// Name reports the catalogue name.
func (updateService) Name() string { return "ecs/update-service" }

// Permission returns the const PermissionWrite.
func (updateService) Permission() tools.Permission { return tools.PermissionWrite }

// Schema declares the args.
func (t updateService) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Update an ECS service: change desired_count, force a new deployment, or roll to a new task definition revision.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cluster":              map[string]any{"type": "string", "description": "Cluster name or ARN."},
				"service":              map[string]any{"type": "string", "description": "Service name or ARN."},
				"desired_count":        map[string]any{"type": "integer", "description": "New desired task count. Omit to leave unchanged."},
				"task_definition":      map[string]any{"type": "string", "description": "Task definition family:revision or ARN. Omit to leave unchanged."},
				"force_new_deployment": map[string]any{"type": "boolean", "description": "If true, force a redeploy even when no other fields changed."},
				"reason":               map[string]any{"type": "string", "description": "Why the service is being updated — surfaced in the consent modal."},
			},
			"required": []string{"cluster", "service", "reason"},
		},
	}
}

// Execute issues UpdateService.
func (t updateService) Execute(ctx context.Context, args map[string]any) (any, error) {
	cluster, err := tools.ArgString(t.Name(), args, "cluster", true)
	if err != nil {
		return nil, err
	}
	service, err := tools.ArgString(t.Name(), args, "service", true)
	if err != nil {
		return nil, err
	}
	if _, err := tools.ArgString(t.Name(), args, "reason", true); err != nil {
		return nil, err
	}
	in := &ecs.UpdateServiceInput{
		Cluster: aws.String(cluster),
		Service: aws.String(service),
	}
	if _, ok := args["desired_count"]; ok {
		n, err := tools.ArgInt(t.Name(), args, "desired_count", false)
		if err != nil {
			return nil, err
		}
		in.DesiredCount = aws.Int32(int32(n))
	}
	td, err := tools.ArgString(t.Name(), args, "task_definition", false)
	if err != nil {
		return nil, err
	}
	if td != "" {
		in.TaskDefinition = aws.String(td)
	}
	if v, ok := args["force_new_deployment"].(bool); ok {
		in.ForceNewDeployment = v
	}
	api, err := ecsClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	out, err := api.UpdateService(ctx, in)
	if err != nil {
		return nil, err
	}
	res := map[string]any{}
	if out.Service != nil {
		res["service"] = map[string]any{
			"arn":           aws.ToString(out.Service.ServiceArn),
			"status":        aws.ToString(out.Service.Status),
			"desired_count": int(out.Service.DesiredCount),
		}
	}
	return res, nil
}

func init() {
	tools.MustRegister(tools.Default, updateService{})
}
