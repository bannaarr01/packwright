package awsx

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// VPC is the trimmed-down view of an EC2 VPC the picker UI needs. Fields are
// stable contract; the SDK's ec2types.Vpc is intentionally not re-exported.
type VPC struct {
	ID        string `json:"id"`
	CIDR      string `json:"cidr"`
	Name      string `json:"name,omitempty"`
	IsDefault bool   `json:"is_default"`
}

// Subnet is the trimmed-down view of an EC2 subnet the picker UI needs.
type Subnet struct {
	ID               string `json:"id"`
	VpcID            string `json:"vpc_id"`
	CIDR             string `json:"cidr"`
	AvailabilityZone string `json:"availability_zone"`
	Name             string `json:"name,omitempty"`
}

// SG is the trimmed-down view of an EC2 security group the picker UI needs.
type SG struct {
	ID          string `json:"id"`
	VpcID       string `json:"vpc_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ListVPCs returns every VPC in the client's region, fully paginated.
// Results are cached per (profile, region) for the cache TTL.
func (c *Client) ListVPCs(ctx context.Context) ([]VPC, error) {
	return GetOrFetch(ctx, c.cache, Key{
		Profile: c.profile, Region: c.region, Fn: "ListVPCs",
	}, func(ctx context.Context) ([]VPC, error) {
		out := []VPC{}
		var token *string
		for {
			r, err := c.ec2API.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{NextToken: token})
			if err != nil {
				return nil, fmt.Errorf("awsx: describing vpcs: %w", err)
			}
			for _, v := range r.Vpcs {
				out = append(out, toVPC(v))
			}
			if aws.ToString(r.NextToken) == "" {
				return out, nil
			}
			token = r.NextToken
		}
	})
}

// ListSubnets returns every subnet in the given VPC, fully paginated.
// Results are cached per (profile, region, vpcID).
func (c *Client) ListSubnets(ctx context.Context, vpcID string) ([]Subnet, error) {
	return GetOrFetch(ctx, c.cache, Key{
		Profile: c.profile, Region: c.region, Fn: "ListSubnets",
		Args: []string{vpcID},
	}, func(ctx context.Context) ([]Subnet, error) {
		filters := []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{vpcID}}}
		out := []Subnet{}
		var token *string
		for {
			r, err := c.ec2API.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
				Filters:   filters,
				NextToken: token,
			})
			if err != nil {
				return nil, fmt.Errorf("awsx: describing subnets for %s: %w", vpcID, err)
			}
			for _, s := range r.Subnets {
				out = append(out, toSubnet(s))
			}
			if aws.ToString(r.NextToken) == "" {
				return out, nil
			}
			token = r.NextToken
		}
	})
}

// ListSecurityGroups returns every security group in the given VPC, fully
// paginated. Results are cached per (profile, region, vpcID).
func (c *Client) ListSecurityGroups(ctx context.Context, vpcID string) ([]SG, error) {
	return GetOrFetch(ctx, c.cache, Key{
		Profile: c.profile, Region: c.region, Fn: "ListSecurityGroups",
		Args: []string{vpcID},
	}, func(ctx context.Context) ([]SG, error) {
		filters := []ec2types.Filter{{Name: aws.String("vpc-id"), Values: []string{vpcID}}}
		out := []SG{}
		var token *string
		for {
			r, err := c.ec2API.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
				Filters:   filters,
				NextToken: token,
			})
			if err != nil {
				return nil, fmt.Errorf("awsx: describing security groups for %s: %w", vpcID, err)
			}
			for _, g := range r.SecurityGroups {
				out = append(out, toSG(g))
			}
			if aws.ToString(r.NextToken) == "" {
				return out, nil
			}
			token = r.NextToken
		}
	})
}

func toVPC(v ec2types.Vpc) VPC {
	return VPC{
		ID:        aws.ToString(v.VpcId),
		CIDR:      aws.ToString(v.CidrBlock),
		Name:      tagValue(v.Tags, "Name"),
		IsDefault: aws.ToBool(v.IsDefault),
	}
}

func toSubnet(s ec2types.Subnet) Subnet {
	return Subnet{
		ID:               aws.ToString(s.SubnetId),
		VpcID:            aws.ToString(s.VpcId),
		CIDR:             aws.ToString(s.CidrBlock),
		AvailabilityZone: aws.ToString(s.AvailabilityZone),
		Name:             tagValue(s.Tags, "Name"),
	}
}

func toSG(g ec2types.SecurityGroup) SG {
	return SG{
		ID:          aws.ToString(g.GroupId),
		VpcID:       aws.ToString(g.VpcId),
		Name:        aws.ToString(g.GroupName),
		Description: aws.ToString(g.Description),
	}
}

// tagValue returns the value of the EC2 tag with the given key, or "" if no
// such tag is present. Used to surface the "Name" tag as a display label.
func tagValue(tags []ec2types.Tag, key string) string {
	for _, t := range tags {
		if aws.ToString(t.Key) == key {
			return aws.ToString(t.Value)
		}
	}
	return ""
}
