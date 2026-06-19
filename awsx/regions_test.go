package awsx

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func regionsOutput(names ...string) *ec2.DescribeRegionsOutput {
	out := &ec2.DescribeRegionsOutput{}
	for _, n := range names {
		out.Regions = append(out.Regions, ec2types.Region{RegionName: aws.String(n)})
	}
	return out
}

func TestListRegionsReturnsSortedNames(t *testing.T) {
	fake := &fakeEC2{regions: regionsOutput("us-west-2", "eu-west-1", "us-east-1")}
	c := newEC2Client(t, fake)

	got, err := c.ListRegions(context.Background())
	if err != nil {
		t.Fatalf("ListRegions: %v", err)
	}
	want := []string{"eu-west-1", "us-east-1", "us-west-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListRegions = %v, want %v", got, want)
	}
}

func TestListRegionsSkipsBlankNames(t *testing.T) {
	out := regionsOutput("us-east-1")
	out.Regions = append(out.Regions, ec2types.Region{RegionName: nil})
	fake := &fakeEC2{regions: out}
	c := newEC2Client(t, fake)

	got, err := c.ListRegions(context.Background())
	if err != nil {
		t.Fatalf("ListRegions: %v", err)
	}
	if want := []string{"us-east-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ListRegions = %v, want %v", got, want)
	}
}

func TestListRegionsCachesAcrossCalls(t *testing.T) {
	fake := &fakeEC2{regions: regionsOutput("us-east-1", "us-west-2")}
	c := newEC2Client(t, fake)

	if _, err := c.ListRegions(context.Background()); err != nil {
		t.Fatalf("ListRegions first: %v", err)
	}
	if _, err := c.ListRegions(context.Background()); err != nil {
		t.Fatalf("ListRegions second: %v", err)
	}
	if fake.regionCalls != 1 {
		t.Fatalf("DescribeRegions calls = %d, want 1 (second call should hit the cache)", fake.regionCalls)
	}
}

func TestListRegionsPropagatesError(t *testing.T) {
	fake := &fakeEC2{regionsErr: errors.New("AccessDenied")}
	c := newEC2Client(t, fake)

	if _, err := c.ListRegions(context.Background()); err == nil {
		t.Fatal("ListRegions = nil error, want the DescribeRegions failure")
	}
}

func TestListRegionsOrFallbackUsesFallbackOnError(t *testing.T) {
	fake := &fakeEC2{regionsErr: errors.New("AccessDenied")}
	c := newEC2Client(t, fake)

	got := ListRegionsOrFallback(context.Background(), c, nil)
	if !reflect.DeepEqual(got, FallbackRegions()) {
		t.Fatalf("ListRegionsOrFallback on error = %v, want FallbackRegions()", got)
	}
}

func TestListRegionsOrFallbackUsesFallbackOnEmpty(t *testing.T) {
	fake := &fakeEC2{regions: regionsOutput()} // no regions returned
	c := newEC2Client(t, fake)

	got := ListRegionsOrFallback(context.Background(), c, nil)
	if !reflect.DeepEqual(got, FallbackRegions()) {
		t.Fatalf("ListRegionsOrFallback on empty = %v, want FallbackRegions()", got)
	}
}

func TestListRegionsOrFallbackReturnsLiveRegions(t *testing.T) {
	fake := &fakeEC2{regions: regionsOutput("us-east-1", "eu-west-1")}
	c := newEC2Client(t, fake)

	got := ListRegionsOrFallback(context.Background(), c, nil)
	if want := []string{"eu-west-1", "us-east-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ListRegionsOrFallback = %v, want %v", got, want)
	}
}

func TestFallbackRegionsNonEmptyAndDistinctSlices(t *testing.T) {
	a := FallbackRegions()
	if len(a) == 0 {
		t.Fatal("FallbackRegions() is empty")
	}
	// Each call must return an independent slice so callers can mutate freely.
	b := FallbackRegions()
	b[0] = "mutated"
	if a[0] == "mutated" {
		t.Fatal("FallbackRegions() returns a shared backing array")
	}
}
