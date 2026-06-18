package scaffold

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	canonical "github.com/bannaarr01/packwright/internal/manifest"
	"github.com/bannaarr01/packwright/manifest"
)

// minInt returns &v; convenient for the *int fields on FieldSpec without
// littering tests with single-use locals.
func intPtr(v int) *int { return &v }

// TestGenerate_RoundTrip_Resource is the DOD round-trip: Generate(spec) for a
// resource manifest must produce YAML that the canonical (MVP-1 PR-05)
// loader accepts without error.
func TestGenerate_RoundTrip_Resource(t *testing.T) {
	spec := Spec{
		Kind:  manifest.KindResource,
		Slash: "/restart-api",
		Title: "Restart the API service",
		Template: &TemplateSpec{
			Kind:           "cloudformation",
			Path:           "../templates/restart-api.yaml",
			ParametersFile: "../templates/parameters.json",
		},
		Deploy: &DeploySpec{
			Driver: "script",
			Script: "../deploy.sh",
			Env: map[string]string{
				"STACK_NAME":  "restart-{{ .Environment }}",
				"AWS_PROFILE": "{{ .Profile }}",
			},
		},
		Form: []FieldSpec{
			{ID: "Profile", Label: "AWS Profile", Type: manifest.TypeString, Required: true, Default: "default"},
			{ID: "Environment", Label: "Environment", Type: manifest.TypeEnum, Required: true, Values: []string{"dev", "stg", "prd"}},
			{ID: "Replicas", Label: "Desired replicas", Type: manifest.TypeInt, Default: "3", Min: intPtr(1), Max: intPtr(10)},
			{ID: "DryRun", Label: "Dry run", Type: manifest.TypeBool, Default: "false"},
			{ID: "VpcId", Label: "VPC", Type: manifest.TypeAWSVpcID, Required: true},
			{ID: "SubnetIds", Label: "Subnets", Type: manifest.TypeAWSSubnetIDs, Required: true, DependsOn: []string{"VpcId"}, Min: intPtr(2)},
		},
	}

	out, err := Generate(spec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Round-trip: write to disk and load via the canonical validator. The
	// loader takes a path, not bytes, so we materialize a temp file first.
	path := filepath.Join(t.TempDir(), "restart-api.yaml")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("write tempfile: %v", err)
	}
	loaded, err := canonical.Load(path)
	if err != nil {
		t.Fatalf("canonical.Load on generated YAML failed: %v\n---\n%s", err, out)
	}

	// Spot-check the round-tripped manifest reflects the inputs: this guards
	// against template typos that produce valid-but-wrong output (e.g.
	// fields silently dropped).
	if loaded.Kind != canonical.KindResource {
		t.Errorf("Kind = %q, want %q", loaded.Kind, canonical.KindResource)
	}
	if loaded.Slash != spec.Slash {
		t.Errorf("Slash = %q, want %q", loaded.Slash, spec.Slash)
	}
	if loaded.Title != spec.Title {
		t.Errorf("Title = %q, want %q", loaded.Title, spec.Title)
	}
	if loaded.Template == nil || loaded.Template.Kind != "cloudformation" {
		t.Errorf("Template = %+v, want kind=cloudformation", loaded.Template)
	}
	if loaded.Deploy == nil || loaded.Deploy.Driver != "script" {
		t.Errorf("Deploy = %+v, want driver=script", loaded.Deploy)
	}
	if got := loaded.Deploy.Env["STACK_NAME"]; got != "restart-{{ .Environment }}" {
		t.Errorf("Deploy.Env[STACK_NAME] = %q, want template literal preserved", got)
	}
	if len(loaded.Form) != len(spec.Form) {
		t.Fatalf("len(Form) = %d, want %d", len(loaded.Form), len(spec.Form))
	}

	byID := make(map[string]canonical.Field, len(loaded.Form))
	for _, f := range loaded.Form {
		byID[f.ID] = f
	}
	if subnets := byID["SubnetIds"]; subnets.Type != canonical.FieldTypeAWSSubnetID {
		t.Errorf("SubnetIds.Type = %q, want %q", subnets.Type, canonical.FieldTypeAWSSubnetID)
	} else if subnets.Min == nil || *subnets.Min != 2 {
		t.Errorf("SubnetIds.Min = %v, want 2", subnets.Min)
	} else if len(subnets.DependsOn) != 1 || subnets.DependsOn[0] != "VpcId" {
		t.Errorf("SubnetIds.DependsOn = %v, want [VpcId]", subnets.DependsOn)
	}
	if env := byID["Environment"]; env.Type != canonical.FieldTypeEnum {
		t.Errorf("Environment.Type = %q, want enum", env.Type)
	} else if len(env.Values) != 3 {
		t.Errorf("Environment.Values = %v, want 3 entries", env.Values)
	}
	if rep := byID["Replicas"]; rep.Default != 3 {
		// YAML decodes "3" as int, not string — yaml.Marshal'd int.
		t.Errorf("Replicas.Default = %v (%T), want 3 (int)", rep.Default, rep.Default)
	}
	if dry := byID["DryRun"]; dry.Default != false {
		t.Errorf("DryRun.Default = %v (%T), want false (bool)", dry.Default, dry.Default)
	}
}

