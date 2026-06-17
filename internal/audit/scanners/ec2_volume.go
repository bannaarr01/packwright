package scanners

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/bannaarr01/packwright/internal/audit"
)

// EC2Volume enumerates every EBS volume in the audit Client's region.
type EC2Volume struct{}

// Kind reports the stable kind identifier.
func (EC2Volume) Kind() string { return "ec2/volume" }

// Permissions reports the IAM actions Scan touches.
func (EC2Volume) Permissions() []string { return []string{"ec2:DescribeVolumes"} }

// Scan walks DescribeVolumes paginators and returns one Resource per
// EBS volume, fully paginated.
func (EC2Volume) Scan(ctx context.Context, c *audit.Client, emit audit.ScannerEmitter) ([]audit.Resource, error) {
	api := c.EC2()
	if api == nil {
		return nil, fmt.Errorf("ec2/volume: ec2 client is not configured")
	}
	tb := c.Throttle("ec2")

	var out []audit.Resource
	pager := ec2.NewDescribeVolumesPaginator(api, &ec2.DescribeVolumesInput{})
	for pager.HasMorePages() {
		if err := tb.Wait(ctx); err != nil {
			return out, err
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			return out, fmt.Errorf("ec2/volume: describing volumes: %w", err)
		}
		for _, v := range page.Volumes {
			tags := ec2TagsToMap(v.Tags)
			res := audit.Resource{
				Kind:    "ec2/volume",
				ID:      aws.ToString(v.VolumeId),
				Region:  c.Region(),
				Account: c.Account(),
				Name:    tags["Name"],
				Tags:    tags,
				State:   string(v.State),
			}
			if v.CreateTime != nil {
				res.CreatedAt = *v.CreateTime
			}
			out = append(out, res)
		}
		emit.Progress(len(out))
	}
	return out, nil
}

func init() { audit.Register(EC2Volume{}) }
