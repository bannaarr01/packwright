package hints

import "testing"

// TestCatalogueCoversAdrTypes locks the ADR-0051 catalogue keys in place so a
// future refactor cannot silently drop a type. The exact placeholder values
// are intentionally not snapshotted — drift on a single value (e.g. a new AWS
// ARN format) should not break the test — but the *keyset* is the contract.
func TestCatalogueCoversAdrTypes(t *testing.T) {
	wantKeys := []string{
		"aws/vpc-id",
		"aws/subnet-id",
		"aws/subnet-ids",
		"aws/sg-id",
		"aws/acm-arn",
		"aws/region",
		"aws/account-id",
		"aws/instance-type",
		"cidr",
		"domain",
		"stack-name",
		"string",
		"int",
		"bool",
		"enum",
	}
	for _, k := range wantKeys {
		if _, ok := Catalogue[k]; !ok {
			t.Errorf("Catalogue missing required ADR-0051 key %q", k)
		}
	}
}

// TestCatalogueAWSEntriesAreNonEmpty asserts that the typed AWS fields (the
// ones a picker is going to drive) all carry a concrete example. An empty
// entry here would surface to the user as a blank input on a typed picker —
// the worst-of-both outcome the ADR explicitly avoids.
func TestCatalogueAWSEntriesAreNonEmpty(t *testing.T) {
	awsKeys := []string{
		"aws/vpc-id",
		"aws/subnet-id",
		"aws/subnet-ids",
		"aws/sg-id",
		"aws/acm-arn",
		"aws/region",
		"aws/account-id",
		"aws/instance-type",
	}
	for _, k := range awsKeys {
		if Catalogue[k] == "" {
			t.Errorf("Catalogue[%q] = \"\", want a non-empty example", k)
		}
	}
}

// TestCatalogueGenericEntriesAreEmpty pins the deliberate "no hint" decision
// for generic widgets (ADR-0051 alternatives §"over-hinting on generic
// types"). If someone fills these in, the resolver would start showing noisy
// hints on every text/number/checkbox field.
func TestCatalogueGenericEntriesAreEmpty(t *testing.T) {
	for _, k := range []string{"string", "int", "bool", "enum"} {
		if got := Catalogue[k]; got != "" {
			t.Errorf("Catalogue[%q] = %q, want empty (over-hinting guard)", k, got)
		}
	}
}

func TestLookupKnownReturnsCatalogueValue(t *testing.T) {
	got := Lookup("aws/vpc-id")
	want := Catalogue["aws/vpc-id"]
	if got != want {
		t.Errorf("Lookup(aws/vpc-id) = %q, want %q", got, want)
	}
}

func TestLookupUnknownReturnsEmptyString(t *testing.T) {
	if got := Lookup("does-not-exist"); got != "" {
		t.Errorf("Lookup(unknown) = %q, want %q", got, "")
	}
}
