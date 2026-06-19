package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/config"
)

func TestAWSContextPaletteItemsOffersProfileAndRegion(t *testing.T) {
	items := awsContextPaletteItems()
	want := map[string]bool{slashProfile: false, slashRegion: false}
	for _, it := range items {
		pi, ok := it.(paletteItem)
		if !ok {
			t.Fatalf("palette item is %T, want paletteItem", it)
		}
		if _, tracked := want[pi.slash]; tracked {
			want[pi.slash] = true
		}
	}
	for slash, seen := range want {
		if !seen {
			t.Errorf("awsContextPaletteItems missing %q", slash)
		}
	}
}

func TestProfileSwitcherMsgPersistsContext(t *testing.T) {
	t.Setenv("PACKWRIGHT_HOME", t.TempDir())
	a := newApp(nil, nil)
	a.cfg = &config.Config{}

	model, _ := a.Update(ProfileSwitcherMsg{
		Profile:  "alpha",
		Identity: &awsx.Identity{Profile: "alpha", Region: "us-west-2", Account: "1"},
	})
	a = model.(app)

	if a.cfg.Profile != "alpha" || a.cfg.Region != "us-west-2" {
		t.Fatalf("in-memory cfg = %+v, want profile=alpha region=us-west-2", a.cfg)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if cfg.Profile != "alpha" || cfg.Region != "us-west-2" {
		t.Errorf("persisted cfg = profile=%q region=%q, want alpha/us-west-2", cfg.Profile, cfg.Region)
	}
}

func TestProfileSwitcherMsgErrorDoesNotPersist(t *testing.T) {
	t.Setenv("PACKWRIGHT_HOME", t.TempDir())
	a := newApp(nil, nil)
	a.cfg = &config.Config{Profile: "keep", Region: "us-east-1"}

	model, _ := a.Update(ProfileSwitcherMsg{Profile: "alpha", Err: errForTest("boom")})
	a = model.(app)

	if a.cfg.Profile != "keep" || a.cfg.Region != "us-east-1" {
		t.Errorf("cfg mutated on failed switch: %+v", a.cfg)
	}
}

func TestRegionSwitcherMsgPersistsRegion(t *testing.T) {
	t.Setenv("PACKWRIGHT_HOME", t.TempDir())
	a := newApp(nil, nil)
	a.cfg = &config.Config{Profile: "ops", Region: "us-east-1"}

	model, _ := a.Update(RegionSwitcherMsg{
		Region:   "eu-west-1",
		Identity: &awsx.Identity{Profile: "ops", Region: "eu-west-1", Account: "1"},
	})
	a = model.(app)

	if a.cfg.Region != "eu-west-1" {
		t.Fatalf("in-memory region = %q, want eu-west-1", a.cfg.Region)
	}
	if a.cfg.Profile != "ops" {
		t.Errorf("profile changed on region switch: %q, want ops", a.cfg.Profile)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if cfg.Region != "eu-west-1" || cfg.Profile != "ops" {
		t.Errorf("persisted cfg = profile=%q region=%q, want ops/eu-west-1", cfg.Profile, cfg.Region)
	}
}

func TestContextLabelFallbacks(t *testing.T) {
	a := newApp(nil, nil)
	a.cfg = &config.Config{}
	if got := a.contextLabel(); got != "default · -" {
		t.Errorf("contextLabel with empty cfg = %q, want %q", got, "default · -")
	}
	a.cfg = &config.Config{Profile: "ops", Region: "eu-west-1"}
	if got := a.contextLabel(); got != "ops · eu-west-1" {
		t.Errorf("contextLabel = %q, want %q", got, "ops · eu-west-1")
	}
}

// errForTest is a tiny error value for tests that need a non-nil error without
// importing errors at every call site.
type errForTest string

func (e errForTest) Error() string { return string(e) }

var _ tea.Msg = ProfileSwitcherMsg{}
