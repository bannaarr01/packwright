package pack

import (
	"testing"

	"github.com/bannaarr01/packwright/manifest"
)

func TestTagEmptyInput(t *testing.T) {
	if got := Tag(nil); got != nil {
		t.Errorf("Tag(nil) = %v, want nil", got)
	}
	if got := Tag([]*Pack{}); got != nil {
		t.Errorf("Tag([]) = %v, want nil", got)
	}
}

func TestTagPackScopeUsesPackName(t *testing.T) {
	m := mkManifest("/foo", "Foo")
	p := &Pack{Name: "acme-platform", Manifests: []*manifest.Manifest{m}}

	got := Tag([]*Pack{p})
	if len(got) != 1 {
		t.Fatalf("Tag len = %d, want 1", len(got))
	}
	if got[0].Manifest != m {
		t.Errorf("Manifest = %p, want %p", got[0].Manifest, m)
	}
	if got[0].Scope != ScopePack {
		t.Errorf("Scope = %q, want %q", got[0].Scope, ScopePack)
	}
	if got[0].SourcePack != "acme-platform" {
		t.Errorf("SourcePack = %q, want %q", got[0].SourcePack, "acme-platform")
	}
}

func TestTagUserScopeNamedPackIsUser(t *testing.T) {
	// A pack whose Name == UserScopeName is treated as user scope and reports
	// an empty SourcePack — the synthetic user-scope pack has no public name.
	m := mkManifest("/restart", "Restart API")
	p := &Pack{Name: UserScopeName, Manifests: []*manifest.Manifest{m}}

	got := Tag([]*Pack{p})
	if len(got) != 1 {
		t.Fatalf("Tag len = %d, want 1", len(got))
	}
	if got[0].Scope != ScopeUser {
		t.Errorf("Scope = %q, want %q", got[0].Scope, ScopeUser)
	}
	if got[0].SourcePack != "" {
		t.Errorf("SourcePack = %q, want empty for user scope", got[0].SourcePack)
	}
}

func TestTagPreservesPackAndManifestOrder(t *testing.T) {
	a := mkManifest("/a", "A")
	b := mkManifest("/b", "B")
	c := mkManifest("/c", "C")
	d := mkManifest("/d", "D")

	user := &Pack{Name: UserScopeName, Manifests: []*manifest.Manifest{a}}
	p1 := &Pack{Name: "p1", Manifests: []*manifest.Manifest{b, c}}
	p2 := &Pack{Name: "p2", Manifests: []*manifest.Manifest{d}}

	got := Tag([]*Pack{user, p1, p2})
	want := []Tagged{
		{Manifest: a, Scope: ScopeUser, SourcePack: ""},
		{Manifest: b, Scope: ScopePack, SourcePack: "p1"},
		{Manifest: c, Scope: ScopePack, SourcePack: "p1"},
		{Manifest: d, Scope: ScopePack, SourcePack: "p2"},
	}
	if len(got) != len(want) {
		t.Fatalf("Tag len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("Tag[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestTagSkipsNilPacksAndManifests(t *testing.T) {
	good := mkManifest("/good", "Good")
	got := Tag([]*Pack{
		nil,
		{Name: "p", Manifests: []*manifest.Manifest{nil, good, nil}},
	})
	if len(got) != 1 {
		t.Fatalf("Tag len = %d, want 1", len(got))
	}
	if got[0].Manifest != good {
		t.Errorf("Manifest = %p, want %p", got[0].Manifest, good)
	}
}
