package pack

import (
	"errors"
	"testing"

	"github.com/bannaarr01/packwright/internal/version"
)

func TestCheckEmptyRequiresAcceptsAnyVersion(t *testing.T) {
	t.Cleanup(version.Set("v0.5.0"))
	if err := Check("anon", nil, CheckOptions{}); err != nil {
		t.Errorf("Check(nil) = %v, want nil", err)
	}
	if err := Check("anon", map[string]string{}, CheckOptions{}); err != nil {
		t.Errorf("Check(empty map) = %v, want nil", err)
	}
}

func TestCheckRejectsAppTooOld(t *testing.T) {
	// The headline DOD case from the PR-02 plan: a pack pinning to a future
	// app version must be rejected against today's running build.
	t.Cleanup(version.Set("v0.5.0"))
	err := Check("acme-platform", map[string]string{
		ModulePackwright: ">=99.0.0",
	}, CheckOptions{})
	if err == nil {
		t.Fatal("Check(>=99.0.0 against v0.5.0) = nil, want *RequiresError")
	}
	var re *RequiresError
	if !errors.As(err, &re) {
		t.Fatalf("error type = %T (%v), want *RequiresError", err, err)
	}
	if re.Module != ModulePackwright {
		t.Errorf("Module = %q, want %q", re.Module, ModulePackwright)
	}
	if re.Have != "v0.5.0" {
		t.Errorf("Have = %q, want %q", re.Have, "v0.5.0")
	}
	if re.PackName != "acme-platform" {
		t.Errorf("PackName = %q, want %q", re.PackName, "acme-platform")
	}
}

func TestCheckAcceptsAppInRange(t *testing.T) {
	t.Cleanup(version.Set("v0.5.0"))
	err := Check("p", map[string]string{
		ModulePackwright: ">=0.4.0 <0.6.0",
	}, CheckOptions{})
	if err != nil {
		t.Errorf("Check(v0.5.0 in [0.4.0, 0.6.0)) = %v, want nil", err)
	}
}

func TestCheckRejectsAppAboveUpperBound(t *testing.T) {
	t.Cleanup(version.Set("v0.7.1"))
	err := Check("acme", map[string]string{
		ModulePackwright: ">=0.4.0 <0.6.0",
	}, CheckOptions{})
	if err == nil {
		t.Fatal("Check(v0.7.1 vs <0.6.0) = nil, want *RequiresError")
	}
	var re *RequiresError
	if !errors.As(err, &re) {
		t.Fatalf("error type = %T, want *RequiresError", err)
	}
}

func TestCheckDevBuildBypassesAppConstraint(t *testing.T) {
	t.Cleanup(version.Set(version.Dev))
	err := Check("acme", map[string]string{
		ModulePackwright: ">=99.0.0",
	}, CheckOptions{})
	if err != nil {
		t.Errorf("Check(dev build) = %v, want nil (dev bypasses)", err)
	}
}

func TestCheckCustomRunningVersion(t *testing.T) {
	// Explicit RunningAppVersion overrides version.Get() — used by tests
	// and any future caller that wants to evaluate constraints against a
	// hypothetical build.
	t.Cleanup(version.Set("v0.1.0"))
	err := Check("p", map[string]string{
		ModulePackwright: ">=0.5.0",
	}, CheckOptions{RunningAppVersion: "v0.5.0"})
	if err != nil {
		t.Errorf("Check(running=v0.5.0) = %v, want nil", err)
	}
}

func TestCheckManifestSchemaMatch(t *testing.T) {
	t.Cleanup(version.Set("v0.5.0"))
	err := Check("p", map[string]string{
		ModuleManifestSchema: "v1",
	}, CheckOptions{})
	if err != nil {
		t.Errorf("Check(manifest=v1) = %v, want nil", err)
	}
}

func TestCheckManifestSchemaMismatch(t *testing.T) {
	t.Cleanup(version.Set("v0.5.0"))
	err := Check("p", map[string]string{
		ModuleManifestSchema: "v2",
	}, CheckOptions{})
	if err == nil {
		t.Fatal("Check(manifest=v2 vs current v1) = nil, want error")
	}
	var re *RequiresError
	if !errors.As(err, &re) {
		t.Fatalf("error type = %T, want *RequiresError", err)
	}
	if re.Module != ModuleManifestSchema {
		t.Errorf("Module = %q, want %q", re.Module, ModuleManifestSchema)
	}
}

