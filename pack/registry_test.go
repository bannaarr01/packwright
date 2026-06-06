package pack

import (
	"testing"

	"github.com/bannaarr01/packwright/manifest"
)

func mkManifest(slash, title string) *manifest.Manifest {
	return &manifest.Manifest{
		SchemaVersion: "packwright.manifest.v1",
		Kind:          manifest.KindResource,
		Slash:         slash,
		Title:         title,
	}
}

func TestNewRegistryEmpty(t *testing.T) {
	r := NewRegistry(nil)
	if r == nil {
		t.Fatal("NewRegistry returned nil for empty input")
	}
	if got := r.Lookup("/anything"); got != nil {
		t.Errorf("Lookup miss = %v, want nil", got)
	}
	if got := r.List(); len(got) != 0 {
		t.Errorf("List on empty registry = %v, want empty", got)
	}
}

func TestRegistryLookupReturnsRegisteredManifest(t *testing.T) {
	mAlb := mkManifest("/alb", "ALB")
	r := NewRegistry([]*Pack{
		{Name: "p1", Manifests: []*manifest.Manifest{mAlb}},
	})

	got := r.Lookup("/alb")
	if len(got) != 1 || got[0] != mAlb {
		t.Fatalf("Lookup(/alb) = %v, want exactly [%p]", got, mAlb)
	}
}

func TestRegistryLookupMissReturnsNil(t *testing.T) {
	r := NewRegistry([]*Pack{
		{Name: "p1", Manifests: []*manifest.Manifest{mkManifest("/alb", "ALB")}},
	})
	if got := r.Lookup("/missing"); got != nil {
		t.Errorf("Lookup miss = %v, want nil", got)
	}
}

func TestRegistryLookupCollisionPreservesInsertionOrder(t *testing.T) {
	// Two packs both register /alb. The registry's data layer keeps both —
	// resolution UX is MVP-3 PR-04. The slice must come back in the order
	// the packs were supplied.
	first := mkManifest("/alb", "user-scope ALB")
	second := mkManifest("/alb", "vendor ALB")
	third := mkManifest("/alb", "team ALB")

	userScope := &Pack{Name: "user-scope", Manifests: []*manifest.Manifest{first}}
	vendor := &Pack{Name: "vendor", Manifests: []*manifest.Manifest{second}}
	team := &Pack{Name: "team", Manifests: []*manifest.Manifest{third}}

	r := NewRegistry([]*Pack{userScope, vendor, team})

	got := r.Lookup("/alb")
	want := []*manifest.Manifest{first, second, third}
	if len(got) != len(want) {
		t.Fatalf("Lookup len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Lookup[%d] = %q, want %q", i, got[i].Title, want[i].Title)
		}
	}
}

func TestRegistryListInsertionOrder(t *testing.T) {
	a := mkManifest("/a", "A")
	b := mkManifest("/b", "B")
	c := mkManifest("/c", "C")
	r := NewRegistry([]*Pack{
		{Name: "p1", Manifests: []*manifest.Manifest{a, b}},
		{Name: "p2", Manifests: []*manifest.Manifest{c}},
	})

	got := r.List()
	want := []*manifest.Manifest{a, b, c}
	if len(got) != len(want) {
		t.Fatalf("List len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("List[%d] = %q, want %q", i, got[i].Slash, want[i].Slash)
		}
	}
}

func TestRegistrySkipsNilAndUnslashedManifests(t *testing.T) {
	// A nil pack or nil manifest, or a manifest with no slash, must not
	// blow up the registry — discovery may legitimately hand back partial
	// data when failures are reported alongside healthy packs.
	good := mkManifest("/good", "Good")
	noSlash := &manifest.Manifest{Title: "no slash"}
	r := NewRegistry([]*Pack{
		nil,
		{Name: "p", Manifests: []*manifest.Manifest{nil, noSlash, good}},
	})

	got := r.List()
	if len(got) != 1 || got[0] != good {
		t.Fatalf("List = %v, want exactly [%p]", got, good)
	}
	if r.Lookup("") != nil {
		t.Error("Lookup(\"\") should not return the slash-less manifest")
	}
}
