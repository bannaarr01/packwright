package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/internal/delete"
)

// writeRecord persists a stackRecordDoc as JSON for the cmd to load.
// Returns the on-disk path.
func writeRecord(t *testing.T, doc stackRecordDoc) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "record.json")
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestLoadStackRecord(t *testing.T) {
	path := writeRecord(t, stackRecordDoc{
		StackName:    "x",
		TemplatePath: "tpl.yaml",
		ManifestPath: "mf.yaml",
		Resources: []stackResourceDoc{
			{LogicalID: "A", PhysicalID: "p-a", Type: "AWS::S3::Bucket"},
			{LogicalID: "Wait", Meta: true},
		},
	})
	rec, err := loadStackRecord(path)
	if err != nil {
		t.Fatalf("loadStackRecord: %v", err)
	}
	if rec.StackName != "x" || rec.TemplatePath != "tpl.yaml" {
		t.Errorf("rec = %+v", rec)
	}
	if len(rec.Resources) != 2 {
		t.Fatalf("Resources = %d, want 2", len(rec.Resources))
	}
	if !rec.Resources[1].Meta {
		t.Errorf("second resource should be Meta=true")
	}
}

func TestLoadStackRecordMissingPath(t *testing.T) {
	if _, err := loadStackRecord(""); err == nil {
		t.Fatalf("expected error for empty path")
	}
}

// TestDeleteResourceDryRunReportsPlan exercises the full command-flow
// happy path without invoking AWS: --dry-run prints the resolver
// verdict and exits cleanly.
func TestDeleteResourceDryRunReportsPlan(t *testing.T) {
	dir := t.TempDir()
	templatePath := filepath.Join(dir, "t.yaml")
	if err := os.WriteFile(templatePath, []byte("Resources:\n  X:\n    Type: AWS::S3::Bucket\n  Y:\n    Type: AWS::S3::Bucket\n"), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	recPath := writeRecord(t, stackRecordDoc{
		StackName:    "demo",
		TemplatePath: templatePath,
		Resources: []stackResourceDoc{
			{LogicalID: "X", Type: "AWS::S3::Bucket"},
			{LogicalID: "Y", Type: "AWS::S3::Bucket"},
		},
	})

	// Reset flag state across test boundaries.
	deleteResourceOpts = deleteResourceFlags{}
	deleteResourceOpts.stackRecordPath = recPath
	deleteResourceOpts.logicalID = "X"
	deleteResourceOpts.dryRun = true
	deleteResourceOpts.force = false

	cmd := deleteResourceCmd
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetContext(context.Background())

	if err := runDeleteResource(cmd, nil); err != nil {
		t.Fatalf("runDeleteResource: %v", err)
	}
	if !strings.Contains(out.String(), "Resolved:   template-shrink") {
		t.Errorf("output missing resolver verdict:\n%s", out.String())
	}
}

func TestDeleteResourceLastResourceRefusesWithoutMode(t *testing.T) {
	dir := t.TempDir()
	templatePath := filepath.Join(dir, "t.yaml")
	if err := os.WriteFile(templatePath, []byte("Resources:\n  Only:\n    Type: AWS::S3::Bucket\n"), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	recPath := writeRecord(t, stackRecordDoc{
		StackName:    "tiny",
		TemplatePath: templatePath,
		Resources: []stackResourceDoc{
			{LogicalID: "Only", Type: "AWS::S3::Bucket"},
		},
	})
	deleteResourceOpts = deleteResourceFlags{
		stackRecordPath: recPath,
		logicalID:       "Only",
	}
	cmd := deleteResourceCmd
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetContext(context.Background())

	err := runDeleteResource(cmd, nil)
	if err == nil {
		t.Fatalf("expected refusal when --mode unset on last-resource prompt")
	}
	if !strings.Contains(err.Error(), "last-resource prompt") {
		t.Errorf("error should mention last-resource prompt: %v", err)
	}
}

func TestDeleteResourceStackDeleteRequiresAdapter(t *testing.T) {
	dir := t.TempDir()
	templatePath := filepath.Join(dir, "t.yaml")
	if err := os.WriteFile(templatePath, []byte("Resources:\n  Only:\n    Type: AWS::S3::Bucket\n"), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	recPath := writeRecord(t, stackRecordDoc{
		StackName:    "tiny",
		TemplatePath: templatePath,
		Resources: []stackResourceDoc{
			{LogicalID: "Only", Type: "AWS::S3::Bucket"},
		},
	})
	deleteResourceOpts = deleteResourceFlags{
		stackRecordPath: recPath,
		logicalID:       "Only",
		mode:            string(delete.ModeStackDelete),
	}
	cmd := deleteResourceCmd
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(out)
	cmd.SetContext(context.Background())

	err := runDeleteResource(cmd, nil)
	if err == nil {
		t.Fatalf("expected adapter-missing error")
	}
	if !strings.Contains(err.Error(), "CFN client adapter") {
		t.Errorf("error should mention CFN adapter: %v", err)
	}
}
