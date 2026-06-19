package gui

import (
	"context"
	"errors"
	"testing"

	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/config"
)

// withRegionDeps replaces regionDeps for the duration of a test and restores it
// via t.Cleanup, mirroring withSwitcherDeps.
func withRegionDeps(t *testing.T, mutate func()) {
	t.Helper()
	orig := regionDeps
	t.Cleanup(func() { regionDeps = orig })
	mutate()
}

func TestListRegionsReturnsSeamResult(t *testing.T) {
	withRegionDeps(t, func() {
		regionDeps.cacheHome = func() (string, error) { return t.TempDir(), nil }
		regionDeps.newClient = func(_ context.Context, profile, region, _ string) (*awsx.Client, error) {
			return awsx.NewForTest(profile, region), nil
		}
		regionDeps.list = func(_ context.Context, _ *awsx.Client) []string {
			return []string{"us-east-1", "eu-west-1"}
		}
	})
	got, err := newTestApp().ListRegions()
	if err != nil {
		t.Fatalf("ListRegions: %v", err)
	}
	if len(got) != 2 || got[0] != "us-east-1" || got[1] != "eu-west-1" {
		t.Fatalf("ListRegions = %v, want [us-east-1 eu-west-1]", got)
	}
}

func TestListRegionsSurfacesClientError(t *testing.T) {
	withRegionDeps(t, func() {
		regionDeps.cacheHome = func() (string, error) { return t.TempDir(), nil }
		regionDeps.newClient = func(_ context.Context, _, _, _ string) (*awsx.Client, error) {
			return nil, errors.New("no cache home")
		}
	})
	if _, err := newTestApp().ListRegions(); err == nil {
		t.Fatal("ListRegions err = nil, want the client-build failure")
	}
}

func TestSwitchRegionPersistsRegionKeepingProfile(t *testing.T) {
	t.Setenv("PACKWRIGHT_HOME", t.TempDir())
	// Seed a persisted profile so we can confirm SwitchRegion leaves it intact.
	seed := &config.Config{Profile: "ops", Region: "us-east-1"}
	if err := seed.Save(); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	withSwitcherDeps(t, func() {
		profileSwitcherDeps.cacheHome = func() (string, error) { return t.TempDir(), nil }
		profileSwitcherDeps.newClient = func(_ context.Context, profile, region, _ string) (*awsx.Client, error) {
			if profile != "ops" {
				t.Errorf("SwitchRegion built client for profile %q, want ops (profile held fixed)", profile)
			}
			return awsx.NewForTest(profile, region), nil
		}
		profileSwitcherDeps.verify = func(_ context.Context, _ *awsx.Client) (*awsx.Identity, error) {
			return &awsx.Identity{Profile: "ops", Region: "eu-west-1", Account: "1"}, nil
		}
	})

	res := newTestApp().SwitchRegion("eu-west-1")
	if !res.Ok {
		t.Fatalf("SwitchRegion result = %+v, want Ok", res)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if cfg.Region != "eu-west-1" {
		t.Errorf("persisted region = %q, want eu-west-1", cfg.Region)
	}
	if cfg.Profile != "ops" {
		t.Errorf("persisted profile = %q, want ops (unchanged)", cfg.Profile)
	}
}

func TestSwitchRegionFailureLeavesConfig(t *testing.T) {
	t.Setenv("PACKWRIGHT_HOME", t.TempDir())
	seed := &config.Config{Profile: "ops", Region: "us-east-1"}
	if err := seed.Save(); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	withSwitcherDeps(t, func() {
		profileSwitcherDeps.cacheHome = func() (string, error) { return t.TempDir(), nil }
		profileSwitcherDeps.newClient = func(_ context.Context, profile, region, _ string) (*awsx.Client, error) {
			return awsx.NewForTest(profile, region), nil
		}
		profileSwitcherDeps.verify = func(_ context.Context, _ *awsx.Client) (*awsx.Identity, error) {
			return nil, &awsx.VerifyError{Cause: errors.New("region not enabled")}
		}
	})

	if res := newTestApp().SwitchRegion("ap-east-1"); res.Ok {
		t.Fatalf("SwitchRegion.Ok = true, want false on verify failure")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if cfg.Region != "us-east-1" {
		t.Errorf("region changed on failed switch: %q, want us-east-1", cfg.Region)
	}
}
