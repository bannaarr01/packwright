package scanners

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/bannaarr01/packwright/internal/audit"
)

// EC2Instance enumerates every EC2 instance visible to the caller in
// the audit Client's region.
type EC2Instance struct{}

// Kind reports the stable kind identifier surfaced to the UI and to
// callers filtering the scanner catalogue.
func (EC2Instance) Kind() string { return "ec2/instance" }

// Permissions reports the IAM actions Scan touches. The audit registry
// validates the list at Register time so the read-only invariant is
// enforced before any scanner is reachable.
func (EC2Instance) Permissions() []string { return []string{"ec2:DescribeInstances"} }

// Scan walks DescribeInstances paginators and returns one
// audit.Resource per EC2 instance, fully paginated.
func (EC2Instance) Scan(ctx context.Context, c *audit.Client, emit audit.ScannerEmitter) ([]audit.Resource, error) {
	api := c.EC2()
	if api == nil {
		return nil, fmt.Errorf("ec2/instance: ec2 client is not configured")
	}
	tb := c.Throttle("ec2")

	var out []audit.Resource
	pager := ec2.NewDescribeInstancesPaginator(api, &ec2.DescribeInstancesInput{})
	for pager.HasMorePages() {
		if err := tb.Wait(ctx); err != nil {
			return out, err
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			return out, fmt.Errorf("ec2/instance: describing instances: %w", err)
		}
		for _, r := range page.Reservations {
			for _, i := range r.Instances {
				out = append(out, toInstanceResource(i, c))
			}
		}
		emit.Progress(len(out))
	}
	return out, nil
}

// toInstanceResource maps an ec2types.Instance into the audit Resource
// shape every scanner produces.
func toInstanceResource(i ec2types.Instance, c *audit.Client) audit.Resource {
	tags := ec2TagsToMap(i.Tags)
	res := audit.Resource{
		Kind:    "ec2/instance",
		ID:      aws.ToString(i.InstanceId),
		Region:  c.Region(),
		Account: c.Account(),
		Name:    tags["Name"],
		Tags:    tags,
		State:   string(i.State.Name),
	}
	if i.LaunchTime != nil {
		res.CreatedAt = *i.LaunchTime
	}
	return res
}

// ec2TagsToMap collapses an EC2 tag slice into a {key: value} map. Empty
// or nil keys are skipped to avoid producing maps with "" entries.
func ec2TagsToMap(tags []ec2types.Tag) map[string]string {
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

func init() { audit.Register(EC2Instance{}) }
