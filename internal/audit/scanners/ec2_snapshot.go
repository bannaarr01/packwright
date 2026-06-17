package scanners

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/bannaarr01/packwright/internal/audit"
)

// EC2Snapshot enumerates every account-owned EBS snapshot in the audit
// Client's region. The "self" filter is mandatory — without it
// DescribeSnapshots returns every public snapshot in the region,
// hundreds of thousands of rows that we have no business inventorying.
type EC2Snapshot struct{}

// Kind reports the stable kind identifier.
func (EC2Snapshot) Kind() string { return "ec2/snapshot" }

// Permissions reports the IAM actions Scan touches.
func (EC2Snapshot) Permissions() []string { return []string{"ec2:DescribeSnapshots"} }

// Scan walks DescribeSnapshots paginators filtered to OwnerIds=["self"]
// and returns one Resource per snapshot, fully paginated.
func (EC2Snapshot) Scan(ctx context.Context, c *audit.Client, emit audit.ScannerEmitter) ([]audit.Resource, error) {
	api := c.EC2()
	if api == nil {
		return nil, fmt.Errorf("ec2/snapshot: ec2 client is not configured")
	}
	tb := c.Throttle("ec2")

	var out []audit.Resource
	pager := ec2.NewDescribeSnapshotsPaginator(api, &ec2.DescribeSnapshotsInput{
		OwnerIds: []string{"self"},
	})
	for pager.HasMorePages() {
		if err := tb.Wait(ctx); err != nil {
			return out, err
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			return out, fmt.Errorf("ec2/snapshot: describing snapshots: %w", err)
		}
		for _, s := range page.Snapshots {
			tags := ec2TagsToMap(s.Tags)
			res := audit.Resource{
				Kind:    "ec2/snapshot",
				ID:      aws.ToString(s.SnapshotId),
				Region:  c.Region(),
				Account: c.Account(),
				Name:    tags["Name"],
				Tags:    tags,
				State:   string(s.State),
			}
			if s.StartTime != nil {
				res.CreatedAt = *s.StartTime
			}
			out = append(out, res)
		}
		emit.Progress(len(out))
	}
	return out, nil
}

func init() { audit.Register(EC2Snapshot{}) }
