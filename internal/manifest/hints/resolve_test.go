package hints

import (
	"testing"

	"github.com/bannaarr01/packwright/internal/manifest"
)

// TestResolveAuthorOverrideWins is the headline rule from ADR-0051: when an
// author has spelled out `placeholder:` on a field, that value is used even if
// a catalogue default exists for the same type. The catalogue must never
// silently overwrite an author's deliberate choice.
func TestResolveAuthorOverrideWins(t *testing.T) {
	f := manifest.Field{
		Type:        manifest.FieldTypeAWSVPCID,
		Placeholder: "vpc-author-supplied",
	}
	if got := Resolve(f); got != "vpc-author-supplied" {
		t.Errorf("Resolve(author override) = %q, want %q", got, "vpc-author-supplied")
	}
}

// TestResolveFallsBackToCatalogue covers the typical case: a manifest with no
// placeholder gets the type-default from the built-in catalogue.
func TestResolveFallsBackToCatalogue(t *testing.T) {
	f := manifest.Field{Type: manifest.FieldTypeAWSVPCID}
	want := Catalogue[string(manifest.FieldTypeAWSVPCID)]
	if got := Resolve(f); got != want {
		t.Errorf("Resolve(catalogue fallback) = %q, want %q", got, want)
	}
}

// TestResolveUnknownTypeReturnsEmpty exercises the bottom of the precedence
// chain: a type with no author override and no catalogue entry resolves to
// "", which the form layers treat as "show no placeholder".
func TestResolveUnknownTypeReturnsEmpty(t *testing.T) {
	f := manifest.Field{Type: manifest.FieldType("custom/never-heard-of-it")}
	if got := Resolve(f); got != "" {
		t.Errorf("Resolve(unknown type, no override) = %q, want %q", got, "")
	}
}

// TestResolveGenericStringTypeFallsBackToEmpty pins the deliberate "no hint"
// behaviour for generic widgets: type=string with no author override yields
// "" because Catalogue["string"] is intentionally empty.
func TestResolveGenericStringTypeFallsBackToEmpty(t *testing.T) {
	f := manifest.Field{Type: manifest.FieldTypeString}
	if got := Resolve(f); got != "" {
		t.Errorf("Resolve(string, no override) = %q, want %q (catalogue intentionally empty)", got, "")
	}
}

// TestResolveAuthorOverrideWinsForGenericType is the override path's mirror
// case: even on a generic type whose catalogue entry is "", an author can
// still surface a hint by setting Placeholder explicitly.
func TestResolveAuthorOverrideWinsForGenericType(t *testing.T) {
	f := manifest.Field{
		Type:        manifest.FieldTypeString,
		Placeholder: "e.g. my-service",
	}
	if got := Resolve(f); got != "e.g. my-service" {
		t.Errorf("Resolve(author override on generic) = %q, want %q", got, "e.g. my-service")
	}
}
