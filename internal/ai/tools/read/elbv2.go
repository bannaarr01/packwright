package read

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// elbv2ClientFactory builds an ELBv2 client bound to the toolset's awsx.Client.
var elbv2ClientFactory = func(ctx context.Context, toolName string) (elbv2API, error) {
	c, err := tools.RequireAWSClient(ctx, toolName)
	if err != nil {
		return nil, err
	}
	cfg, err := tools.LoadAWSConfig(ctx, toolName, c)
	if err != nil {
		return nil, err
	}
	return elasticloadbalancingv2.NewFromConfig(cfg), nil
}

// elbv2API is the subset of ELBv2 operations the read tools call.
type elbv2API interface {
	DescribeLoadBalancers(ctx context.Context, in *elasticloadbalancingv2.DescribeLoadBalancersInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeLoadBalancersOutput, error)
	DescribeTargetHealth(ctx context.Context, in *elasticloadbalancingv2.DescribeTargetHealthInput, opts ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error)
}

// describeLoadBalancers summarises ALBs / NLBs / GWLBs.
type describeLoadBalancers struct{}

// Name reports the catalogue name.
func (describeLoadBalancers) Name() string { return "elbv2/describe-load-balancers" }

// Permission returns the const PermissionRead.
func (describeLoadBalancers) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t describeLoadBalancers) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Describe ELBv2 load balancers (ALB/NLB/GWLB) by name or ARN. Returns DNS name, state, type, and VPC.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"names": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Load balancer names (one or more).",
				},
				"arns": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Load balancer ARNs (one or more).",
				},
			},
		},
	}
}

// Execute issues DescribeLoadBalancers.
func (t describeLoadBalancers) Execute(ctx context.Context, args map[string]any) (any, error) {
	names, err := tools.ArgStringSlice(t.Name(), args, "names", false)
	if err != nil {
		return nil, err
	}
	arns, err := tools.ArgStringSlice(t.Name(), args, "arns", false)
	if err != nil {
		return nil, err
	}
	api, err := elbv2ClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	in := &elasticloadbalancingv2.DescribeLoadBalancersInput{}
	if len(names) > 0 {
		in.Names = names
	}
	if len(arns) > 0 {
		in.LoadBalancerArns = arns
	}
	out, err := api.DescribeLoadBalancers(ctx, in)
	if err != nil {
		return nil, err
	}
	res := make([]map[string]any, 0, len(out.LoadBalancers))
	for _, lb := range out.LoadBalancers {
		entry := map[string]any{
			"name":     aws.ToString(lb.LoadBalancerName),
			"arn":      aws.ToString(lb.LoadBalancerArn),
			"dns_name": aws.ToString(lb.DNSName),
			"scheme":   string(lb.Scheme),
			"type":     string(lb.Type),
			"vpc_id":   aws.ToString(lb.VpcId),
		}
		if lb.State != nil {
			entry["state"] = string(lb.State.Code)
			if lb.State.Reason != nil {
				entry["state_reason"] = aws.ToString(lb.State.Reason)
			}
		}
		res = append(res, entry)
	}
	return map[string]any{"load_balancers": res}, nil
}

// describeTargetHealth returns the per-target health state of a target group.
type describeTargetHealth struct{}

// Name reports the catalogue name.
func (describeTargetHealth) Name() string { return "elbv2/describe-target-health" }

// Permission returns the const PermissionRead.
func (describeTargetHealth) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t describeTargetHealth) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Describe the health of every registered target in an ELBv2 target group.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target_group_arn": map[string]any{"type": "string", "description": "Target group ARN."},
			},
			"required": []string{"target_group_arn"},
		},
	}
}

// Execute issues DescribeTargetHealth.
func (t describeTargetHealth) Execute(ctx context.Context, args map[string]any) (any, error) {
	arn, err := tools.ArgString(t.Name(), args, "target_group_arn", true)
	if err != nil {
		return nil, err
	}
	api, err := elbv2ClientFactory(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	out, err := api.DescribeTargetHealth(ctx, &elasticloadbalancingv2.DescribeTargetHealthInput{
		TargetGroupArn: aws.String(arn),
	})
	if err != nil {
		return nil, err
	}
	res := make([]map[string]any, 0, len(out.TargetHealthDescriptions))
	for _, h := range out.TargetHealthDescriptions {
		entry := map[string]any{}
		if h.Target != nil {
			entry["target_id"] = aws.ToString(h.Target.Id)
			if h.Target.Port != nil {
				entry["target_port"] = int(*h.Target.Port)
			}
			if h.Target.AvailabilityZone != nil {
				entry["az"] = aws.ToString(h.Target.AvailabilityZone)
			}
		}
		if h.TargetHealth != nil {
			entry["state"] = string(h.TargetHealth.State)
			entry["reason"] = string(h.TargetHealth.Reason)
			if h.TargetHealth.Description != nil {
				entry["description"] = aws.ToString(h.TargetHealth.Description)
			}
		}
		res = append(res, entry)
	}
	return map[string]any{"targets": res}, nil
}

func init() {
	tools.MustRegister(tools.Default, describeLoadBalancers{})
	tools.MustRegister(tools.Default, describeTargetHealth{})
}
