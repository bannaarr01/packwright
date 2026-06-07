package gui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/awsx"
)

// withSwitcherDeps replaces profileSwitcherDeps for the duration of a test and
// restores it via t.Cleanup so tests cannot leak state between cases.
func withSwitcherDeps(t *testing.T, mutate func()) {
	t.Helper()
	orig := profileSwitcherDeps
	t.Cleanup(func() { profileSwitcherDeps = orig })
	mutate()
}

func TestListProfilesMarksActive(t *testing.T) {
	withSwitcherDeps(t, func() {
		profileSwitcherDeps.listProfiles = func() ([]awsx.Profile, error) {
			return []awsx.Profile{
				{Name: "alpha", Region: "us-east-1"},
				{Name: "beta", Region: "eu-west-1"},
			}, nil
		}
	})
	t.Setenv("AWS_PROFILE", "beta")
	got, err := newTestApp().ListProfiles()
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	for _, p := range got {
		want := p.Name == "beta"
		if p.Active != want {
			t.Errorf("profile %q Active = %v, want %v", p.Name, p.Active, want)
		}
	}
}

func TestListProfilesFallsBackToDefault(t *testing.T) {
	withSwitcherDeps(t, func() {
		profileSwitcherDeps.listProfiles = func() ([]awsx.Profile, error) {
			return []awsx.Profile{{Name: "default"}, {Name: "ops"}}, nil
		}
	})
	t.Setenv("AWS_PROFILE", "")
	got, err := newTestApp().ListProfiles()
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	for _, p := range got {
		want := p.Name == "default"
		if p.Active != want {
			t.Errorf("profile %q Active = %v, want %v with empty AWS_PROFILE", p.Name, p.Active, want)
		}
	}
}

func TestSwitchProfileSuccessReturnsIdentity(t *testing.T) {
	wantID := &awsx.Identity{
		Profile: "alpha", Region: "us-east-1",
		Account: "111122223333",
		Arn:     "arn:aws:iam::111122223333:user/jdoe",
		UserId:  "AIDAEXAMPLE",
	}
	withSwitcherDeps(t, func() {
		profileSwitcherDeps.newClient = func(_ context.Context, profile, region, cacheHome string) (*awsx.Client, error) {
			if profile != "alpha" || region != "us-east-1" {
				t.Errorf("newClient saw profile=%q region=%q, want alpha/us-east-1", profile, region)
			}
			if cacheHome == "" {
				t.Error("newClient saw empty cacheHome")
			}
			return awsx.NewForTest(profile, region), nil
		}
		profileSwitcherDeps.verify = func(_ context.Context, _ *awsx.Client) (*awsx.Identity, error) {
			return wantID, nil
		}
		profileSwitcherDeps.cacheHome = func() (string, error) { return t.TempDir(), nil }
	})

	res := newTestApp().SwitchProfile("alpha", "us-east-1")
	if !res.Ok || res.Identity == nil {
		t.Fatalf("SwitchProfile result = %+v, want Ok with Identity set", res)
	}
	if res.Identity.Account != wantID.Account || res.Identity.Arn != wantID.Arn {
		t.Errorf("Identity = %+v, want %+v", res.Identity, wantID)
	}
	if res.Error != "" || len(res.Suggested) != 0 {
		t.Errorf("Error / Suggested should be empty on success: %+v", res)
	}
}

func TestSwitchProfileSurfacesVerifyError(t *testing.T) {
	ve := &awsx.VerifyError{
		Profile:   "alpha",
		Region:    "us-east-1",
		Cause:     errors.New("expired sso token"),
		Suggested: []string{"aws sso login --profile alpha"},
	}
	withSwitcherDeps(t, func() {
		profileSwitcherDeps.newClient = func(_ context.Context, _, _, _ string) (*awsx.Client, error) {
			return awsx.NewForTest("alpha", "us-east-1"), nil
		}
		profileSwitcherDeps.verify = func(_ context.Context, _ *awsx.Client) (*awsx.Identity, error) { return nil, ve }
		profileSwitcherDeps.cacheHome = func() (string, error) { return t.TempDir(), nil }
	})

	res := newTestApp().SwitchProfile("alpha", "us-east-1")
	if res.Ok {
		t.Fatalf("SwitchProfile.Ok = true, want false on verify failure")
	}
	if res.Identity != nil {
		t.Errorf("Identity should be nil on failure: %+v", res.Identity)
	}
	if len(res.Suggested) == 0 || !strings.Contains(res.Suggested[0], "aws sso login --profile alpha") {
		t.Errorf("Suggested = %v, want SSO login hint", res.Suggested)
	}
	if res.Error == "" {
		t.Error("Error empty; expected the VerifyError formatted string")
	}
}

func TestSwitchProfileSurfacesNewClientError(t *testing.T) {
	withSwitcherDeps(t, func() {
		profileSwitcherDeps.newClient = func(_ context.Context, _, _, _ string) (*awsx.Client, error) {
			return nil, errors.New("cache home missing")
		}
		profileSwitcherDeps.cacheHome = func() (string, error) { return t.TempDir(), nil }
	})
	res := newTestApp().SwitchProfile("alpha", "us-east-1")
	if res.Ok || !strings.Contains(res.Error, "cache home missing") {
		t.Errorf("SwitchProfile result = %+v, want err containing 'cache home missing'", res)
	}
}

func TestVerifyCurrentUsesEnvProfile(t *testing.T) {
	t.Setenv("AWS_PROFILE", "ops")
	var sawProfile string
	withSwitcherDeps(t, func() {
		profileSwitcherDeps.newClient = func(_ context.Context, profile, _, _ string) (*awsx.Client, error) {
			sawProfile = profile
			return awsx.NewForTest(profile, ""), nil
		}
		profileSwitcherDeps.verify = func(_ context.Context, _ *awsx.Client) (*awsx.Identity, error) {
			return &awsx.Identity{Profile: "ops", Account: "1", Arn: "a", UserId: "u"}, nil
		}
		profileSwitcherDeps.cacheHome = func() (string, error) { return t.TempDir(), nil }
	})
	res := newTestApp().VerifyCurrent()
	if !res.Ok {
		t.Fatalf("VerifyCurrent failed: %+v", res)
	}
	if sawProfile != "ops" {
		t.Errorf("newClient saw profile=%q, want ops", sawProfile)
	}
}
