package awsx

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
)

type fakeELBv2 struct {
	pages []*elasticloadbalancingv2.DescribeLoadBalancersOutput
	calls int
}

func (f *fakeELBv2) DescribeLoadBalancers(_ context.Context, _ *elasticloadbalancingv2.DescribeLoadBalancersInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
	if len(f.pages) == 0 {
		return nil, errNoMorePages
	}
	f.calls++
	out := f.pages[0]
	f.pages = f.pages[1:]
	return out, nil
}

func newELBv2Client(t *testing.T, fake *fakeELBv2) *Client {
	t.Helper()
	c := newTestClient(t)
	c.elbv2API = fake
	return c
}

func TestListALBsFiltersOutNonApplicationTypes(t *testing.T) {
	fake := &fakeELBv2{
		pages: []*elasticloadbalancingv2.DescribeLoadBalancersOutput{
			{
				LoadBalancers: []elbv2types.LoadBalancer{
					{
						LoadBalancerArn:  aws.String("arn:alb-1"),
						LoadBalancerName: aws.String("alb-1"),
						DNSName:          aws.String("alb-1.example"),
						Scheme:           elbv2types.LoadBalancerSchemeEnumInternetFacing,
						VpcId:            aws.String("vpc-1"),
						Type:             elbv2types.LoadBalancerTypeEnumApplication,
					},
					{
						LoadBalancerArn:  aws.String("arn:nlb-1"),
						LoadBalancerName: aws.String("nlb-1"),
						Type:             elbv2types.LoadBalancerTypeEnumNetwork,
					},
					{
						LoadBalancerArn:  aws.String("arn:gwlb-1"),
						LoadBalancerName: aws.String("gwlb-1"),
						Type:             elbv2types.LoadBalancerTypeEnumGateway,
					},
				},
				NextMarker: aws.String("next"),
			},
			{
				LoadBalancers: []elbv2types.LoadBalancer{
					{
						LoadBalancerArn:  aws.String("arn:alb-2"),
						LoadBalancerName: aws.String("alb-2"),
						Type:             elbv2types.LoadBalancerTypeEnumApplication,
					},
				},
			},
		},
	}
	c := newELBv2Client(t, fake)

	got, err := c.ListALBs(context.Background())
	if err != nil {
		t.Fatalf("ListALBs: %v", err)
	}
	if fake.calls != 2 {
		t.Fatalf("DescribeLoadBalancers calls = %d, want 2", fake.calls)
	}
	if len(got) != 2 {
		t.Fatalf("ListALBs len = %d, want 2 (NLB and GWLB must be excluded): %+v", len(got), got)
	}
	if got[0].ARN != "arn:alb-1" || got[1].ARN != "arn:alb-2" {
		t.Fatalf("ListALBs result = %+v", got)
	}
	if got[0].Scheme != string(elbv2types.LoadBalancerSchemeEnumInternetFacing) {
		t.Fatalf("ListALBs scheme = %q, want %q", got[0].Scheme, elbv2types.LoadBalancerSchemeEnumInternetFacing)
	}
}

func TestListALBsCaches(t *testing.T) {
	fake := &fakeELBv2{
		pages: []*elasticloadbalancingv2.DescribeLoadBalancersOutput{
			{LoadBalancers: []elbv2types.LoadBalancer{{
				LoadBalancerArn: aws.String("arn:alb-1"),
				Type:            elbv2types.LoadBalancerTypeEnumApplication,
			}}},
		},
	}
	c := newELBv2Client(t, fake)
	ctx := context.Background()

	if _, err := c.ListALBs(ctx); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := c.ListALBs(ctx); err != nil {
		t.Fatalf("second: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("DescribeLoadBalancers calls = %d, want 1 (second must hit cache)", fake.calls)
	}
}
