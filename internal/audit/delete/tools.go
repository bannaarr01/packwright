package delete

import (
	"context"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// clientFactory builds the AWS service bundle for an AI-invoked
// audit/delete-* tool. Tests replace it with a fake builder so the
// tool's Execute path can be exercised without real AWS credentials.
var clientFactory = func(ctx context.Context, toolName string) (*Clients, error) {
	c, err := tools.RequireAWSClient(ctx, toolName)
	if err != nil {
		return nil, err
	}
	return NewClients(ctx, toolName, c)
}

// runTool is the shared body of every audit/delete-* tool's Execute.
// It enforces the "reason" arg required by ADR-0036 / consent.Gate
// before any AWS call, builds the AWS client bundle, then dispatches
// to DeleteResource — the same helper the human batch executor uses.
//
// The arg map is required to contain a non-empty "reason"; the
// consent.Gate that fronts every PermissionWrite tool also enforces
// this, but checking again here yields a clearer ErrCodeBadArgs from
// the tool itself if the caller bypasses the gate (e.g. tests).
func runTool(ctx context.Context, toolName string, args map[string]any, res Resource) (any, error) {
	if _, err := tools.ArgString(toolName, args, "reason", true); err != nil {
		return nil, err
	}
	c, err := clientFactory(ctx, toolName)
	if err != nil {
		return nil, err
	}
	if err := DeleteResource(ctx, c, res); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true}, nil
}

// Each tool below mirrors the pattern from internal/ai/tools/write/cfn.go:
// an unexported zero-size struct that implements tools.Tool. The init()
// at the bottom registers them all into tools.Default so the AI dispatch
// loop discovers them automatically.
//
// Argument keys deliberately match the AWS handle the resource uses so
// the LLM's prompt is unsurprising:
//   audit/delete-volume        → volume_id
//   audit/delete-snapshot      → snapshot_id
//   audit/release-eip          → allocation_id
//   audit/delete-nat-gateway   → nat_gateway_id
//   audit/delete-target-group  → target_group_arn
//   audit/delete-log-group     → log_group_name
//   audit/delete-rds-snapshot  → db_snapshot_id
//   audit/delete-ecr-image     → repository_name, image_digest

type deleteVolumeTool struct{}

func (deleteVolumeTool) Name() string                 { return "audit/delete-volume" }
func (deleteVolumeTool) Permission() tools.Permission { return tools.PermissionWrite }
func (t deleteVolumeTool) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Delete an unused EBS volume.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"volume_id": map[string]any{"type": "string", "description": "EBS volume ID (vol-...)."},
				"reason":    map[string]any{"type": "string", "description": "Why the volume is being deleted."},
			},
			"required": []string{"volume_id", "reason"},
		},
	}
}
func (t deleteVolumeTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	id, err := tools.ArgString(t.Name(), args, "volume_id", true)
	if err != nil {
		return nil, err
	}
	return runTool(ctx, t.Name(), args, Resource{Kind: KindEC2Volume, Identifier: id})
}

type deleteSnapshotTool struct{}

func (deleteSnapshotTool) Name() string                 { return "audit/delete-snapshot" }
func (deleteSnapshotTool) Permission() tools.Permission { return tools.PermissionWrite }
func (t deleteSnapshotTool) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Delete an EBS snapshot. Fails if any AMI still references it.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"snapshot_id": map[string]any{"type": "string", "description": "EBS snapshot ID (snap-...)."},
				"reason":      map[string]any{"type": "string", "description": "Why the snapshot is being deleted."},
			},
			"required": []string{"snapshot_id", "reason"},
		},
	}
}
func (t deleteSnapshotTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	id, err := tools.ArgString(t.Name(), args, "snapshot_id", true)
	if err != nil {
		return nil, err
	}
	return runTool(ctx, t.Name(), args, Resource{Kind: KindEC2Snapshot, Identifier: id})
}

type releaseEIPTool struct{}

func (releaseEIPTool) Name() string                 { return "audit/release-eip" }
func (releaseEIPTool) Permission() tools.Permission { return tools.PermissionWrite }
func (t releaseEIPTool) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Release an unused Elastic IP allocation.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"allocation_id": map[string]any{"type": "string", "description": "EIP allocation ID (eipalloc-...)."},
				"reason":        map[string]any{"type": "string", "description": "Why the EIP is being released."},
			},
			"required": []string{"allocation_id", "reason"},
		},
	}
}
func (t releaseEIPTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	id, err := tools.ArgString(t.Name(), args, "allocation_id", true)
	if err != nil {
		return nil, err
	}
	return runTool(ctx, t.Name(), args, Resource{Kind: KindEC2EIP, Identifier: id})
}

type deleteNATGatewayTool struct{}

func (deleteNATGatewayTool) Name() string                 { return "audit/delete-nat-gateway" }
func (deleteNATGatewayTool) Permission() tools.Permission { return tools.PermissionWrite }
func (t deleteNATGatewayTool) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Delete a NAT gateway. Existing routes that reference it become orphans.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"nat_gateway_id": map[string]any{"type": "string", "description": "NAT gateway ID (nat-...)."},
				"reason":         map[string]any{"type": "string", "description": "Why the NAT gateway is being deleted."},
			},
			"required": []string{"nat_gateway_id", "reason"},
		},
	}
}
func (t deleteNATGatewayTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	id, err := tools.ArgString(t.Name(), args, "nat_gateway_id", true)
	if err != nil {
		return nil, err
	}
	return runTool(ctx, t.Name(), args, Resource{Kind: KindEC2NATGateway, Identifier: id})
}

