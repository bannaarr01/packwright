package awsx

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// fakeEC2 is a counting fake for the ec2API surface. Each method walks the
// matching slice of canned outputs in order and increments the call counter
// only on the successful path; any extra call beyond the canned outputs
// returns errNoMorePages so tests fail loudly rather than silently looping.
type fakeEC2 struct {
	vpcs      []*ec2.DescribeVpcsOutput
	vpcCalls  int
	subs      []*ec2.DescribeSubnetsOutput
	subCalls  int
	sgs       []*ec2.DescribeSecurityGroupsOutput
	sgCalls   int
	lastVPCID string // captured from the last filtered call, for assertions
	lastSGVPC string
	failNext  error
}

func (f *fakeEC2) DescribeVpcs(_ context.Context, _ *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return nil, err
	}
	if len(f.vpcs) == 0 {
		return nil, errNoMorePages
	}
	f.vpcCalls++
	out := f.vpcs[0]
	f.vpcs = f.vpcs[1:]
	return out, nil
}

func (f *fakeEC2) DescribeSubnets(_ context.Context, in *ec2.DescribeSubnetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	for _, filt := range in.Filters {
		if aws.ToString(filt.Name) == "vpc-id" && len(filt.Values) > 0 {
			f.lastVPCID = filt.Values[0]
		}
	}
	if len(f.subs) == 0 {
		return nil, errNoMorePages
	}
	f.subCalls++
	out := f.subs[0]
	f.subs = f.subs[1:]
	return out, nil
}

func (f *fakeEC2) DescribeSecurityGroups(_ context.Context, in *ec2.DescribeSecurityGroupsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	for _, filt := range in.Filters {
		if aws.ToString(filt.Name) == "vpc-id" && len(filt.Values) > 0 {
			f.lastSGVPC = filt.Values[0]
		}
	}
	if len(f.sgs) == 0 {
		return nil, errNoMorePages
	}
	f.sgCalls++
	out := f.sgs[0]
	f.sgs = f.sgs[1:]
	return out, nil
}

func newEC2Client(t *testing.T, fake *fakeEC2) *Client {
	t.Helper()
	c := newTestClient(t)
	c.ec2API = fake
	return c
}

func TestListVPCsConcatenatesPages(t *testing.T) {
	fake := &fakeEC2{
		vpcs: []*ec2.DescribeVpcsOutput{
			{
				Vpcs: []ec2types.Vpc{
					{VpcId: aws.String("vpc-1"), CidrBlock: aws.String("10.0.0.0/16"), IsDefault: aws.Bool(false),
						Tags: []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("prod")}}},
				},
				NextToken: aws.String("more"),
			},
			{
				Vpcs: []ec2types.Vpc{
					{VpcId: aws.String("vpc-2"), CidrBlock: aws.String("10.1.0.0/16"), IsDefault: aws.Bool(true)},
				},
			},
		},
	}
	c := newEC2Client(t, fake)

	got, err := c.ListVPCs(context.Background())
	if err != nil {
		t.Fatalf("ListVPCs: %v", err)
	}
	if fake.vpcCalls != 2 {
		t.Fatalf("DescribeVpcs calls = %d, want 2", fake.vpcCalls)
	}
	if len(got) != 2 || got[0].ID != "vpc-1" || got[0].Name != "prod" || got[1].ID != "vpc-2" || !got[1].IsDefault {
		t.Fatalf("ListVPCs result = %+v", got)
	}
}

func TestListVPCsCachesAcrossCalls(t *testing.T) {
	fake := &fakeEC2{
		vpcs: []*ec2.DescribeVpcsOutput{
			{Vpcs: []ec2types.Vpc{{VpcId: aws.String("vpc-1"), CidrBlock: aws.String("10.0.0.0/16")}}},
		},
	}
	c := newEC2Client(t, fake)
	ctx := context.Background()

	if _, err := c.ListVPCs(ctx); err != nil {
		t.Fatalf("first ListVPCs: %v", err)
	}
	if _, err := c.ListVPCs(ctx); err != nil {
		t.Fatalf("second ListVPCs: %v", err)
	}
	if fake.vpcCalls != 1 {
		t.Fatalf("DescribeVpcs calls = %d, want 1 (second call should hit the cache)", fake.vpcCalls)
	}
}

