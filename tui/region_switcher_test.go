package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bannaarr01/packwright/awsx"
)

// fakeRegionLister is a deterministic RegionLister the tests drive instead of
// reaching AWS.
type fakeRegionLister struct {
	regions    []string
	gotProfile string
	gotRegion  string
}

func (f *fakeRegionLister) ListRegions(_ context.Context, profile, region string) []string {
	f.gotProfile = profile
	f.gotRegion = region
	return f.regions
}

// newTestRegionSwitcher seeds a switcher with three regions, the first marked
// active, sized large enough to render a row.
func newTestRegionSwitcher(v Verifier, l RegionLister) RegionSwitcher {
	s := NewRegionSwitcher(DefaultKeyMap(),
		[]string{"us-east-1", "eu-west-1", "ap-south-1"},
		"alpha", "us-east-1", "us-east-1", v, l, nil,
	)
	s.SetSize(80, 24)
	return s
}

func TestRegionSwitcherActiveItemHasMarker(t *testing.T) {
	s := newTestRegionSwitcher(nil, nil)
	if rendered := s.View(); !strings.Contains(rendered, "→ us-east-1") {
		t.Fatalf("View() did not show active marker for us-east-1:\n%s", rendered)
	}
}

func TestRegionSwitcherEnterTriggersVerify(t *testing.T) {
	fv := &fakeVerifier{identity: &awsx.Identity{Region: "us-east-1", Account: "111122223333"}}
	s := newTestRegionSwitcher(fv, nil)

	_, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter returned nil cmd; expected verify command")
	}
	got, ok := cmd().(RegionSwitcherMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want RegionSwitcherMsg", cmd())
	}
	if got.Region != "us-east-1" {
		t.Errorf("emitted msg region = %q, want us-east-1", got.Region)
	}
	if got.Err != nil {
		t.Errorf("Err = %v, want nil", got.Err)
	}
	if got.Identity == nil || got.Identity.Account != "111122223333" {
		t.Errorf("Identity = %+v, want Account=111122223333", got.Identity)
	}
	// The profile is held fixed; only the region varies on a region switch.
	if fv.gotName != "alpha" {
		t.Errorf("Verifier saw profile=%q, want alpha (held fixed)", fv.gotName)
	}
	if fv.gotRegn != "us-east-1" {
		t.Errorf("Verifier saw region=%q, want us-east-1", fv.gotRegn)
	}
}

func TestRegionSwitcherLoadReplacesList(t *testing.T) {
	s := newTestRegionSwitcher(nil, nil)
	next, _ := s.Update(regionsLoadedMsg{regions: []string{"ap-southeast-2", "ca-central-1"}})

	items := next.list.Items()
	if len(items) != 2 {
		t.Fatalf("after regionsLoadedMsg, list has %d items, want 2", len(items))
	}
	if got := items[0].(regionItem).name; got != "ap-southeast-2" {
		t.Errorf("first region = %q, want ap-southeast-2", got)
	}
}

func TestRegionSwitcherLoadIgnoresEmpty(t *testing.T) {
	s := newTestRegionSwitcher(nil, nil)
	before := len(s.list.Items())
	next, _ := s.Update(regionsLoadedMsg{regions: nil})
	if after := len(next.list.Items()); after != before {
		t.Errorf("empty regionsLoadedMsg changed list size from %d to %d", before, after)
	}
}

func TestRegionSwitcherInitLoadsRegions(t *testing.T) {
	fl := &fakeRegionLister{regions: []string{"sa-east-1"}}
	s := newTestRegionSwitcher(nil, fl)

	cmd := s.initCmd()
	if cmd == nil {
		t.Fatal("initCmd returned nil with a lister wired")
	}
	loaded, ok := cmd().(regionsLoadedMsg)
	if !ok {
		t.Fatalf("initCmd produced %T, want regionsLoadedMsg", cmd())
	}
	if len(loaded.regions) != 1 || loaded.regions[0] != "sa-east-1" {
		t.Errorf("loaded regions = %v, want [sa-east-1]", loaded.regions)
	}
	if fl.gotProfile != "alpha" || fl.gotRegion != "us-east-1" {
		t.Errorf("lister saw (%q, %q), want (alpha, us-east-1)", fl.gotProfile, fl.gotRegion)
	}
}

func TestRegionSwitcherInitNilLister(t *testing.T) {
	s := newTestRegionSwitcher(nil, nil)
	if cmd := s.initCmd(); cmd != nil {
		t.Error("initCmd with nil lister returned a cmd, want nil")
	}
}

// ensure the seed list type is what the load path expects.
var _ list.Item = regionItem{}
