package delete

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	auditdelete "github.com/bannaarr01/packwright/internal/audit/delete"
)

// s3BucketTemplate is the canonical adopt-and-delete fixture. The
// stack holds one resource (the S3 bucket), so adopt-and-delete is
// the natural "last resource" alternative branch.
const s3BucketTemplate = `AWSTemplateFormatVersion: '2010-09-09'
Description: Single-resource bucket fixture

Resources:
  # The bucket we will adopt
  MyBucket:
    Type: AWS::S3::Bucket
    Properties:
      BucketName: pw-test-bucket-1234
`

func TestAdoptTemplateAddsDeletionPolicy(t *testing.T) {
	path := writeFixture(t, s3BucketTemplate)
	rec := StackRecord{
		StackName:    "bucket-stack",
		TemplatePath: path,
		ManifestPath: "manifests/bucket.manifest.yaml",
		Resources: []Resource{
			{LogicalID: "MyBucket", PhysicalID: "pw-test-bucket-1234", Type: "AWS::S3::Bucket"},
		},
	}
	res, err := AdoptTemplate(rec, "MyBucket", AdoptOptions{Now: time.Unix(1700000000, 0)})
	if err != nil {
		t.Fatalf("AdoptTemplate: %v", err)
	}
	out, _ := os.ReadFile(res.ShrunkPath)
	if !strings.Contains(string(out), "DeletionPolicy: Retain") {
		t.Errorf("DeletionPolicy: Retain missing from output:\n%s", string(out))
	}
	// Body should still mention MyBucket — adopt does NOT remove it.
	if !strings.Contains(string(out), "MyBucket") {
		t.Errorf("MyBucket missing after adopt; want resource preserved with Retain policy\n%s", string(out))
	}
	if res.UpdateRan {
		t.Errorf("UpdateRan = true for AdoptTemplate (should only be true for Adopt)")
	}
}

func TestAdoptTemplateProducesBridgeRequest(t *testing.T) {
	path := writeFixture(t, s3BucketTemplate)
	rec := StackRecord{
		StackName:    "bucket-stack",
		TemplatePath: path,
		Resources: []Resource{
			{LogicalID: "MyBucket", PhysicalID: "pw-test-bucket-1234", Type: "AWS::S3::Bucket"},
		},
	}
	res, err := AdoptTemplate(rec, "MyBucket", AdoptOptions{Now: time.Unix(1, 0)})
	if err != nil {
		t.Fatalf("AdoptTemplate: %v", err)
	}
	if len(res.Request.Items) != 1 {
		t.Fatalf("Request.Items = %d, want 1", len(res.Request.Items))
	}
	item := res.Request.Items[0]
	// DOD: kind=s3/bucket, physical_id=...
	if item.Kind != "s3/bucket" {
		t.Errorf("Kind = %q, want %q", item.Kind, "s3/bucket")
	}
	if item.PhysicalID != "pw-test-bucket-1234" {
		t.Errorf("PhysicalID = %q", item.PhysicalID)
	}
	if item.Source.OriginatingFlow != auditdelete.FlowAdoptAndDelete {
		t.Errorf("OriginatingFlow = %q", item.Source.OriginatingFlow)
	}
	if item.Source.StackName != "bucket-stack" {
		t.Errorf("Source.StackName = %q", item.Source.StackName)
	}
	if item.Source.LogicalID != "MyBucket" {
		t.Errorf("Source.LogicalID = %q", item.Source.LogicalID)
	}
}

func TestAdoptRunsUpdateRunner(t *testing.T) {
	path := writeFixture(t, s3BucketTemplate)
	rec := StackRecord{
		StackName:    "bucket-stack",
		TemplatePath: path,
		Resources: []Resource{
			{LogicalID: "MyBucket", PhysicalID: "b1", Type: "AWS::S3::Bucket"},
		},
	}
	var got UpdateRequest
	prev := SetUpdateRunner(func(_ context.Context, req UpdateRequest) error {
		got = req
		return nil
	})
	t.Cleanup(func() { SetUpdateRunner(prev) })

	res, err := Adopt(context.Background(), rec, "MyBucket", AdoptOptions{Now: time.Unix(1, 0)})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if !res.UpdateRan {
		t.Errorf("UpdateRan = false, want true after Adopt success")
	}
	if got.TemplatePath != res.ShrunkPath {
		t.Errorf("update.TemplatePath = %q, want %q", got.TemplatePath, res.ShrunkPath)
	}
	if !strings.Contains(got.Reason, "MyBucket") {
		t.Errorf("update.Reason = %q (want mention of MyBucket)", got.Reason)
	}
}

func TestAdoptTemplateUpdatesExistingPolicy(t *testing.T) {
	tmpl := `Resources:
  Bucket:
    Type: AWS::S3::Bucket
    DeletionPolicy: Delete
    Properties:
      BucketName: x
`
	path := writeFixture(t, tmpl)
	rec := StackRecord{
		TemplatePath: path,
		Resources:    []Resource{{LogicalID: "Bucket", Type: "AWS::S3::Bucket"}},
	}
	res, err := AdoptTemplate(rec, "Bucket", AdoptOptions{Now: time.Unix(1, 0)})
	if err != nil {
		t.Fatalf("AdoptTemplate: %v", err)
	}
	out, _ := os.ReadFile(res.ShrunkPath)
	if !strings.Contains(string(out), "DeletionPolicy: Retain") {
		t.Errorf("DeletionPolicy: Retain missing\n%s", string(out))
	}
	if strings.Contains(string(out), "DeletionPolicy: Delete") {
		t.Errorf("old DeletionPolicy still present\n%s", string(out))
	}
}

func TestKindFromCFNTypeMapping(t *testing.T) {
	cases := map[string]string{
		"AWS::S3::Bucket":                          "s3/bucket",
		"AWS::EC2::Volume":                         "ec2/volume",
		"AWS::ElasticLoadBalancingV2::TargetGroup": "elbv2/target-group",
		"AWS::Logs::LogGroup":                      "logs/log-group",
		"AWS::CloudFormation::Stack":               "cfn/stack",
		"AWS::Made::UpResource":                    "made/up-resource",
	}
	for in, want := range cases {
		if got := auditdelete.KindFromCFNType(in); got != want {
			t.Errorf("KindFromCFNType(%q) = %q, want %q", in, got, want)
		}
	}
}