func TestCheckManifestSchemaOrList(t *testing.T) {
	t.Cleanup(version.Set("v0.5.0"))
	err := Check("p", map[string]string{
		ModuleManifestSchema: "v1, v2",
	}, CheckOptions{})
	if err != nil {
		t.Errorf("Check(manifest=v1,v2) = %v, want nil", err)
	}
}

func TestCheckIgnoreManifestSkipsSchema(t *testing.T) {
	t.Cleanup(version.Set("v0.5.0"))
	err := Check("p", map[string]string{
		ModuleManifestSchema: "v9",
	}, CheckOptions{IgnoreManifest: true})
	if err != nil {
		t.Errorf("Check(IgnoreManifest) = %v, want nil", err)
	}
}

func TestCheckCustomManifestMajor(t *testing.T) {
	t.Cleanup(version.Set("v0.5.0"))
	err := Check("p", map[string]string{
		ModuleManifestSchema: "v2",
	}, CheckOptions{CurrentManifestMajor: 2})
	if err != nil {
		t.Errorf("Check(currentMajor=2, requires v2) = %v, want nil", err)
	}
}

func TestCheckMalformedAppConstraintReturnsError(t *testing.T) {
	t.Cleanup(version.Set("v0.5.0"))
	err := Check("p", map[string]string{
		ModulePackwright: ">=not-a-version",
	}, CheckOptions{})
	if err == nil {
		t.Fatal("Check(malformed constraint) = nil, want parse error")
	}
	// Not a *RequiresError — this is a constraint-parse error, not a
	// version mismatch.
	var re *RequiresError
	if errors.As(err, &re) {
		t.Errorf("error = %T, want non-*RequiresError parse error", err)
	}
}

func TestRequiresErrorMessage(t *testing.T) {
	e := &RequiresError{
		PackName:   "acme-platform",
		Module:     ModulePackwright,
		Constraint: ">=0.4.0 <0.6.0",
		Have:       "v0.7.1",
	}
	got := e.Error()
	want := `pack "acme-platform" requires packwright >=0.4.0 <0.6.0; you have v0.7.1`
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestRequiresErrorMessageDegradesGracefullyWithoutPackName(t *testing.T) {
	e := &RequiresError{
		Module:     ModuleManifestSchema,
		Constraint: "v1",
		Have:       "v2",
	}
	got := e.Error()
	want := `pack "<unknown>" requires packwright.manifest v1; you have v2`
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestMatchSemverComparators(t *testing.T) {
	// Each case must run with the same running version so the matrix is
	// easy to scan; "v1.2.3" sits in the middle of the comparator targets.
	const running = "v1.2.3"
	cases := []struct {
		comp string
		want bool
	}{
		{"=v1.2.3", true},
		{"==v1.2.3", true},
		{"v1.2.3", true},
		{"!=v1.2.3", false},
		{">v1.2.2", true},
		{">v1.2.3", false},
		{">=v1.2.3", true},
		{">=v1.2.4", false},
		{"<v1.3.0", true},
		{"<v1.2.3", false},
		{"<=v1.2.3", true},
		{"<=v1.2.2", false},
		{"^v1.0.0", true},
		{"^v2.0.0", false},
		{"~v1.2.0", true},
		{"~v1.3.0", false},
	}
	for _, c := range cases {
		got, err := matchSemverComparator(c.comp, running)
		if err != nil {
			t.Errorf("matchSemverComparator(%q, %q) error = %v", c.comp, running, err)
			continue
		}
		if got != c.want {
			t.Errorf("matchSemverComparator(%q, %q) = %v, want %v",
				c.comp, running, got, c.want)
		}
	}
}

func TestMatchSemverConstraintAndSemantics(t *testing.T) {
	// Space-separated comparators combine with AND semantics per ADR-0028.
	ok, err := matchSemverConstraint(">=0.4.0 <0.6.0", "v0.5.0")
	if err != nil {
		t.Fatalf("constraint parse error: %v", err)
	}
	if !ok {
		t.Error("v0.5.0 should satisfy >=0.4.0 <0.6.0")
	}
	ok, err = matchSemverConstraint(">=0.4.0 <0.6.0", "v0.7.0")
	if err != nil {
		t.Fatalf("constraint parse error: %v", err)
	}
	if ok {
		t.Error("v0.7.0 should NOT satisfy >=0.4.0 <0.6.0")
	}
}
