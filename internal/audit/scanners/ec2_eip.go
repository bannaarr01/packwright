package scanners

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/bannaarr01/packwright/internal/audit"
)

// EC2EIP enumerates every Elastic IP allocation in the audit Client's
// region. DescribeAddresses is not paginated by the SDK (it returns the
// full list in one call), so this scanner makes a single request.
type EC2EIP struct{}

// Kind reports the stable kind identifier.
func (EC2EIP) Kind() string { return "ec2/eip" }

// Permissions reports the IAM actions Scan touches.
func (EC2EIP) Permissions() []string { return []string{"ec2:DescribeAddresses"} }

// Scan calls DescribeAddresses once and returns one Resource per EIP.
// The SDK does not provide a paginator for this operation because the
// API does not return a NextToken.
func (EC2EIP) Scan(ctx context.Context, c *audit.Client, emit audit.ScannerEmitter) ([]audit.Resource, error) {
	api := c.EC2()
	if api == nil {
		return nil, fmt.Errorf("ec2/eip: ec2 client is not configured")
	}
	tb := c.Throttle("ec2")
	if err := tb.Wait(ctx); err != nil {
		return nil, err
	}
	page, err := api.DescribeAddresses(ctx, &ec2.DescribeAddressesInput{})
	if err != nil {
		return nil, fmt.Errorf("ec2/eip: describing addresses: %w", err)
	}
	out := make([]audit.Resource, 0, len(page.Addresses))
	for _, a := range page.Addresses {
		tags := ec2TagsToMap(a.Tags)
		state := "in-use"
		if aws.ToString(a.AssociationId) == "" {
			state = "unassociated"
		}
		out = append(out, audit.Resource{
			Kind:    "ec2/eip",
			ID:      aws.ToString(a.AllocationId),
			Region:  c.Region(),
			Account: c.Account(),
			Name:    tags["Name"],
			Tags:    tags,
			State:   state,
		})
	}
	emit.Progress(len(out))
	return out, nil
}

func init() { audit.Register(EC2EIP{}) }