type deleteTargetGroupTool struct{}

func (deleteTargetGroupTool) Name() string                 { return "audit/delete-target-group" }
func (deleteTargetGroupTool) Permission() tools.Permission { return tools.PermissionWrite }
func (t deleteTargetGroupTool) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Delete an ELBv2 target group. Fails if any listener still forwards to it.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target_group_arn": map[string]any{"type": "string", "description": "Target group ARN."},
				"reason":           map[string]any{"type": "string", "description": "Why the target group is being deleted."},
			},
			"required": []string{"target_group_arn", "reason"},
		},
	}
}
func (t deleteTargetGroupTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	arn, err := tools.ArgString(t.Name(), args, "target_group_arn", true)
	if err != nil {
		return nil, err
	}
	return runTool(ctx, t.Name(), args, Resource{Kind: KindELBv2TargetGroup, Identifier: arn})
}

type deleteLogGroupTool struct{}

func (deleteLogGroupTool) Name() string                 { return "audit/delete-log-group" }
func (deleteLogGroupTool) Permission() tools.Permission { return tools.PermissionWrite }
func (t deleteLogGroupTool) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Delete a CloudWatch Logs log group. Deletion is immediate and irrecoverable.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"log_group_name": map[string]any{"type": "string", "description": "Log group name (e.g. /aws/lambda/foo)."},
				"reason":         map[string]any{"type": "string", "description": "Why the log group is being deleted."},
			},
			"required": []string{"log_group_name", "reason"},
		},
	}
}
func (t deleteLogGroupTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	name, err := tools.ArgString(t.Name(), args, "log_group_name", true)
	if err != nil {
		return nil, err
	}
	return runTool(ctx, t.Name(), args, Resource{Kind: KindLogsLogGroup, Identifier: name})
}

type deleteRDSSnapshotTool struct{}

func (deleteRDSSnapshotTool) Name() string                 { return "audit/delete-rds-snapshot" }
func (deleteRDSSnapshotTool) Permission() tools.Permission { return tools.PermissionWrite }
func (t deleteRDSSnapshotTool) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Delete a manual or automated RDS DB snapshot.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"db_snapshot_id": map[string]any{"type": "string", "description": "RDS DB snapshot identifier."},
				"reason":         map[string]any{"type": "string", "description": "Why the snapshot is being deleted."},
			},
			"required": []string{"db_snapshot_id", "reason"},
		},
	}
}
func (t deleteRDSSnapshotTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	id, err := tools.ArgString(t.Name(), args, "db_snapshot_id", true)
	if err != nil {
		return nil, err
	}
	return runTool(ctx, t.Name(), args, Resource{Kind: KindRDSDBSnapshot, Identifier: id})
}

type deleteECRImageTool struct{}

func (deleteECRImageTool) Name() string                 { return "audit/delete-ecr-image" }
func (deleteECRImageTool) Permission() tools.Permission { return tools.PermissionWrite }
func (t deleteECRImageTool) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Delete a single ECR image by digest from the given repository.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repository_name": map[string]any{"type": "string", "description": "ECR repository name."},
				"image_digest":    map[string]any{"type": "string", "description": "Image digest (sha256:...)."},
				"reason":          map[string]any{"type": "string", "description": "Why the image is being deleted."},
			},
			"required": []string{"repository_name", "image_digest", "reason"},
		},
	}
}
func (t deleteECRImageTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	repo, err := tools.ArgString(t.Name(), args, "repository_name", true)
	if err != nil {
		return nil, err
	}
	digest, err := tools.ArgString(t.Name(), args, "image_digest", true)
	if err != nil {
		return nil, err
	}
	return runTool(ctx, t.Name(), args, Resource{
		Kind:       KindECRImage,
		Identifier: digest,
		Extra:      map[string]string{"repository_name": repo},
	})
}

// init registers every audit/delete-* tool with the shared catalogue.
// Importing this package — or transitively via internal/ai/tools/all
// once that import is added — is enough to make the tools visible to
// the AI dispatch loop.
//
// Per ADR-0043 no tool is registered for RDS DB instances, S3
// buckets, or KMS keys; those kinds are out of scope for MVP 6.
func init() {
	tools.MustRegister(tools.Default, deleteVolumeTool{})
	tools.MustRegister(tools.Default, deleteSnapshotTool{})
	tools.MustRegister(tools.Default, releaseEIPTool{})
	tools.MustRegister(tools.Default, deleteNATGatewayTool{})
	tools.MustRegister(tools.Default, deleteTargetGroupTool{})
	tools.MustRegister(tools.Default, deleteLogGroupTool{})
	tools.MustRegister(tools.Default, deleteRDSSnapshotTool{})
	tools.MustRegister(tools.Default, deleteECRImageTool{})
}
