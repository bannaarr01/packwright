package manifest

import (
	"strings"
	"testing"
)

func TestCanRunResourceReturnsNil(t *testing.T) {
	m := &Manifest{Kind: KindResource}
	if err := CanRun(m); err != nil {
		t.Fatalf("CanRun(resource) error = %v, want nil", err)
	}
}

func TestCanRunShellReturnsUnsupported(t *testing.T) {
	assertCanRunUnsupported(t, KindShell)
}

func TestCanRunMonitorReturnsUnsupported(t *testing.T) {
	assertCanRunUnsupported(t, KindMonitor)
}

func TestCanRunCompositeReturnsUnsupported(t *testing.T) {
	assertCanRunUnsupported(t, KindComposite)
}

func TestCanRunUnknownKindReturnsError(t *testing.T) {
	err := CanRun(&Manifest{Kind: Kind("frobnicate")})
	if err == nil {
		t.Fatal("CanRun(unknown) error = nil, want unknown-kind error")
	}
	if !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("CanRun(unknown) error = %v, want it to mention %q", err, "unknown kind")
	}
}

func TestCanRunNilManifestReturnsError(t *testing.T) {
	if err := CanRun(nil); err == nil {
		t.Fatal("CanRun(nil) error = nil, want nil-manifest error")
	}
}

func TestKnownFieldTypesCoverPlannedSet(t *testing.T) {
	// Sanity check that every FieldTypeXxx constant is in the allow-list. If
	// someone adds a constant without registering it, Validate would silently
	// reject manifests that use it.
	for _, ft := range []FieldType{
		FieldTypeString, FieldTypeInt, FieldTypeBool, FieldTypeEnum,
		FieldTypeMultistring, FieldTypeSecret,
		FieldTypeAWSVPCID, FieldTypeAWSSubnetID, FieldTypeAWSSGID, FieldTypeAWSACMArn,
	} {
		if _, ok := knownFieldTypes[ft]; !ok {
			t.Errorf("FieldType %q is declared but not in knownFieldTypes", ft)
		}
	}
}

func TestValidationErrorErrorFormat(t *testing.T) {
	ve := &ValidationError{Path: "form[1].id", Reason: "is required"}
	if got, want := ve.Error(), "manifest: form[1].id: is required"; got != want {
		t.Fatalf("ValidationError.Error() = %q, want %q", got, want)
	}

	veNoPath := &ValidationError{Reason: "manifest is nil"}
	if got, want := veNoPath.Error(), "manifest: manifest is nil"; got != want {
		t.Fatalf("ValidationError.Error() = %q, want %q", got, want)
	}
}

// assertCanRunUnsupported is the shared assertion for the kinds that are
// recognised but not runnable in MVP-1. Each kind has its own top-level test
// function so a failure points at the exact case rather than a loop index.
func assertCanRunUnsupported(t *testing.T, k Kind) {
	t.Helper()
	err := CanRun(&Manifest{Kind: k})
	if err == nil {
		t.Fatalf("CanRun(%q) error = nil, want unsupported error", k)
	}
	if !strings.Contains(err.Error(), "not yet supported") {
		t.Fatalf("CanRun(%q) error = %v, want it to mention %q", k, err, "not yet supported")
	}
}
