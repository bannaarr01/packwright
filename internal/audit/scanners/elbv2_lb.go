package scanners

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"

	"github.com/bannaarr01/packwright/internal/audit"
)

// ELBv2LoadBalancer enumerates every ELBv2-managed load balancer
// (Application, Network, Gateway) in the audit Client's region.
type ELBv2LoadBalancer struct{}

// Kind reports the stable kind identifier.
func (ELBv2LoadBalancer) Kind() string { return "elbv2/load-balancer" }

// Permissions reports the IAM actions Scan touches.
func (ELBv2LoadBalancer) Permissions() []string {
	return []string{"elasticloadbalancing:DescribeLoadBalancers"}
}

// Scan walks DescribeLoadBalancers paginators and returns one Resource
// per load balancer, fully paginated.
func (ELBv2LoadBalancer) Scan(ctx context.Context, c *audit.Client, emit audit.ScannerEmitter) ([]audit.Resource, error) {
	api := c.ELBv2()
	if api == nil {
		return nil, fmt.Errorf("elbv2/load-balancer: elbv2 client is not configured")
	}
	tb := c.Throttle("elbv2")

	var out []audit.Resource
	pager := elasticloadbalancingv2.NewDescribeLoadBalancersPaginator(api, &elasticloadbalancingv2.DescribeLoadBalancersInput{})
	for pager.HasMorePages() {
		if err := tb.Wait(ctx); err != nil {
			return out, err
		}
		page, err := pager.NextPage(ctx)
		if err != nil {
			return out, fmt.Errorf("elbv2/load-balancer: describing load balancers: %w", err)
		}
		for _, lb := range page.LoadBalancers {
			state := ""
			if lb.State != nil {
				state = string(lb.State.Code)
			}
			res := audit.Resource{
				Kind:    "elbv2/load-balancer",
				ID:      aws.ToString(lb.LoadBalancerArn),
				Region:  c.Region(),
				Account: c.Account(),
				Name:    aws.ToString(lb.LoadBalancerName),
				State:   state,
			}
			if lb.CreatedTime != nil {
				res.CreatedAt = *lb.CreatedTime
			}
			out = append(out, res)
		}
		emit.Progress(len(out))
	}
	return out, nil
}

func init() { audit.Register(ELBv2LoadBalancer{}) }
