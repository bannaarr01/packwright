package manifest

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestFieldPlaceholderYAMLRoundTrip is the DOD round-trip from ADR-0051: a
// Field with Placeholder set must Marshal → UnmarshalStrict back to a Field
// whose Placeholder is byte-identical. UnmarshalStrict (via KnownFields(true))
// is the same strict mode Load uses, so the test guards against the tag
// drifting away from the manifest loader's accepted keyset.
func TestFieldPlaceholderYAMLRoundTrip(t *testing.T) {
	want := Field{
		ID:          "VpcId",
		Label:       "VPC",
		Type:        FieldTypeAWSVPCID,
		Placeholder: "vpc-0abc1234567890abcdef",
		Required:    true,
	}

	raw, err := yaml.Marshal(want)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	if !strings.Contains(string(raw), "placeholder: vpc-0abc1234567890abcdef") {
		t.Fatalf("Marshal omitted placeholder; got:\n%s", raw)
	}

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var got Field
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("yaml.Decode (strict): %v\n---\n%s", err, raw)
	}
	if got.Placeholder != want.Placeholder {
		t.Errorf("Placeholder = %q, want %q", got.Placeholder, want.Placeholder)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Field round-trip drifted:\n got = %#v\nwant = %#v", got, want)
	}
}

// TestFieldPlaceholderOmitemptyDropsEmptyValue confirms the yaml tag's
// `omitempty` flag is wired correctly: a Field with no Placeholder must not
// emit a `placeholder:` key at all. Without this, every existing manifest
// would gain a noisy empty-string line on round-trip.
func TestFieldPlaceholderOmitemptyDropsEmptyValue(t *testing.T) {
	f := Field{
		ID:    "Name",
		Label: "Name",
		Type:  FieldTypeString,
	}
	raw, err := yaml.Marshal(f)
	if err != nil {
		t.Fatalf("yaml.Marshal: %v", err)
	}
	if strings.Contains(string(raw), "placeholder") {
		t.Errorf("Marshal emitted placeholder for empty value; got:\n%s", raw)
	}
}

// TestLoadAcceptsPlaceholderField is the manifest-loader round-trip: a YAML
// document carrying a `placeholder:` line on a form field must decode under
// strict KnownFields(true) without error and preserve the value end-to-end.
// This is the integration-shaped variant of the unit Marshal/Unmarshal test
// above — it goes through the real Load path, including validate.
func TestLoadAcceptsPlaceholderField(t *testing.T) {
	path := writeTempYAML(t, `
schema_version: packwright.manifest.v1
kind: resource
slash: /x
title: X
template:
  kind: cloudformation
  path: ./x.yaml
deploy:
  driver: script
  script: ./d.sh
form:
  - id: VpcId
    label: VPC
    type: aws/vpc-id
    placeholder: "vpc-0abc1234567890abcdef"
    required: true
`)
	m, err := Load(path)
	if err != nil {
		t.Fatalf("Load(placeholder manifest) error = %v, want nil", err)
	}
	if len(m.Form) != 1 {
		t.Fatalf("len(Form) = %d, want 1", len(m.Form))
	}
	if got, want := m.Form[0].Placeholder, "vpc-0abc1234567890abcdef"; got != want {
		t.Errorf("Form[0].Placeholder = %q, want %q", got, want)
	}
}

// TestLoadAlbFixtureFormFieldsHaveNoPlaceholder pins the backwards-compat
// promise: the pre-PR ALB fixture omits `placeholder:`, and after the schema
// extension every field's Placeholder must still load as the empty string
// (i.e. the new key did not somehow steal value from a sibling field).
func TestLoadAlbFixtureFormFieldsHaveNoPlaceholder(t *testing.T) {
	m, err := Load(filepath.Join("testdata", "alb.yaml"))
	if err != nil {
		t.Fatalf("Load(alb.yaml) error = %v, want nil", err)
	}
	for i, f := range m.Form {
		if f.Placeholder != "" {
			t.Errorf("Form[%d].Placeholder = %q, want empty (fixture defines no placeholders)", i, f.Placeholder)
		}
	}
}
