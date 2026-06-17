package read

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

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

// ecsAPI is the subset of ECS operations the read tools call.
type ecsAPI interface {
	DescribeClusters(ctx context.Context, in *ecs.DescribeClustersInput, opts ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error)
	DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, opts ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	DescribeTasks(ctx context.Context, in *ecs.DescribeTasksInput, opts ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
	DescribeTaskDefinition(ctx context.Context, in *ecs.DescribeTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error)
}

// describeCluster summarises one or more ECS clusters.
type describeCluster struct{}

// Name reports the catalogue name.
func (describeCluster) Name() string { return "ecs/describe-cluster" }

// Permission returns the const PermissionRead.
func (describeCluster) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t describeCluster) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Describe one or more ECS clusters by name or ARN. Returns status, running/pending task counts, and registered container instances.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"clusters": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Cluster names or ARNs.",
				},
			},
			"required": []string{"clusters"},
		},
	}
}

// Execute issues DescribeClusters.
func (t describeCluster) Execute(ctx context.Context, args map[string]any) (any, error) {
	clusters, err := tools.ArgStringSlice(t.Name(), args, "clusters", true)
	if err != nil {
		return nil, err
	}
	api, err := ecsClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	out, err := api.DescribeClusters(ctx, &ecs.DescribeClustersInput{
		Clusters: clusters,
		Include:  []ecstypes.ClusterField{ecstypes.ClusterFieldStatistics},
	})
	if err != nil {
		return nil, err
	}
	res := make([]map[string]any, 0, len(out.Clusters))
	for _, c := range out.Clusters {
		res = append(res, map[string]any{
			"name":                 aws.ToString(c.ClusterName),
			"arn":                  aws.ToString(c.ClusterArn),
			"status":               aws.ToString(c.Status),
			"active_services":      int(c.ActiveServicesCount),
			"running_tasks":        int(c.RunningTasksCount),
			"pending_tasks":        int(c.PendingTasksCount),
			"registered_instances": int(c.RegisteredContainerInstancesCount),
		})
	}
	return map[string]any{"clusters": res}, nil
}

// describeService summarises one or more services in a cluster.
type describeService struct{}

// Name reports the catalogue name.
func (describeService) Name() string { return "ecs/describe-service" }

// Permission returns the const PermissionRead.
func (describeService) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t describeService) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Describe one or more ECS services. Returns desired/running counts, deployment state, and task definition ARN.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cluster": map[string]any{"type": "string", "description": "Cluster name or ARN."},
				"services": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Service names or ARNs (up to 10).",
				},
			},
			"required": []string{"cluster", "services"},
		},
	}
}

// Execute issues DescribeServices.
func (t describeService) Execute(ctx context.Context, args map[string]any) (any, error) {
	cluster, err := tools.ArgString(t.Name(), args, "cluster", true)
	if err != nil {
		return nil, err
	}
	services, err := tools.ArgStringSlice(t.Name(), args, "services", true)
	if err != nil {
		return nil, err
	}
	api, err := ecsClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	out, err := api.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster:  aws.String(cluster),
		Services: services,
	})
	if err != nil {
		return nil, err
	}
	res := make([]map[string]any, 0, len(out.Services))
	for _, s := range out.Services {
		entry := map[string]any{
			"name":            aws.ToString(s.ServiceName),
			"arn":             aws.ToString(s.ServiceArn),
			"status":          aws.ToString(s.Status),
			"desired_count":   int(s.DesiredCount),
			"running_count":   int(s.RunningCount),
			"pending_count":   int(s.PendingCount),
			"task_definition": aws.ToString(s.TaskDefinition),
			"launch_type":     string(s.LaunchType),
		}
		if s.CreatedAt != nil {
			entry["created_at"] = s.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		res = append(res, entry)
	}
	return map[string]any{"services": res}, nil
}

// describeTasks returns task instances and (optionally) the task definition.
type describeTasks struct{}

// Name reports the catalogue name.
func (describeTasks) Name() string { return "ecs/describe-tasks" }

// Permission returns the const PermissionRead.
func (describeTasks) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t describeTasks) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Describe ECS tasks by ID or ARN. Returns last status, desired status, container exits, and the task definition family/revision.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cluster": map[string]any{"type": "string", "description": "Cluster name or ARN."},
				"tasks": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Task IDs or ARNs.",
				},
				"include_task_definition": map[string]any{
					"type":        "boolean",
					"description": "If true, also fetch the underlying task definition. Default false.",
				},
			},
			"required": []string{"cluster", "tasks"},
		},
	}
}

// Execute issues DescribeTasks and, when asked, DescribeTaskDefinition.
func (t describeTasks) Execute(ctx context.Context, args map[string]any) (any, error) {
	cluster, err := tools.ArgString(t.Name(), args, "cluster", true)
	if err != nil {
		return nil, err
	}
	taskIDs, err := tools.ArgStringSlice(t.Name(), args, "tasks", true)
	if err != nil {
		return nil, err
	}
	includeDef := false
	if v, ok := args["include_task_definition"].(bool); ok {
		includeDef = v
	}
	api, err := ecsClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	out, err := api.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(cluster),
		Tasks:   taskIDs,
	})
	if err != nil {
		return nil, err
	}

	tasks := make([]map[string]any, 0, len(out.Tasks))
	defArns := make(map[string]struct{})
	for _, ts := range out.Tasks {
		containers := make([]map[string]any, 0, len(ts.Containers))
		for _, c := range ts.Containers {
			cn := map[string]any{
				"name":          aws.ToString(c.Name),
				"last_status":   aws.ToString(c.LastStatus),
				"health_status": string(c.HealthStatus),
			}
			if c.ExitCode != nil {
				cn["exit_code"] = int(*c.ExitCode)
			}
			if c.Reason != nil {
				cn["reason"] = aws.ToString(c.Reason)
			}
			containers = append(containers, cn)
		}
		entry := map[string]any{
			"task_arn":        aws.ToString(ts.TaskArn),
			"task_definition": aws.ToString(ts.TaskDefinitionArn),
			"last_status":     aws.ToString(ts.LastStatus),
			"desired_status":  aws.ToString(ts.DesiredStatus),
			"stop_code":       string(ts.StopCode),
			"stopped_reason":  aws.ToString(ts.StoppedReason),
			"containers":      containers,
		}
		tasks = append(tasks, entry)
		if includeDef && ts.TaskDefinitionArn != nil {
			defArns[*ts.TaskDefinitionArn] = struct{}{}
		}
	}

	defs := make([]map[string]any, 0, len(defArns))
	for arn := range defArns {
		def, err := api.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{
			TaskDefinition: aws.String(arn),
		})
		if err != nil {
			return nil, err
		}
		td := def.TaskDefinition
		if td == nil {
			continue
		}
		defs = append(defs, map[string]any{
			"family":       aws.ToString(td.Family),
			"revision":     int(td.Revision),
			"arn":          aws.ToString(td.TaskDefinitionArn),
			"cpu":          aws.ToString(td.Cpu),
			"memory":       aws.ToString(td.Memory),
			"network_mode": string(td.NetworkMode),
		})
	}
	return map[string]any{
		"tasks":            tasks,
		"task_definitions": defs,
	}, nil
}

func init() {
	tools.MustRegister(tools.Default, describeCluster{})
	tools.MustRegister(tools.Default, describeService{})
	tools.MustRegister(tools.Default, describeTasks{})
}
