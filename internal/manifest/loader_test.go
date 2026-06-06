package manifest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAlbFixtureSucceeds(t *testing.T) {
	m, err := Load(filepath.Join("testdata", "alb.yaml"))
	if err != nil {
		t.Fatalf("Load(alb.yaml) error = %v, want nil", err)
	}

	if m.SchemaVersion != SchemaVersionV1 {
		t.Errorf("SchemaVersion = %q, want %q", m.SchemaVersion, SchemaVersionV1)
	}
	if m.Kind != KindResource {
		t.Errorf("Kind = %q, want %q", m.Kind, KindResource)
	}
	if m.Slash != "/alb" {
		t.Errorf("Slash = %q, want %q", m.Slash, "/alb")
	}
	if m.Title == "" {
		t.Error("Title is empty, want non-empty")
	}

	if m.Template == nil {
		t.Fatal("Template = nil, want populated TemplateSpec")
	}
	if m.Template.Kind != "cloudformation" {
		t.Errorf("Template.Kind = %q, want %q", m.Template.Kind, "cloudformation")
	}
	if m.Deploy == nil {
		t.Fatal("Deploy = nil, want populated DeploySpec")
	}
	if m.Deploy.Driver != "script" {
		t.Errorf("Deploy.Driver = %q, want %q", m.Deploy.Driver, "script")
	}
	if got := m.Deploy.Env["STACK_NAME"]; !strings.Contains(got, "{{ .Project }}") {
		t.Errorf("Deploy.Env[STACK_NAME] = %q, want it to preserve template syntax", got)
	}

	if len(m.Form) < 4 {
		t.Fatalf("len(Form) = %d, want at least 4 fields", len(m.Form))
	}

	byID := make(map[string]Field, len(m.Form))
	for _, f := range m.Form {
		byID[f.ID] = f
	}

	subnets, ok := byID["SubnetIds"]
	if !ok {
		t.Fatal("missing SubnetIds field in form")
	}
	if subnets.Type != FieldTypeAWSSubnetID {
		t.Errorf("SubnetIds.Type = %q, want %q", subnets.Type, FieldTypeAWSSubnetID)
	}
	if len(subnets.DependsOn) != 1 || subnets.DependsOn[0] != "VpcId" {
		t.Errorf("SubnetIds.DependsOn = %v, want [VpcId]", subnets.DependsOn)
	}
	if subnets.Min == nil || *subnets.Min != 2 {
		t.Errorf("SubnetIds.Min = %v, want 2", subnets.Min)
	}
	if len(subnets.Validate) != 1 || subnets.Validate[0].Rule != "distinct-az" {
		t.Errorf("SubnetIds.Validate = %v, want one rule distinct-az", subnets.Validate)
	}

	env, ok := byID["Environment"]
	if !ok {
		t.Fatal("missing Environment field in form")
	}
	if env.Type != FieldTypeEnum {
		t.Errorf("Environment.Type = %q, want %q", env.Type, FieldTypeEnum)
	}
	if len(env.Values) == 0 {
		t.Error("Environment.Values is empty, want enum values")
	}

	if err := CanRun(m); err != nil {
		t.Errorf("CanRun(loaded alb manifest) error = %v, want nil", err)
	}
}

func TestLoadInvalidFixtureFails(t *testing.T) {
	_, err := Load(filepath.Join("testdata", "invalid.yaml"))
	if err == nil {
		t.Fatal("Load(invalid.yaml) error = nil, want a structured error")
	}
	// The fixture has both an unknown top-level key ("observe") and a missing
	// template section; the unknown-key check fires first in the YAML decoder
	// before Validate runs, so we only assert the failure mode at this level.
	if !strings.Contains(err.Error(), "manifest:") {
		t.Errorf("error = %v, want it to be wrapped with manifest: prefix", err)
	}
}

func TestLoadRejectsUnknownYAMLKey(t *testing.T) {
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
unexpected_key: nope
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load with unknown key error = nil, want decoder error")
	}
	if !strings.Contains(err.Error(), "unexpected_key") {
		t.Fatalf("error = %v, want it to name the unexpected key", err)
	}
}

func TestLoadRejectsBadSchemaVersion(t *testing.T) {
	path := writeTempYAML(t, `
schema_version: packwright.manifest.v9
kind: resource
slash: /x
title: X
template:
  kind: cloudformation
  path: ./x.yaml
deploy:
  driver: script
  script: ./d.sh
`)
	assertValidationPath(t, path, "schema_version")
}

func TestLoadRejectsResourceMissingTemplate(t *testing.T) {
	path := writeTempYAML(t, `
schema_version: packwright.manifest.v1
kind: resource
slash: /x
title: X
deploy:
  driver: script
  script: ./d.sh
`)
	assertValidationPath(t, path, "template")
}

func TestLoadRejectsNonResourceWithTemplate(t *testing.T) {
	path := writeTempYAML(t, `
schema_version: packwright.manifest.v1
kind: shell
slash: /x
title: X
template:
  kind: cloudformation
  path: ./x.yaml
`)
	assertValidationPath(t, path, "template")
}

