package read

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/efs"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// efsClientFactory builds an EFS client bound to the toolset's awsx.Client.
var efsClientFactory = func(ctx context.Context, toolName string) (efsAPI, error) {
	c, err := tools.RequireAWSClient(ctx, toolName)
	if err != nil {
		return nil, err
	}
	cfg, err := tools.LoadAWSConfig(ctx, toolName, c)
	if err != nil {
		return nil, err
	}
	return efs.NewFromConfig(cfg), nil
}

// efsAPI is the subset of EFS operations the read tool calls.
type efsAPI interface {
	DescribeFileSystems(ctx context.Context, in *efs.DescribeFileSystemsInput, opts ...func(*efs.Options)) (*efs.DescribeFileSystemsOutput, error)
	DescribeAccessPoints(ctx context.Context, in *efs.DescribeAccessPointsInput, opts ...func(*efs.Options)) (*efs.DescribeAccessPointsOutput, error)
}

// describeFileSystem summarises an EFS file system together with its access
// points. The AI uses it when diagnosing storage mount problems.
type describeFileSystem struct{}

// Name reports the catalogue name.
func (describeFileSystem) Name() string { return "efs/describe-file-system" }

// Permission returns the const PermissionRead.
func (describeFileSystem) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t describeFileSystem) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Describe an EFS file system and its access points. Pass file_system_id to filter to one; empty returns every file system in the region.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_system_id": map[string]any{
					"type":        "string",
					"description": "EFS file system ID (fs-...). Empty returns every file system.",
				},
			},
		},
	}
}

// Execute issues DescribeFileSystems and DescribeAccessPoints.
func (t describeFileSystem) Execute(ctx context.Context, args map[string]any) (any, error) {
	id, err := tools.ArgString(t.Name(), args, "file_system_id", false)
	if err != nil {
		return nil, err
	}
	api, err := efsClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	fsIn := &efs.DescribeFileSystemsInput{}
	if id != "" {
		fsIn.FileSystemId = aws.String(id)
	}
	fsOut, err := api.DescribeFileSystems(ctx, fsIn)
	if err != nil {
		return nil, err
	}
	systems := make([]map[string]any, 0, len(fsOut.FileSystems))
	for _, fs := range fsOut.FileSystems {
		entry := map[string]any{
			"file_system_id":   aws.ToString(fs.FileSystemId),
			"name":             aws.ToString(fs.Name),
			"lifecycle_state":  string(fs.LifeCycleState),
			"performance_mode": string(fs.PerformanceMode),
			"throughput_mode":  string(fs.ThroughputMode),
			"mount_targets":    int(fs.NumberOfMountTargets),
		}
		if fs.SizeInBytes != nil {
			entry["size_bytes"] = fs.SizeInBytes.Value
		}
		apOut, err := api.DescribeAccessPoints(ctx, &efs.DescribeAccessPointsInput{
			FileSystemId: fs.FileSystemId,
		})
		if err != nil {
			return nil, err
		}
		aps := make([]map[string]any, 0, len(apOut.AccessPoints))
		for _, ap := range apOut.AccessPoints {
			ape := map[string]any{
				"access_point_id": aws.ToString(ap.AccessPointId),
				"name":            aws.ToString(ap.Name),
				"lifecycle_state": string(ap.LifeCycleState),
			}
			if ap.RootDirectory != nil {
				ape["root_path"] = aws.ToString(ap.RootDirectory.Path)
			}
			aps = append(aps, ape)
		}
		entry["access_points"] = aps
		systems = append(systems, entry)
	}
	return map[string]any{"file_systems": systems}, nil
}

func init() {
	tools.MustRegister(tools.Default, describeFileSystem{})
}
