package awsx

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

// ALB is the trimmed-down view of an Application Load Balancer the picker UI
// needs. ELBv2 lumps ALBs, NLBs, and Gateway LBs together; ListALBs filters
// down to application load balancers only.
type ALB struct {
	ARN     string `json:"arn"`
	Name    string `json:"name"`
	DNSName string `json:"dns_name"`
	Scheme  string `json:"scheme"`
	VpcID   string `json:"vpc_id"`
}

// ListALBs returns every Application Load Balancer in the client's region.
// The ELBv2 API has no server-side type filter, so NLB and Gateway load
// balancers are dropped client-side. Results are cached per (profile, region).
func (c *Client) ListALBs(ctx context.Context) ([]ALB, error) {
	return GetOrFetch(ctx, c.cache, Key{
		Profile: c.profile, Region: c.region, Fn: "ListALBs",
	}, func(ctx context.Context) ([]ALB, error) {
		out := []ALB{}
		var marker *string
		for {
			r, err := c.elbv2API.DescribeLoadBalancers(ctx, &elasticloadbalancingv2.DescribeLoadBalancersInput{
				Marker: marker,
			})
			if err != nil {
				return nil, fmt.Errorf("awsx: describing load balancers: %w", err)
			}
			for _, lb := range r.LoadBalancers {
				if lb.Type != elbv2types.LoadBalancerTypeEnumApplication {
					continue
				}
				out = append(out, toALB(lb))
			}
			if aws.ToString(r.NextMarker) == "" {
				return out, nil
			}
			marker = r.NextMarker
		}
	})
}

func toALB(lb elbv2types.LoadBalancer) ALB {
	return ALB{
		ARN:     aws.ToString(lb.LoadBalancerArn),
		Name:    aws.ToString(lb.LoadBalancerName),
		DNSName: aws.ToString(lb.DNSName),
		Scheme:  string(lb.Scheme),
		VpcID:   aws.ToString(lb.VpcId),
	}
}
