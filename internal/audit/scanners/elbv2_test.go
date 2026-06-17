package scanners

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/bannaarr01/packwright/internal/audit"
)

// fakeELBv2 walks canned DescribeLoadBalancers / DescribeTargetGroups
// outputs in order.
type fakeELBv2 struct {
	lbs []*elasticloadbalancingv2.DescribeLoadBalancersOutput
	tgs []*elasticloadbalancingv2.DescribeTargetGroupsOutput

	lbCalls, tgCalls int
}

func (f *fakeELBv2) DescribeLoadBalancers(_ context.Context, _ *elasticloadbalancingv2.DescribeLoadBalancersInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error) {
	if len(f.lbs) == 0 {
		return &elasticloadbalancingv2.DescribeLoadBalancersOutput{}, nil
	}
	f.lbCalls++
	out := f.lbs[0]
	f.lbs = f.lbs[1:]
	return out, nil
}

func (f *fakeELBv2) DescribeTargetGroups(_ context.Context, _ *elasticloadbalancingv2.DescribeTargetGroupsInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupsOutput, error) {
	if len(f.tgs) == 0 {
		return &elasticloadbalancingv2.DescribeTargetGroupsOutput{}, nil
	}
	f.tgCalls++
	out := f.tgs[0]
	f.tgs = f.tgs[1:]
	return out, nil
}

func TestELBv2LoadBalancerScannerPaginates(t *testing.T) {
	when := time.Unix(1_700_000_000, 0).UTC()
	fake := &fakeELBv2{
		lbs: []*elasticloadbalancingv2.DescribeLoadBalancersOutput{
			{LoadBalancers: []elbv2types.LoadBalancer{
				{
					LoadBalancerArn:  aws.String("arn:lb-1"),
					LoadBalancerName: aws.String("front"),
					CreatedTime:      &when,
					State:            &elbv2types.LoadBalancerState{Code: elbv2types.LoadBalancerStateEnumActive},
				},
			}, NextMarker: aws.String("more")},
			{LoadBalancers: []elbv2types.LoadBalancer{
				{LoadBalancerArn: aws.String("arn:lb-2"), LoadBalancerName: aws.String("back")},
			}},
		},
	}
	c := audit.NewForTest(audit.WithELBv2(fake))
	got, err := ELBv2LoadBalancer{}.Scan(context.Background(), c, audit.NoopEmitter{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 2 || got[0].Name != "front" || got[1].Name != "back" {
		t.Errorf("got %+v, want front + back", got)
	}
	if got[0].State != "active" {
		t.Errorf("state = %q, want active", got[0].State)
	}
	if fake.lbCalls != 2 {
		t.Errorf("DescribeLoadBalancers calls = %d, want 2", fake.lbCalls)
	}
}

func TestELBv2TargetGroupScannerSurfacesProtocol(t *testing.T) {
	fake := &fakeELBv2{
		tgs: []*elasticloadbalancingv2.DescribeTargetGroupsOutput{
			{TargetGroups: []elbv2types.TargetGroup{
				{TargetGroupArn: aws.String("arn:tg-1"), TargetGroupName: aws.String("web"), Protocol: elbv2types.ProtocolEnumHttps},
			}},
		},
	}
	c := audit.NewForTest(audit.WithELBv2(fake))
	got, err := ELBv2TargetGroup{}.Scan(context.Background(), c, audit.NoopEmitter{})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(got) != 1 || got[0].State != "HTTPS" {
		t.Errorf("got %+v, want HTTPS protocol", got)
	}
}
