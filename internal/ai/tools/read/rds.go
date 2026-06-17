package read

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// rdsClientFactory builds an RDS client bound to the toolset's awsx.Client.
var rdsClientFactory = func(ctx context.Context, toolName string) (rdsAPI, error) {
	c, err := tools.RequireAWSClient(ctx, toolName)
	if err != nil {
		return nil, err
	}
	cfg, err := tools.LoadAWSConfig(ctx, toolName, c)
	if err != nil {
		return nil, err
	}
	return rds.NewFromConfig(cfg), nil
}

// rdsAPI is the subset of RDS operations the read tools call.
type rdsAPI interface {
	DescribeDBInstances(ctx context.Context, in *rds.DescribeDBInstancesInput, opts ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
}

// describeDBInstance summarises one or more RDS instances.
type describeDBInstance struct{}

// Name reports the catalogue name.
func (describeDBInstance) Name() string { return "rds/describe-db-instance" }

// Permission returns the const PermissionRead.
func (describeDBInstance) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t describeDBInstance) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Describe an RDS DB instance. Returns engine, status, endpoint, storage, and multi-AZ posture. Pass db_instance_id to filter to one instance, or leave empty for all.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"db_instance_id": map[string]any{
					"type":        "string",
					"description": "DB instance identifier or ARN. Empty to return every instance.",
				},
			},
		},
	}
}

// Execute issues DescribeDBInstances.
func (t describeDBInstance) Execute(ctx context.Context, args map[string]any) (any, error) {
	id, err := tools.ArgString(t.Name(), args, "db_instance_id", false)
	if err != nil {
		return nil, err
	}
	api, err := rdsClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	in := &rds.DescribeDBInstancesInput{}
	if id != "" {
		in.DBInstanceIdentifier = aws.String(id)
	}
	out, err := api.DescribeDBInstances(ctx, in)
	if err != nil {
		return nil, err
	}
	res := make([]map[string]any, 0, len(out.DBInstances))
	for _, db := range out.DBInstances {
		entry := map[string]any{
			"id":             aws.ToString(db.DBInstanceIdentifier),
			"arn":            aws.ToString(db.DBInstanceArn),
			"engine":         aws.ToString(db.Engine),
			"engine_version": aws.ToString(db.EngineVersion),
			"status":         aws.ToString(db.DBInstanceStatus),
			"instance_class": aws.ToString(db.DBInstanceClass),
		}
		if db.AllocatedStorage != nil {
			entry["allocated_storage_gib"] = int(*db.AllocatedStorage)
		}
		if db.MultiAZ != nil {
			entry["multi_az"] = *db.MultiAZ
		}
		if db.Endpoint != nil {
			entry["endpoint"] = map[string]any{
				"address": aws.ToString(db.Endpoint.Address),
				"port":    int(aws.ToInt32(db.Endpoint.Port)),
			}
		}
		res = append(res, entry)
	}
	return map[string]any{"db_instances": res}, nil
}

func init() {
	tools.MustRegister(tools.Default, describeDBInstance{})
}