// TestGenerate_RoundTrip_NonResourceKinds verifies that shell, monitor, and
// composite manifests also pass the canonical validator. These kinds have
// no template/deploy sections; the templates must omit them entirely
// (otherwise validateKindSections rejects the file).
func TestGenerate_RoundTrip_NonResourceKinds(t *testing.T) {
	for _, kind := range []manifest.Kind{manifest.KindShell, manifest.KindMonitor, manifest.KindComposite} {
		t.Run(string(kind), func(t *testing.T) {
			spec := Spec{
				Kind:  kind,
				Slash: "/" + string(kind) + "-cmd",
				Title: "Title for " + string(kind),
				Form: []FieldSpec{
					{ID: "Name", Label: "Name", Type: manifest.TypeString, Required: true},
				},
			}
			out, err := Generate(spec)
			if err != nil {
				t.Fatalf("Generate(%s): %v", kind, err)
			}
			path := filepath.Join(t.TempDir(), "m.yaml")
			if err := os.WriteFile(path, out, 0o600); err != nil {
				t.Fatalf("write tempfile: %v", err)
			}
			m, err := canonical.Load(path)
			if err != nil {
				t.Fatalf("canonical.Load on %s YAML: %v\n---\n%s", kind, err, out)
			}
			if string(m.Kind) != string(kind) {
				t.Errorf("Kind = %q, want %q", m.Kind, kind)
			}
			if m.Template != nil || m.Deploy != nil {
				t.Errorf("%s manifest must not carry template/deploy (got template=%v deploy=%v)", kind, m.Template, m.Deploy)
			}
		})
	}
}

