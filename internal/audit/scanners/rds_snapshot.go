package scanners

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"

	"github.com/bannaarr01/packwright/internal/audit"
)

// RDSDBSnapshot enumerates every RDS DB snapshot in the audit Client's
// region.
type RDSDBSnapshot struct{}

// Kind reports the stable kind identifier.
func (RDSDBSnapshot) Kind() string { return "rds/db-snapshot" }

// Permissions reports the IAM actions Scan touches.
func (RDSDBSnapshot) Permissions() []string { return []string{"rds:DescribeDBSnapshots"} }

// Scan walks DescribeDBSnapshots paginators and returns one Resource
// per snapshot, fully paginated.
func (RDSDBSnapshot) Scan(ctx context.Context, c *audit.Client, emit audit.ScannerEmitter) ([]audit.Resource, error) {
	api := c.RDS()
	if api == nil {
		return nil, fmt.Errorf("rds/db-snapshot: rds client is not configured")
	}
	tb := c.Throttle("rds")

	var out []audit.Resource
	pager := rds.NewDescribeDBSnapshotsPaginator(api, &rds.DescribeDBSnapshotsInput{})
	for pager.HasMorePages() {
		if err := tb.Wait(ctx); err != nil {
			return out, err
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			return out, fmt.Errorf("rds/db-snapshot: describing db snapshots: %w", err)
		}
		for _, s := range page.DBSnapshots {
			res := audit.Resource{
				Kind:    "rds/db-snapshot",
				ID:      aws.ToString(s.DBSnapshotArn),
				Region:  c.Region(),
				Account: c.Account(),
				Name:    aws.ToString(s.DBSnapshotIdentifier),
				Tags:    rdsTagsToMap(s.TagList),
				State:   aws.ToString(s.Status),
			}
			if s.SnapshotCreateTime != nil {
				res.CreatedAt = *s.SnapshotCreateTime
			}
			out = append(out, res)
		}
		emit.Progress(len(out))
	}
	return out, nil
}

func init() { audit.Register(RDSDBSnapshot{}) }
