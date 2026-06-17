package scanners

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/bannaarr01/packwright/internal/audit"
)

// EC2NATGateway enumerates every NAT gateway in the audit Client's region.
type EC2NATGateway struct{}

// Kind reports the stable kind identifier.
func (EC2NATGateway) Kind() string { return "ec2/nat-gateway" }

// Permissions reports the IAM actions Scan touches.
func (EC2NATGateway) Permissions() []string { return []string{"ec2:DescribeNatGateways"} }

// Scan walks DescribeNatGateways paginators and returns one Resource
// per NAT gateway, fully paginated.
func (EC2NATGateway) Scan(ctx context.Context, c *audit.Client, emit audit.ScannerEmitter) ([]audit.Resource, error) {
	api := c.EC2()
	if api == nil {
		return nil, fmt.Errorf("ec2/nat-gateway: ec2 client is not configured")
	}
	tb := c.Throttle("ec2")

	var out []audit.Resource
	pager := ec2.NewDescribeNatGatewaysPaginator(api, &ec2.DescribeNatGatewaysInput{})
	for pager.HasMorePages() {
		if err := tb.Wait(ctx); err != nil {
			return out, err
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			return out, fmt.Errorf("ec2/nat-gateway: describing nat gateways: %w", err)
		}
		for _, g := range page.NatGateways {
			tags := ec2TagsToMap(g.Tags)
			res := audit.Resource{
				Kind:    "ec2/nat-gateway",
				ID:      aws.ToString(g.NatGatewayId),
				Region:  c.Region(),
				Account: c.Account(),
				Name:    tags["Name"],
				Tags:    tags,
				State:   string(g.State),
			}
			if g.CreateTime != nil {
				res.CreatedAt = *g.CreateTime
			}
			out = append(out, res)
		}
		emit.Progress(len(out))
	}
	return out, nil
}

func init() { audit.Register(EC2NATGateway{}) }
