package manifest

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateScalingResolvesParamToFormID(t *testing.T) {
	m := &Manifest{
		SchemaVersion: SchemaVersionV1,
		Kind:          KindResource,
		Slash:         "/x",
		Title:         "X",
		Template:      &TemplateSpec{Kind: "cloudformation", Path: "./x.yaml"},
		Deploy:        &DeploySpec{Driver: "script", Script: "./d.sh"},
		Form: []Field{
			{ID: "DesiredCount", Label: "Tasks", Type: FieldTypeInt},
		},
		Scaling: []ScalingSpec{
			{Param: "DesiredCount", Kind: "integer"},
		},
	}
	if err := Validate(m); err != nil {
		t.Fatalf("Validate(matching scaling.param) err = %v, want nil", err)
	}
}

func TestValidateScalingRejectsUnknownParam(t *testing.T) {
	m := &Manifest{
		SchemaVersion: SchemaVersionV1,
		Kind:          KindResource,
		Slash:         "/x",
		Title:         "X",
		Template:      &TemplateSpec{Kind: "cloudformation", Path: "./x.yaml"},
		Deploy:        &DeploySpec{Driver: "script", Script: "./d.sh"},
		Form: []Field{
			{ID: "DesiredCount", Label: "Tasks", Type: FieldTypeInt},
		},
		Scaling: []ScalingSpec{
			{Param: "NotInForm", Kind: "integer"},
		},
	}
	err := Validate(m)
	if err == nil {
		t.Fatal("Validate(scaling refs missing field) err = nil, want ValidationError")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want *ValidationError", err)
	}
	if ve.Path != "scaling[0].param" {
		t.Errorf("ValidationError.Path = %q, want %q", ve.Path, "scaling[0].param")
	}
	if !strings.Contains(ve.Reason, "NotInForm") {
		t.Errorf("ValidationError.Reason = %q, want it to name the missing param", ve.Reason)
	}
}

func TestValidateScalingEmptyParamRejected(t *testing.T) {
	m := &Manifest{
		SchemaVersion: SchemaVersionV1,
		Kind:          KindResource,
		Slash:         "/x",
		Title:         "X",
		Template:      &TemplateSpec{Kind: "cloudformation", Path: "./x.yaml"},
		Deploy:        &DeploySpec{Driver: "script", Script: "./d.sh"},
		Form:          []Field{{ID: "A", Type: FieldTypeString}},
		Scaling:       []ScalingSpec{{Kind: "integer"}},
	}
	err := Validate(m)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want *ValidationError", err)
	}
	if ve.Path != "scaling[0].param" {
		t.Errorf("Path = %q, want scaling[0].param", ve.Path)
	}
}

func TestValidateScalingEmptyBlockIsAccepted(t *testing.T) {
	// A manifest without a scaling block must still validate — the block
	// is optional, the /scale action just won't be offered for stacks
	// deployed from it.
	m := &Manifest{
		SchemaVersion: SchemaVersionV1,
		Kind:          KindResource,
		Slash:         "/x",
		Title:         "X",
		Template:      &TemplateSpec{Kind: "cloudformation", Path: "./x.yaml"},
		Deploy:        &DeploySpec{Driver: "script", Script: "./d.sh"},
		Form:          []Field{{ID: "A", Type: FieldTypeString}},
	}
	if err := Validate(m); err != nil {
		t.Errorf("Validate(no scaling) err = %v, want nil", err)
	}
}

func TestLoadParsesScalingBlockWithEnvGuards(t *testing.T) {
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
  - id: DesiredCount
    label: Tasks
    type: int
  - id: TaskCpu
    label: CPU
    type: enum
    values: ["256","512"]
scaling:
  - param: DesiredCount
    label: Desired tasks
    kind: integer
    min: 1
    max: 50
    step: 1
    env_guards:
      prd: { min: 2, max: 20, require_confirmation: true }
      dev: { min: 0, max: 50 }
  - param: TaskCpu
    label: Task CPU
    kind: enum
    values: ["256","512"]
`)
	m, err := Load(path)
	if err != nil {
		t.Fatalf("Load(scaling fixture) err = %v, want nil", err)
	}
	if got, want := len(m.Scaling), 2; got != want {
		t.Fatalf("len(Scaling) = %d, want %d", got, want)
	}
	desired := m.Scaling[0]
	if desired.Param != "DesiredCount" || desired.Kind != "integer" {
		t.Errorf("Scaling[0] = %+v, want DesiredCount/integer", desired)
	}
	if desired.Min == nil || *desired.Min != 1 || desired.Max == nil || *desired.Max != 50 {
		t.Errorf("Scaling[0] Min/Max = %v/%v, want 1/50", desired.Min, desired.Max)
	}
	prd, ok := desired.EnvGuards["prd"]
	if !ok {
		t.Fatal("Scaling[0].EnvGuards[prd] missing")
	}
	if prd.Max == nil || *prd.Max != 20 || !prd.RequireConfirmation {
		t.Errorf("prd guard = %+v, want max=20 require_confirmation=true", prd)
	}
	cpu := m.Scaling[1]
	if cpu.Kind != "enum" || len(cpu.Values) != 2 {
		t.Errorf("Scaling[1] = %+v, want enum with 2 values", cpu)
	}
}

func TestLoadRejectsScalingParamMissingFromForm(t *testing.T) {
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
scaling:
  - param: B
    kind: integer
`)
	assertValidationPath(t, path, "scaling[0].param")
}
