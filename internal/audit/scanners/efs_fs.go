package scanners

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"

	"github.com/bannaarr01/packwright/internal/audit"
)

// EFSFileSystem enumerates every EFS file system in the audit Client's
// region.
type EFSFileSystem struct{}

// Kind reports the stable kind identifier.
func (EFSFileSystem) Kind() string { return "efs/file-system" }

// Permissions reports the IAM actions Scan touches.
func (EFSFileSystem) Permissions() []string { return []string{"elasticfilesystem:DescribeFileSystems"} }

// Scan walks DescribeFileSystems pages and returns one Resource per EFS
// file system. The EFS SDK exposes DescribeFileSystems but no
// dedicated paginator helper, so we follow the Marker by hand — the
// loop is still "full pagination" per ADR-0040.
func (EFSFileSystem) Scan(ctx context.Context, c *audit.Client, emit audit.ScannerEmitter) ([]audit.Resource, error) {
	api := c.EFS()
	if api == nil {
		return nil, fmt.Errorf("efs/file-system: efs client is not configured")
	}
	tb := c.Throttle("efs")

	var out []audit.Resource
	var marker *string
	for {
		if err := tb.Wait(ctx); err != nil {
			return out, err
		}
		page, err := api.DescribeFileSystems(ctx, &efs.DescribeFileSystemsInput{Marker: marker})
		if err != nil {
			return out, fmt.Errorf("efs/file-system: describing file systems: %w", err)
		}
		for _, fs := range page.FileSystems {
			tags := efsTagsToMap(fs.Tags)
			name := aws.ToString(fs.Name)
			if name == "" {
				name = tags["Name"]
			}
			res := audit.Resource{
				Kind:    "efs/file-system",
				ID:      aws.ToString(fs.FileSystemArn),
				Region:  c.Region(),
				Account: c.Account(),
				Name:    name,
				Tags:    tags,
				State:   string(fs.LifeCycleState),
			}
			if fs.CreationTime != nil {
				res.CreatedAt = *fs.CreationTime
			}
			out = append(out, res)
		}
		emit.Progress(len(out))
		if aws.ToString(page.NextMarker) == "" {
			return out, nil
		}
		marker = page.NextMarker
	}
}

// efsTagsToMap collapses an EFS tag slice into a {key: value} map.
func efsTagsToMap(tags []efstypes.Tag) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	out := make(map[string]string, len(tags))
	for _, t := range tags {
		k := aws.ToString(t.Key)
		if k == "" {
			continue
		}
		out[k] = aws.ToString(t.Value)
	}
	return out
}

func init() { audit.Register(EFSFileSystem{}) }
