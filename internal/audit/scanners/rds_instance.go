package scanners

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"

	"github.com/bannaarr01/packwright/internal/audit"
)

// RDSDBInstance enumerates every RDS DB instance in the audit Client's
// region.
type RDSDBInstance struct{}

// Kind reports the stable kind identifier.
func (RDSDBInstance) Kind() string { return "rds/db-instance" }

// Permissions reports the IAM actions Scan touches.
func (RDSDBInstance) Permissions() []string { return []string{"rds:DescribeDBInstances"} }

// Scan walks DescribeDBInstances paginators and returns one Resource
// per DB instance, fully paginated.
func (RDSDBInstance) Scan(ctx context.Context, c *audit.Client, emit audit.ScannerEmitter) ([]audit.Resource, error) {
	api := c.RDS()
	if api == nil {
		return nil, fmt.Errorf("rds/db-instance: rds client is not configured")
	}
	tb := c.Throttle("rds")

	var out []audit.Resource
	pager := rds.NewDescribeDBInstancesPaginator(api, &rds.DescribeDBInstancesInput{})
	for pager.HasMorePages() {
		if err := tb.Wait(ctx); err != nil {
			return out, err
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			return out, fmt.Errorf("rds/db-instance: describing db instances: %w", err)
		}
		for _, db := range page.DBInstances {
			res := audit.Resource{
				Kind:    "rds/db-instance",
				ID:      aws.ToString(db.DBInstanceArn),
				Region:  c.Region(),
				Account: c.Account(),
				Name:    aws.ToString(db.DBInstanceIdentifier),
				Tags:    rdsTagsToMap(db.TagList),
				State:   aws.ToString(db.DBInstanceStatus),
			}
			if db.InstanceCreateTime != nil {
				res.CreatedAt = *db.InstanceCreateTime
			}
			out = append(out, res)
		}
		emit.Progress(len(out))
	}
	return out, nil
}

// rdsTagsToMap collapses an RDS tag slice into a {key: value} map.
func rdsTagsToMap(tags []rdstypes.Tag) map[string]string {
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

func init() { audit.Register(RDSDBInstance{}) }
