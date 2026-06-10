package manifest

import (
	"errors"
	"strings"
	"testing"
)

func TestParseSchemaMajor(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"packwright.manifest.v1", 1, false},
		{"packwright.manifest.v2", 2, false},
		{"packwright.manifest.v42", 42, false},
		{"packwright.manifest.V3", 3, false},
		{"", 0, true},
		{"v1", 0, true},
		{"packwright.manifest.", 0, true},
		{"packwright.manifest.v", 0, true},
		{"packwright.manifest.vNope", 0, true},
		{"packwright.manifest.v-1", 0, true},
		{"packwright.manifestv1", 0, true},
	}
	for _, c := range cases {
		got, err := ParseSchemaMajor(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseSchemaMajor(%q) error = nil, want error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSchemaMajor(%q) error = %v, want nil", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseSchemaMajor(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestFormatSchemaVersion(t *testing.T) {
	if got := FormatSchemaVersion(1); got != "packwright.manifest.v1" {
		t.Errorf("FormatSchemaVersion(1) = %q, want %q", got, "packwright.manifest.v1")
	}
	if got := FormatSchemaVersion(7); got != "packwright.manifest.v7" {
		t.Errorf("FormatSchemaVersion(7) = %q, want %q", got, "packwright.manifest.v7")
	}
}

func TestCheckSchemaVersionAcceptsCurrent(t *testing.T) {
	m := &Manifest{SchemaVersion: SchemaVersionV1}
	if err := checkSchemaVersion(m); err != nil {
		t.Fatalf("checkSchemaVersion(v1) = %v, want nil", err)
	}
}

func TestCheckSchemaVersionEmptyDefersToValidate(t *testing.T) {
	// An empty schema_version should not trip the hook — Validate owns the
	// "is required" diagnostic so the user-visible error path is single-
	// sourced.
	m := &Manifest{SchemaVersion: ""}
	if err := checkSchemaVersion(m); err != nil {
		t.Fatalf("checkSchemaVersion(empty) = %v, want nil", err)
	}
}

func TestCheckSchemaVersionRejectsUnsupportedMajor(t *testing.T) {
	m := &Manifest{SchemaVersion: "packwright.manifest.v9"}
	err := checkSchemaVersion(m)
	if err == nil {
		t.Fatal("checkSchemaVersion(v9) = nil, want validation error")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type = %T (%v), want *ValidationError", err, err)
	}
	if ve.Path != "schema_version" {
		t.Errorf("ValidationError.Path = %q, want %q", ve.Path, "schema_version")
	}
	if !strings.Contains(ve.Reason, "unsupported") {
		t.Errorf("ValidationError.Reason = %q, want it to mention unsupported", ve.Reason)
	}
}

func TestCheckSchemaVersionRejectsMalformed(t *testing.T) {
	m := &Manifest{SchemaVersion: "not-a-schema-version"}
	err := checkSchemaVersion(m)
	if err == nil {
		t.Fatal("checkSchemaVersion(garbage) = nil, want validation error")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type = %T (%v), want *ValidationError", err, err)
	}
	if ve.Path != "schema_version" {
		t.Errorf("ValidationError.Path = %q, want %q", ve.Path, "schema_version")
	}
}

func TestCheckSchemaVersionNilManifest(t *testing.T) {
	if err := checkSchemaVersion(nil); err == nil {
		t.Error("checkSchemaVersion(nil) = nil, want error")
	}
}

func TestSchemaVersionCheckHookIsSwappable(t *testing.T) {
	// Confirms the package-level variable contract: tests can replace the
	// check, and a restore via t.Cleanup unwinds the swap.
	called := false
	prev := schemaVersionCheck
	schemaVersionCheck = func(*Manifest) error {
		called = true
		return nil
	}
	t.Cleanup(func() { schemaVersionCheck = prev })

	if err := schemaVersionCheck(&Manifest{SchemaVersion: "anything"}); err != nil {
		t.Errorf("hook returned %v, want nil from custom hook", err)
	}
	if !called {
		t.Error("custom hook not invoked")
	}
}
