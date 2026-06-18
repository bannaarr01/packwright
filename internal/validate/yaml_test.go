package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestYAMLStage_AcceptsValidTemplate(t *testing.T) {
	tpl := writeTemp(t, "ok.yaml", `AWSTemplateFormatVersion: '2010-09-09'
Resources:
  Bucket:
    Type: AWS::S3::Bucket
    Properties:
      BucketName: example
`)
	findings, err := runYAMLStage(Input{TemplatePath: tpl})
	if err != nil {
		t.Fatalf("runYAMLStage err = %v, want nil", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want empty", findings)
	}
}

func TestYAMLStage_RejectsMixedTabsAndSpaces(t *testing.T) {
	// Line 4 leads with a space then a tab: "Resources:\n  Bucket:\n    Type:..."
	// Hand-craft a line that mixes a space with a tab in its leading whitespace.
	body := "AWSTemplateFormatVersion: '2010-09-09'\n" +
		"Resources:\n" +
		"  Bucket:\n" +
		" \tType: AWS::S3::Bucket\n"
	tpl := writeTemp(t, "mixed.yaml", body)

	findings, err := runYAMLStage(Input{TemplatePath: tpl})
	if err != nil {
		t.Fatalf("runYAMLStage err = %v", err)
	}
	if len(findings) == 0 {
		t.Fatalf("findings empty; want at least one tab/space mix finding")
	}

	var got *Finding
	for i := range findings {
		if strings.Contains(findings[i].Reason, "mixes tabs and spaces") {
			got = &findings[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("no tab/space finding; got %+v", findings)
	}
	if got.Line != 4 {
		t.Errorf("Line = %d, want 4", got.Line)
	}
	if got.Col != 2 {
		t.Errorf("Col = %d, want 2 (column of the tab)", got.Col)
	}
	if got.Severity != SeverityError {
		t.Errorf("Severity = %q, want %q", got.Severity, SeverityError)
	}
	if got.Stage != StageYAML {
		t.Errorf("Stage = %q, want %q", got.Stage, StageYAML)
	}
}

func TestYAMLStage_RejectsDuplicateKeys(t *testing.T) {
	body := `AWSTemplateFormatVersion: '2010-09-09'
Resources:
  Bucket:
    Type: AWS::S3::Bucket
    Properties:
      BucketName: a
      BucketName: b
`
	tpl := writeTemp(t, "dup.yaml", body)

	findings, err := runYAMLStage(Input{TemplatePath: tpl})
	if err != nil {
		t.Fatalf("runYAMLStage err = %v", err)
	}

	var dup *Finding
	for i := range findings {
		if strings.Contains(findings[i].Reason, "duplicate key") {
			dup = &findings[i]
			break
		}
	}
	if dup == nil {
		t.Fatalf("duplicate-key finding missing; got %+v", findings)
	}
	if dup.Line != 7 {
		t.Errorf("Line = %d, want 7", dup.Line)
	}
	if !strings.Contains(dup.Reason, `"BucketName"`) {
		t.Errorf("Reason = %q; want it to name BucketName", dup.Reason)
	}
}

func TestYAMLStage_SkipsMissingParametersFile(t *testing.T) {
	tpl := writeTemp(t, "ok.yaml", "Resources: {}\n")
	findings, err := runYAMLStage(Input{
		TemplatePath:   tpl,
		ParametersPath: filepath.Join(t.TempDir(), "not-yet-generated.json"),
	})
	if err != nil {
		t.Fatalf("runYAMLStage err = %v, want nil (missing parameters file should be silent)", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want empty", findings)
	}
}

func TestYAMLStage_LintsExistingParametersFile(t *testing.T) {
	tpl := writeTemp(t, "ok.yaml", "Resources: {}\n")
	dir := filepath.Dir(tpl)
	paramsPath := filepath.Join(dir, "parameters.json")
	// JSON is a subset of YAML; this content parses cleanly so we just
	// check that the lint runs without producing a finding.
	if err := os.WriteFile(paramsPath, []byte(`{"VpcId":"vpc-1"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write params: %v", err)
	}

	findings, err := runYAMLStage(Input{TemplatePath: tpl, ParametersPath: paramsPath})
	if err != nil {
		t.Fatalf("runYAMLStage err = %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want empty for a valid params file", findings)
	}
}

func TestYAMLStage_TemplateMustExist(t *testing.T) {
	_, err := runYAMLStage(Input{TemplatePath: filepath.Join(t.TempDir(), "nope.yaml")})
	if err == nil {
		t.Fatal("expected error for missing template, got nil")
	}
}
