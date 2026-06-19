package gui

import (
	"context"
	"fmt"

	"github.com/bannaarr01/packwright/awsx"
)

// PR-07's profile switcher gains a region counterpart here. The two share the
// SwitchResult shape, the ProfileSwitchedEvent (a region change is still an
// AWS-context change the header must reflect), and the persistContext helper —
// only the discovery source and the "profile fixed, region varies" intent
// differ, so the region surface lives in its own file rather than bloating
// bindings_profile.go.

// regionDeps lets tests override the AWS-touching parts of ListRegions without
// widening the App struct, mirroring profileSwitcherDeps. SwitchRegion reuses
// runSwitch (and therefore profileSwitcherDeps) for verification, so only the
// discovery path needs its own seam.
var regionDeps = struct {
	newClient func(ctx context.Context, profile, region, cacheHome string) (*awsx.Client, error)
	list      func(ctx context.Context, c *awsx.Client) []string
	cacheHome func() (string, error)
}{
	newClient: func(ctx context.Context, profile, region, cacheHome string) (*awsx.Client, error) {
		return awsx.New(ctx, profile, region, cacheHome, nil)
	},
	list: func(ctx context.Context, c *awsx.Client) []string {
		return awsx.ListRegionsOrFallback(ctx, c, nil)
	},
	cacheHome: defaultCacheHome,
}

// ListRegions returns the AWS regions enabled for the active profile/account
// via EC2 DescribeRegions, falling back to awsx.FallbackRegions when that call
// fails or the credentials lack ec2:DescribeRegions — so the switcher always
// receives a non-empty list. Discovery runs against the currently-resolved
// profile; an unset region ("-") is normalised to "" so the SDK picks the
// endpoint, which does not affect the partition-global result.
func (a *App) ListRegions() ([]string, error) {
	ctx := a.parentCtx
	if ctx == nil {
		ctx = context.Background()
	}
	cacheHome, err := regionDeps.cacheHome()
	if err != nil {
		return nil, fmt.Errorf("gui: resolving cache home: %w", err)
	}
	region := currentRegionName()
	if region == "-" {
		region = ""
	}
	client, err := regionDeps.newClient(ctx, currentProfileName(), region, cacheHome)
	if err != nil {
		return nil, fmt.Errorf("gui: building region-discovery client: %w", err)
	}
	return regionDeps.list(ctx, client), nil
}

// SwitchRegion re-initialises the awsx Client for the current profile against
// the chosen region and verifies it via STS. On success it persists the region
// to config.yaml (the profile is unchanged), emits a ProfileSwitchedEvent so
// the footer refreshes, and returns Identity. On failure it returns the
// structured error + Suggested[] and leaves the active context unchanged.
func (a *App) SwitchRegion(region string) SwitchResult {
	res := a.runSwitch(currentProfileName(), region)
	if res.Ok {
		a.persistContext("", region)
	}
	return res
}