func TestLoadRejectsShellManifestAtRuntime(t *testing.T) {
	// A shell manifest with no resource sections should *parse* cleanly. The
	// runtime gate is CanRun, not Load.
	path := writeTempYAML(t, `
schema_version: packwright.manifest.v1
kind: shell
slash: /open-docs
title: Open AWS docs
`)
	m, err := Load(path)
	if err != nil {
		t.Fatalf("Load(shell-only manifest) error = %v, want nil", err)
	}
	if err := CanRun(m); err == nil {
		t.Fatal("CanRun(shell) error = nil, want unsupported-kind error")
	}
}

func TestLoadRejectsBadFieldType(t *testing.T) {
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
  - id: Name
    label: Name
    type: bogus-type
`)
	assertValidationPath(t, path, "form[0].type")
}

func TestLoadRejectsEnumWithoutValues(t *testing.T) {
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
  - id: Env
    label: Env
    type: enum
`)
	assertValidationPath(t, path, "form[0].values")
}

func TestLoadRejectsDuplicateFieldID(t *testing.T) {
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
  - id: Name
    label: One
    type: string
  - id: Name
    label: Two
    type: string
`)
	assertValidationPath(t, path, "form[1].id")
}

func TestLoadRejectsDependsOnUnknownField(t *testing.T) {
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
  - id: A
    label: A
    type: string
    depends_on: [Nope]
`)
	assertValidationPath(t, path, "form[0].depends_on[0]")
}

func TestLoadRejectsSelfDependency(t *testing.T) {
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
  - id: A
    label: A
    type: string
    depends_on: [A]
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load(self-dependency) error = nil, want validation error")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	if ve.Path != "form[0].depends_on[0]" {
		t.Errorf("ValidationError.Path = %q, want %q", ve.Path, "form[0].depends_on[0]")
	}
	if !strings.Contains(ve.Reason, "cannot depend on itself") {
		t.Errorf("ValidationError.Reason = %q, want it to mention self-dependency", ve.Reason)
	}
}

func TestLoadAcceptsForwardDependsOn(t *testing.T) {
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
  - id: Subnets
    label: Subnets
    type: aws/subnet-ids
    depends_on: [VpcId]
  - id: VpcId
    label: VPC
    type: aws/vpc-id
`)
	if _, err := Load(path); err != nil {
		t.Fatalf("Load(forward depends_on) error = %v, want nil", err)
	}
}

func TestLoadCapturesValidatorInlineParams(t *testing.T) {
	// Confirms ValidatorSpec uses yaml:",inline" correctly: keys on the same
	// node as `rule` and `message` land in Params instead of triggering
	// KnownFields(true)'s unknown-key error.
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
  - id: Name
    label: Name
    type: string
    validate:
      - rule: regex
        message: must be lowercase
        pattern: "^[a-z]+$"
        flags: i
`)
	m, err := Load(path)
	if err != nil {
		t.Fatalf("Load(validator with inline params) error = %v, want nil", err)
	}
	if len(m.Form) != 1 || len(m.Form[0].Validate) != 1 {
		t.Fatalf("unexpected form/validate shape: %+v", m.Form)
	}
	v := m.Form[0].Validate[0]
	if v.Rule != "regex" {
		t.Errorf("Rule = %q, want %q", v.Rule, "regex")
	}
	if v.Message != "must be lowercase" {
		t.Errorf("Message = %q, want %q", v.Message, "must be lowercase")
	}
	if got, want := v.Params["pattern"], "^[a-z]+$"; got != want {
		t.Errorf("Params[pattern] = %v, want %v", got, want)
	}
	if got, want := v.Params["flags"], "i"; got != want {
		t.Errorf("Params[flags] = %v, want %v", got, want)
	}
}

func TestLoadRejectsMultipleYAMLDocuments(t *testing.T) {
	// A manifest file describes exactly one action; the second document must
	// be flagged rather than silently dropped.
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
---
schema_version: packwright.manifest.v1
kind: resource
slash: /y
title: Y
template:
  kind: cloudformation
  path: ./y.yaml
deploy:
  driver: script
  script: ./d.sh
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load(multi-doc) error = nil, want multi-document error")
	}
	if !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("error = %v, want it to mention multiple YAML documents", err)
	}
}

func TestLoadFileNotFoundReturnsWrappedFSError(t *testing.T) {
	_, err := Load(filepath.Join("testdata", "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("Load(missing) error = nil, want wrapped fs error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(missing) error = %v, want errors.Is(err, os.ErrNotExist)", err)
	}
}

// writeTempYAML writes body to a temp file and returns its path. The file is
// cleaned up automatically when the test finishes.
func writeTempYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writeTempYAML: %v", err)
	}
	return path
}

// assertValidationPath loads path and asserts the error chain unwraps to a
// *ValidationError whose Path equals wantPath. Used for the many "load this
// bad manifest and check the field that failed" cases.
func assertValidationPath(t *testing.T, path, wantPath string) {
	t.Helper()
	_, err := Load(path)
	if err == nil {
		t.Fatalf("Load(%s) error = nil, want validation error at %q", path, wantPath)
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("Load(%s) error = %v, want *ValidationError", path, err)
	}
	if ve.Path != wantPath {
		t.Errorf("ValidationError.Path = %q, want %q", ve.Path, wantPath)
	}
}
