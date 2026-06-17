package audit_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/internal/audit"
	_ "github.com/bannaarr01/packwright/internal/audit/scanners"
)

// fakeScanner is a minimal Scanner used to exercise the registry's
// validation contract. Tests construct one per case so the
// kind/permissions surface is exactly what the case under test
// requires.
type fakeScanner struct {
	kind  string
	perms []string
}

func (f fakeScanner) Kind() string          { return f.kind }
func (f fakeScanner) Permissions() []string { return f.perms }
func (fakeScanner) Scan(context.Context, *audit.Client, audit.ScannerEmitter) ([]audit.Resource, error) {
	return nil, nil
}

// TestRegistryRejectsMutatingPermissions is the read-only-by-construction
// invariant from ADR-0040: a scanner whose Permissions list names a
// mutating action is rejected at Register time. The DoD names
// "ec2:DeleteVolume" specifically; the table extends the case to every
// forbidden verb the registry knows about so an allowlist regression
// trips a named subtest.
func TestRegistryRejectsMutatingPermissions(t *testing.T) {
	cases := []struct {
		name  string
		perms []string
	}{
		{"DeleteVolume", []string{"ec2:DeleteVolume"}},
		{"ModifyInstance", []string{"ec2:ModifyInstanceAttribute"}},
		{"UpdateStack", []string{"cloudformation:UpdateStack"}},
		{"CreateBucket", []string{"s3:CreateBucket"}},
		{"PutObject", []string{"s3:PutObject"}},
		{"StartInstances", []string{"ec2:StartInstances"}}, // not Describe/List/Get
		{"NoColon", []string{"ec2DescribeInstances"}},
		{"EmptyAction", []string{"ec2:"}},
		{"EmptyService", []string{":DescribeInstances"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			r := audit.NewRegistry()
			err := r.Register(fakeScanner{kind: "fake/" + tc.name, perms: tc.perms})
			if err == nil {
				t.Fatalf("Register accepted forbidden permissions %v; want error", tc.perms)
			}
		})
	}
}

// TestRegistryAcceptsReadOnlyPermissions exercises the positive side of
// the allowlist so the negative test alone cannot pass against a broken
// implementation (e.g. one that rejects everything).
func TestRegistryAcceptsReadOnlyPermissions(t *testing.T) {
	cases := [][]string{
		{"ec2:DescribeInstances"},
		{"rds:DescribeDBSnapshots", "rds:DescribeDBInstances"},
		{"s3:ListBuckets", "s3:GetBucketTagging"},
		{"logs:DescribeLogGroups"},
	}
	for _, perms := range cases {
		perms := perms
		t.Run(strings.Join(perms, ","), func(t *testing.T) {
			r := audit.NewRegistry()
			if err := r.Register(fakeScanner{kind: "fake/" + perms[0], perms: perms}); err != nil {
				t.Fatalf("Register(%v) returned error: %v", perms, err)
			}
		})
	}
}

// TestRegistryRejectsDuplicateKind locks in the contract that a second
// scanner with the same Kind() does not silently overwrite the first.
func TestRegistryRejectsDuplicateKind(t *testing.T) {
	r := audit.NewRegistry()
	if err := r.Register(fakeScanner{kind: "fake/dup", perms: []string{"ec2:DescribeInstances"}}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := r.Register(fakeScanner{kind: "fake/dup", perms: []string{"ec2:DescribeInstances"}})
	if err == nil {
		t.Fatal("second Register returned nil; want duplicate-kind error")
	}
}

// TestRegistryRejectsEmpty asserts the registry's own preconditions:
// nil scanner, empty kind, and empty permissions list are all errors.
func TestRegistryRejectsEmpty(t *testing.T) {
	r := audit.NewRegistry()
	if err := r.Register(nil); err == nil {
		t.Error("Register(nil) returned nil")
	}
	if err := r.Register(fakeScanner{kind: "", perms: []string{"ec2:DescribeInstances"}}); err == nil {
		t.Error("Register with empty kind returned nil")
	}
	if err := r.Register(fakeScanner{kind: "fake", perms: nil}); err == nil {
		t.Error("Register with nil permissions returned nil")
	}
}

// TestMustRegisterPanicsOnInvalid lets scanner init functions stay one-
// liners while still failing the program at startup on a bad list.
func TestMustRegisterPanicsOnInvalid(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustRegister with mutating permission did not panic")
		}
	}()
	audit.NewRegistry().MustRegister(fakeScanner{kind: "fake/must", perms: []string{"ec2:DeleteVolume"}})
}

// TestDefaultRegistryAllReadOnly walks every scanner registered with
// the package-level Default and asserts the regex-shape contract the
// task description names verbatim: no scanner's Permissions returns a
// string containing "Delete|Modify|Update|Create|Put". The init imports
// for internal/audit/scanners populate Default.
func TestDefaultRegistryAllReadOnly(t *testing.T) {
	forbidden := regexp.MustCompile(`Delete|Modify|Update|Create|Put`)
	all := audit.Default.All()
	if len(all) < 11 {
		t.Fatalf("Default registry has %d scanners; ADR-0040 requires at least 11", len(all))
	}
	for _, s := range all {
		for _, p := range s.Permissions() {
			if forbidden.MatchString(p) {
				t.Errorf("scanner %q has forbidden permission %q", s.Kind(), p)
			}
			if !strings.Contains(p, ":") {
				t.Errorf("scanner %q permission %q is not in service:Action form", s.Kind(), p)
			}
		}
	}
}

// TestDefaultRegistryKindsAreUnique guards against two scanner files
// claiming the same Kind() — the registry already rejects duplicates at
// init time (the MustRegister panic would fail the test binary
// startup), but this explicit assertion makes the contract testable in
// isolation.
func TestDefaultRegistryKindsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range audit.Default.All() {
		if seen[s.Kind()] {
			t.Errorf("duplicate kind %q in Default registry", s.Kind())
		}
		seen[s.Kind()] = true
	}
}
