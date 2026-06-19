package awsx

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// FallbackRegions returns the static list of AWS commercial-partition regions
// used when DescribeRegions cannot be consulted — most commonly because the
// active credentials lack ec2:DescribeRegions, but also on any transport
// failure. It is deliberately the broad public list rather than an account's
// enabled subset: callers that need account-accurate data use ListRegions and
// treat this only as a floor so the region switcher is never empty.
//
// A fresh slice is returned on every call so callers may sort or filter the
// result without mutating shared state.
func FallbackRegions() []string {
	return []string{
		"af-south-1",
		"ap-east-1",
		"ap-northeast-1", "ap-northeast-2", "ap-northeast-3",
		"ap-south-1", "ap-south-2",
		"ap-southeast-1", "ap-southeast-2", "ap-southeast-3", "ap-southeast-4",
		"ca-central-1", "ca-west-1",
		"eu-central-1", "eu-central-2",
		"eu-north-1",
		"eu-south-1", "eu-south-2",
		"eu-west-1", "eu-west-2", "eu-west-3",
		"il-central-1",
		"me-central-1", "me-south-1",
		"sa-east-1",
		"us-east-1", "us-east-2",
		"us-west-1", "us-west-2",
	}
}

// ListRegions returns the AWS regions enabled for the account behind the
// client's credentials, sorted by name. It calls EC2 DescribeRegions with
// AllRegions=false, so opt-in regions the account has not enabled are excluded
// — the result is exactly the set the account can operate in.
//
// DescribeRegions is partition-global: the client's own region only selects
// which endpoint answers, not the contents. Results are cached per
// (profile, region) for the cache TTL; the enabled-region set changes rarely,
// so the switcher opens instantly after the first call. A fetch error is not
// cached, so a transient failure or a later-granted permission is retried on
// the next call.
func (c *Client) ListRegions(ctx context.Context) ([]string, error) {
	return GetOrFetch(ctx, c.cache, Key{
		Profile: c.profile, Region: c.region, Fn: "ListRegions",
	}, func(ctx context.Context) ([]string, error) {
		r, err := c.ec2API.DescribeRegions(ctx, &ec2.DescribeRegionsInput{
			AllRegions: aws.Bool(false),
		})
		if err != nil {
			return nil, fmt.Errorf("awsx: describing regions: %w", err)
		}
		out := make([]string, 0, len(r.Regions))
		for _, rg := range r.Regions {
			if name := aws.ToString(rg.RegionName); name != "" {
				out = append(out, name)
			}
		}
		sort.Strings(out)
		return out, nil
	})
}

// ListRegionsOrFallback returns the account's enabled regions via ListRegions,
// or FallbackRegions when that call fails or yields nothing. The underlying
// error is logged through log (when non-nil) so a denied ec2:DescribeRegions is
// visible in the logs without surfacing to the UI, which always receives a
// usable, non-empty list to render.
func ListRegionsOrFallback(ctx context.Context, c *Client, log *slog.Logger) []string {
	regions, err := c.ListRegions(ctx)
	if err != nil {
		if log != nil {
			log.Warn("awsx: region discovery failed; using fallback list", slog.Any("err", err))
		}
		return FallbackRegions()
	}
	if len(regions) == 0 {
		return FallbackRegions()
	}
	return regions
}
