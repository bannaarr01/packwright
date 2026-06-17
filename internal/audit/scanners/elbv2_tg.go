package scanners

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"

	"github.com/bannaarr01/packwright/internal/audit"
)

// ELBv2TargetGroup enumerates every ELBv2 target group in the audit
// Client's region.
type ELBv2TargetGroup struct{}

// Kind reports the stable kind identifier.
func (ELBv2TargetGroup) Kind() string { return "elbv2/target-group" }

// Permissions reports the IAM actions Scan touches.
func (ELBv2TargetGroup) Permissions() []string {
	return []string{"elasticloadbalancing:DescribeTargetGroups"}
}

// Scan walks DescribeTargetGroups paginators and returns one Resource
// per target group, fully paginated.
func (ELBv2TargetGroup) Scan(ctx context.Context, c *audit.Client, emit audit.ScannerEmitter) ([]audit.Resource, error) {
	api := c.ELBv2()
	if api == nil {
		return nil, fmt.Errorf("elbv2/target-group: elbv2 client is not configured")
	}
	tb := c.Throttle("elbv2")

	var out []audit.Resource
	pager := elasticloadbalancingv2.NewDescribeTargetGroupsPaginator(api, &elasticloadbalancingv2.DescribeTargetGroupsInput{})
	for pager.HasMorePages() {
		if err := tb.Wait(ctx); err != nil {
			return out, err
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			return out, fmt.Errorf("elbv2/target-group: describing target groups: %w", err)
		}
		for _, tg := range page.TargetGroups {
			out = append(out, audit.Resource{
				Kind:    "elbv2/target-group",
				ID:      aws.ToString(tg.TargetGroupArn),
				Region:  c.Region(),
				Account: c.Account(),
				Name:    aws.ToString(tg.TargetGroupName),
				State:   string(tg.Protocol),
			})
		}
		emit.Progress(len(out))
	}
	return out, nil
}

func init() { audit.Register(ELBv2TargetGroup{}) }