func TestListVPCsPropagatesSDKError(t *testing.T) {
	fake := &fakeEC2{failNext: errors.New("boom")}
	c := newEC2Client(t, fake)
	if _, err := c.ListVPCs(context.Background()); err == nil {
		t.Fatal("ListVPCs err = nil, want one wrapping boom")
	}
}

func TestListSubnetsFiltersByVPC(t *testing.T) {
	fake := &fakeEC2{
		subs: []*ec2.DescribeSubnetsOutput{
			{Subnets: []ec2types.Subnet{
				{SubnetId: aws.String("subnet-a"), VpcId: aws.String("vpc-1"),
					CidrBlock: aws.String("10.0.1.0/24"), AvailabilityZone: aws.String("eu-west-1a"),
					Tags: []ec2types.Tag{{Key: aws.String("Name"), Value: aws.String("public-a")}}},
			}},
		},
	}
	c := newEC2Client(t, fake)

	got, err := c.ListSubnets(context.Background(), "vpc-1")
	if err != nil {
		t.Fatalf("ListSubnets: %v", err)
	}
	if fake.lastVPCID != "vpc-1" {
		t.Fatalf("filter vpc-id = %q, want vpc-1", fake.lastVPCID)
	}
	if len(got) != 1 || got[0].ID != "subnet-a" || got[0].Name != "public-a" || got[0].AvailabilityZone != "eu-west-1a" {
		t.Fatalf("ListSubnets result = %+v", got)
	}
}

func TestListSubnetsCachesPerVPC(t *testing.T) {
	fake := &fakeEC2{
		subs: []*ec2.DescribeSubnetsOutput{
			{Subnets: []ec2types.Subnet{{SubnetId: aws.String("subnet-a"), VpcId: aws.String("vpc-1")}}},
			{Subnets: []ec2types.Subnet{{SubnetId: aws.String("subnet-b"), VpcId: aws.String("vpc-2")}}},
		},
	}
	c := newEC2Client(t, fake)
	ctx := context.Background()

	// Different vpcIDs ⇒ different keys ⇒ two SDK calls.
	if _, err := c.ListSubnets(ctx, "vpc-1"); err != nil {
		t.Fatalf("vpc-1: %v", err)
	}
	if _, err := c.ListSubnets(ctx, "vpc-2"); err != nil {
		t.Fatalf("vpc-2: %v", err)
	}
	// Repeats hit the cache.
	if _, err := c.ListSubnets(ctx, "vpc-1"); err != nil {
		t.Fatalf("vpc-1 repeat: %v", err)
	}
	if _, err := c.ListSubnets(ctx, "vpc-2"); err != nil {
		t.Fatalf("vpc-2 repeat: %v", err)
	}
	if fake.subCalls != 2 {
		t.Fatalf("DescribeSubnets calls = %d, want 2 (one per distinct vpc-id)", fake.subCalls)
	}
}

func TestListSecurityGroupsFiltersByVPC(t *testing.T) {
	fake := &fakeEC2{
		sgs: []*ec2.DescribeSecurityGroupsOutput{
			{SecurityGroups: []ec2types.SecurityGroup{
				{GroupId: aws.String("sg-1"), VpcId: aws.String("vpc-1"),
					GroupName: aws.String("web"), Description: aws.String("web tier")},
			}},
		},
	}
	c := newEC2Client(t, fake)

	got, err := c.ListSecurityGroups(context.Background(), "vpc-1")
	if err != nil {
		t.Fatalf("ListSecurityGroups: %v", err)
	}
	if fake.lastSGVPC != "vpc-1" {
		t.Fatalf("filter vpc-id = %q, want vpc-1", fake.lastSGVPC)
	}
	if len(got) != 1 || got[0].ID != "sg-1" || got[0].Name != "web" || got[0].Description != "web tier" {
		t.Fatalf("ListSecurityGroups result = %+v", got)
	}
}