// TestGenerate_EmptyForm exercises the "parameterless command" path: the
// form section must be omitted entirely so the canonical loader does not
// see an empty list (which would still parse but produces noisier output).
func TestGenerate_EmptyForm(t *testing.T) {
	spec := Spec{
		Kind:  manifest.KindShell,
		Slash: "/open-docs",
		Title: "Open AWS docs",
	}
	out, err := Generate(spec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.Contains(string(out), "form:") {
		t.Errorf("output unexpectedly contains form:\n%s", out)
	}
}

// TestGenerate_RejectsMissingSlash documents one of the structural checks
// validateSpec performs. The error chain must surface "Slash" so the
// wizard front-end can highlight the right field.
func TestGenerate_RejectsMissingSlash(t *testing.T) {
	_, err := Generate(Spec{Kind: manifest.KindShell, Title: "X"})
	if err == nil {
		t.Fatal("Generate: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Slash") {
		t.Errorf("error = %v, want it to mention Slash", err)
	}
}

// TestGenerate_RejectsResourceMissingTemplate enforces the kind/section
// coupling — a resource spec without Template must fail upfront rather than
// produce invalid YAML the loader catches later.
func TestGenerate_RejectsResourceMissingTemplate(t *testing.T) {
	_, err := Generate(Spec{
		Kind:   manifest.KindResource,
		Slash:  "/x",
		Title:  "X",
		Deploy: &DeploySpec{Driver: "script"},
	})
	if err == nil || !strings.Contains(err.Error(), "Template") {
		t.Fatalf("Generate: error = %v, want it to mention Template", err)
	}
}

// TestGenerate_RejectsNonResourceWithTemplate is the mirror check: shell
// manifests cannot declare template/deploy.
func TestGenerate_RejectsNonResourceWithTemplate(t *testing.T) {
	_, err := Generate(Spec{
		Kind:     manifest.KindShell,
		Slash:    "/x",
		Title:    "X",
		Template: &TemplateSpec{Kind: "cloudformation", Path: "x.yaml"},
	})
	if err == nil || !strings.Contains(err.Error(), "Template") {
		t.Fatalf("Generate: error = %v, want it to mention Template", err)
	}
}

// TestGenerate_RejectsDuplicateFieldID mirrors the canonical validator's
// duplicate-ID rule so wizard users see the error before YAML generation.
func TestGenerate_RejectsDuplicateFieldID(t *testing.T) {
	_, err := Generate(Spec{
		Kind:  manifest.KindShell,
		Slash: "/x",
		Title: "X",
		Form: []FieldSpec{
			{ID: "A", Type: manifest.TypeString},
			{ID: "A", Type: manifest.TypeString},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("Generate: error = %v, want duplicate-ID complaint", err)
	}
}

// TestGenerate_RejectsEnumWithoutValues catches the most common wizard
// drop-down miss-configuration.
func TestGenerate_RejectsEnumWithoutValues(t *testing.T) {
	_, err := Generate(Spec{
		Kind:  manifest.KindShell,
		Slash: "/x",
		Title: "X",
		Form:  []FieldSpec{{ID: "Env", Type: manifest.TypeEnum}},
	})
	if err == nil || !strings.Contains(err.Error(), "Values") {
		t.Fatalf("Generate: error = %v, want Values complaint", err)
	}
}

// TestGenerate_RejectsBadIntDefault verifies fieldDefault validates type
// coercions before they reach the loader; an int field with a non-numeric
// default would otherwise produce a YAML value the loader's strict typing
// rejects much later in the pipeline.
func TestGenerate_RejectsBadIntDefault(t *testing.T) {
	_, err := Generate(Spec{
		Kind:  manifest.KindShell,
		Slash: "/x",
		Title: "X",
		Form:  []FieldSpec{{ID: "Count", Type: manifest.TypeInt, Default: "abc"}},
	})
	if err == nil || !strings.Contains(err.Error(), "Count") {
		t.Fatalf("Generate: error = %v, want Count complaint", err)
	}
}

// TestGenerate_DeterministicEnvOrder asserts the env map sorts lexically.
// Without this, two consecutive Generate calls on the same spec could
// emit different byte orderings, breaking version-control diffs.
func TestGenerate_DeterministicEnvOrder(t *testing.T) {
	spec := Spec{
		Kind:     manifest.KindResource,
		Slash:    "/x",
		Title:    "X",
		Template: &TemplateSpec{Kind: "cloudformation", Path: "x.yaml"},
		Deploy: &DeploySpec{
			Driver: "script",
			Env: map[string]string{
				"Z_LAST":  "z",
				"A_FIRST": "a",
				"M_MID":   "m",
			},
		},
	}
	var first []byte
	for i := 0; i < 5; i++ {
		out, err := Generate(spec)
		if err != nil {
			t.Fatalf("Generate iter %d: %v", i, err)
		}
		if first == nil {
			first = out
			continue
		}
		if string(out) != string(first) {
			t.Fatalf("Generate output is non-deterministic on iter %d", i)
		}
	}
	aIdx := strings.Index(string(first), "A_FIRST")
	mIdx := strings.Index(string(first), "M_MID")
	zIdx := strings.Index(string(first), "Z_LAST")
	if !(aIdx < mIdx && mIdx < zIdx) {
		t.Errorf("env keys not sorted lexically: A=%d M=%d Z=%d\n%s", aIdx, mIdx, zIdx, first)
	}
}

// TestGenerate_QuotesTitleWithColon checks the YAML escape path: a title
// containing a colon would, unquoted, parse as a flow mapping. The
// yaml.Marshal-backed `q` helper must guard against this.
func TestGenerate_QuotesTitleWithColon(t *testing.T) {
	spec := Spec{
		Kind:  manifest.KindShell,
		Slash: "/x",
		Title: "Restart: API service",
	}
	out, err := Generate(spec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	path := filepath.Join(t.TempDir(), "m.yaml")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := canonical.Load(path)
	if err != nil {
		t.Fatalf("Load on title-with-colon YAML: %v\n%s", err, out)
	}
	if m.Title != "Restart: API service" {
		t.Errorf("Title = %q, want %q", m.Title, "Restart: API service")
	}
}

// TestGenerate_EmitsCommentedPlaceholderForTypedField is the ADR-0051
// scaffolder hook: every typed field with a catalogue entry must ship a
// commented `# placeholder:` line pre-filled with the catalogue default. The
// line is a comment (leading `#`), so strict YAML decoding via canonical.Load
// must still accept the output — the assertion below load-checks the
// generated YAML to confirm.
func TestGenerate_EmitsCommentedPlaceholderForTypedField(t *testing.T) {
	spec := Spec{
		Kind:  manifest.KindResource,
		Slash: "/x",
		Title: "X",
		Template: &TemplateSpec{
			Kind: "cloudformation",
			Path: "x.yaml",
		},
		Deploy: &DeploySpec{Driver: "script", Script: "d.sh"},
		Form: []FieldSpec{
			{ID: "VpcId", Label: "VPC", Type: manifest.TypeAWSVpcID, Required: true},
			// Generic string has an empty catalogue entry; no comment line.
			{ID: "Name", Label: "Name", Type: manifest.TypeString},
		},
	}
	out, err := Generate(spec)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	body := string(out)
	if !strings.Contains(body, `# placeholder: vpc-0abc1234567890abcdef`) {
		t.Errorf("Generate output missing commented placeholder for aws/vpc-id:\n%s", body)
	}
	// The generic string field's catalogue entry is intentionally empty
	// (ADR-0051 §"over-hinting"), so no comment line should appear under
	// the `Name` field.
	nameIdx := strings.Index(body, "id: Name")
	if nameIdx < 0 {
		t.Fatalf("Generate output missing Name field:\n%s", body)
	}
	after := body[nameIdx:]
	if strings.HasPrefix(strings.TrimSpace(strings.SplitN(after, "\n", 4)[3]), "# placeholder") {
		t.Errorf("Generate emitted a placeholder comment for an empty catalogue type:\n%s", body)
	}

	// Strict YAML round-trip: the comment must not be parsed as a key.
	path := filepath.Join(t.TempDir(), "x.yaml")
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("write tempfile: %v", err)
	}
	loaded, err := canonical.Load(path)
	if err != nil {
		t.Fatalf("canonical.Load on scaffolded YAML failed: %v\n---\n%s", err, out)
	}
	for _, f := range loaded.Form {
		if f.Placeholder != "" {
			t.Errorf("Form[%s].Placeholder = %q, want empty (the line is a comment)", f.ID, f.Placeholder)
		}
	}
}

// TestGenerate_UnknownKindIsTypedError documents the failure mode when a
// caller invents a new kind: the error must surface the bad value rather
// than panic on a nil template lookup.
func TestGenerate_UnknownKindIsTypedError(t *testing.T) {
	_, err := Generate(Spec{Kind: manifest.Kind("bogus"), Slash: "/x", Title: "X"})
	if err == nil {
		t.Fatal("Generate: expected error, got nil")
	}
	if !errors.Is(err, err) || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error = %v, want it to mention the unknown kind", err)
	}
}
