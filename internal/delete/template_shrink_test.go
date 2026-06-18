package delete

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fiveResourceTemplate is the canonical YAML fixture used by the DOD
// "stack with 5 resources" scenario. It mixes !Ref and Fn::GetAtt
// references so the dangling-ref scanner has both forms to exercise.
const fiveResourceTemplate = `AWSTemplateFormatVersion: '2010-09-09'
Description: Five-resource ALB fixture

Resources:
  # ALB load balancer
  ALB:
    Type: AWS::ElasticLoadBalancingV2::LoadBalancer
    Properties:
      Name: my-alb

  # Listener depends on the ALB
  Listener:
    Type: AWS::ElasticLoadBalancingV2::Listener
    Properties:
      LoadBalancerArn: !Ref ALB

  # Logs bucket - not referenced anywhere
  Bucket:
    Type: AWS::S3::Bucket
    Properties:
      BucketName: my-bucket

  Logs:
    Type: AWS::Logs::LogGroup
    Properties:
      LogGroupName: /aws/alb/my-alb

  # Target group - the row we will delete in the happy-path test
  MyTargetGroup:
    Type: AWS::ElasticLoadBalancingV2::TargetGroup
    DependsOn: ALB
    Properties:
      Name: my-tg
      VpcId: vpc-1234
`

// writeFixture lays out a temp template path the shrink can edit.
func writeFixture(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "template.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestShrinkTemplateHappyPath(t *testing.T) {
	path := writeFixture(t, fiveResourceTemplate)
	rec := fixtureFive(path)

	res, err := ShrinkTemplate(rec, "MyTargetGroup", ShrinkOptions{
		Now: time.Unix(1700000000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("ShrinkTemplate: %v", err)
	}
	if res.ShrunkPath == "" {
		t.Fatalf("ShrunkPath empty")
	}
	if !strings.Contains(res.ShrunkPath, "shrunk-1700000000") {
		t.Errorf("ShrunkPath = %q, want timestamp segment", res.ShrunkPath)
	}
	// Original moved to .prev — both files exist on disk.
	if _, err := os.Stat(res.ShrunkPath); err != nil {
		t.Errorf("shrunk file missing: %v", err)
	}
	if _, err := os.Stat(res.PrevPath); err != nil {
		t.Errorf("prev file missing: %v", err)
	}
	// The original path is now gone (renamed to .prev).
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("original template should be renamed away, stat err = %v", err)
	}
	// Verify content: MyTargetGroup gone, ALB / Listener / Bucket / Logs intact.
	shrunk, _ := os.ReadFile(res.ShrunkPath)
	if strings.Contains(string(shrunk), "MyTargetGroup") {
		t.Errorf("shrunk template still contains MyTargetGroup:\n%s", string(shrunk))
	}
	for _, want := range []string{"ALB:", "Listener:", "Bucket:", "Logs:"} {
		if !strings.Contains(string(shrunk), want) {
			t.Errorf("shrunk template missing %q", want)
		}
	}
	// DependsOn on MyTargetGroup -> ALB no longer needs purging (the
	// resource is gone) but the helper still counts neighbour edits.
	// DependsOn purge count: the removed resource's own DependsOn
	// doesn't count; only neighbour DependsOn lists are edited.
	if res.RemovedDependsOnEdits != 0 {
		t.Errorf("RemovedDependsOnEdits = %d, want 0 (no neighbour DependsOn referenced MyTargetGroup)", res.RemovedDependsOnEdits)
	}
}

func TestShrinkTemplatePreservesNeighbourComments(t *testing.T) {
	path := writeFixture(t, fiveResourceTemplate)
	rec := fixtureFive(path)
	res, err := ShrinkTemplate(rec, "MyTargetGroup", ShrinkOptions{Now: time.Unix(1, 0)})
	if err != nil {
		t.Fatalf("ShrinkTemplate: %v", err)
	}
	out, _ := os.ReadFile(res.ShrunkPath)
	// Comments on surviving keys (ALB, Listener, Bucket) should still
	// be present. yaml.v3 attaches head comments to the key node.
	for _, want := range []string{"# ALB load balancer", "# Listener depends on the ALB", "# Logs bucket - not referenced anywhere"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("shrunk template missing comment %q\n--- got ---\n%s", want, string(out))
		}
	}
}

func TestShrinkTemplateRefusesDanglingRefWithoutForce(t *testing.T) {
	// Reverse the DOD case: delete ALB, which Listener still !Ref's.
	path := writeFixture(t, fiveResourceTemplate)
	rec := fixtureFive(path)
	_, err := ShrinkTemplate(rec, "ALB", ShrinkOptions{})
	if err == nil {
		t.Fatalf("ShrinkTemplate(ALB) should fail with dangling refs, got nil")
	}
	if !IsValidationError(err) {
		t.Errorf("err is not *ValidationError: %T %v", err, err)
	}
	var v *ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("errors.As failed: %v", err)
	}
	// Listener.Properties.LoadBalancerArn = !Ref ALB → one Ref dangler.
	if v.Removed != "ALB" {
		t.Errorf("Removed = %q, want ALB", v.Removed)
	}
	if len(v.Dangling) == 0 {
		t.Fatalf("Dangling empty, want at least 1")
	}
	found := false
	for _, d := range v.Dangling {
		if d.FromLogicalID == "Listener" && d.Kind == "Ref" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected Listener.Ref dangler, got %+v", v.Dangling)
	}
}

func TestShrinkTemplateForceOverridesDanglingRef(t *testing.T) {
	path := writeFixture(t, fiveResourceTemplate)
	rec := fixtureFive(path)
	res, err := ShrinkTemplate(rec, "ALB", ShrinkOptions{Force: true, Now: time.Unix(1, 0)})
	if err != nil {
		t.Fatalf("ShrinkTemplate(ALB, force=true) returned: %v", err)
	}
	out, _ := os.ReadFile(res.ShrunkPath)
	if strings.Contains(string(out), "ALB:\n") {
		t.Errorf("shrunk template still has top-level ALB:\n%s", string(out))
	}
}

func TestShrinkTemplateDetectsLongFormReferences(t *testing.T) {
	// Long-form Ref / Fn::GetAtt / Fn::Sub all reference Removed.
	tmpl := `Resources:
  Removed:
    Type: AWS::S3::Bucket
  RefUser:
    Type: AWS::Foo::Bar
    Properties:
      P:
        Ref: Removed
  GetAttUser:
    Type: AWS::Foo::Bar
    Properties:
      P:
        Fn::GetAtt: [Removed, Arn]
  SubUser:
    Type: AWS::Foo::Bar
    Properties:
      P:
        Fn::Sub: "arn:aws:foo:${Removed}"
`
	path := writeFixture(t, tmpl)
	rec := StackRecord{
		StackName:    "longform",
		TemplatePath: path,
		Resources: []Resource{
			{LogicalID: "Removed"},
			{LogicalID: "RefUser"},
			{LogicalID: "GetAttUser"},
			{LogicalID: "SubUser"},
		},
	}
	_, err := ShrinkTemplate(rec, "Removed", ShrinkOptions{})
	if !IsValidationError(err) {
		t.Fatalf("err = %v, want ValidationError", err)
	}
	var v *ValidationError
	_ = errors.As(err, &v)
	kinds := map[string]int{}
	for _, d := range v.Dangling {
		kinds[d.Kind]++
	}
	for _, want := range []string{"Ref", "GetAtt", "Sub"} {
		if kinds[want] == 0 {
			t.Errorf("expected at least one %s dangler, got kinds=%v", want, kinds)
		}
	}
}

func TestShrinkTemplatePurgesDependsOnList(t *testing.T) {
	tmpl := `Resources:
  Removed:
    Type: AWS::S3::Bucket
  Survivor:
    Type: AWS::Foo::Bar
    DependsOn:
      - Removed
      - Other
  Other:
    Type: AWS::Foo::Bar
`
	path := writeFixture(t, tmpl)
	rec := StackRecord{
		StackName:    "deps",
		TemplatePath: path,
		Resources: []Resource{
			{LogicalID: "Removed"},
			{LogicalID: "Survivor"},
			{LogicalID: "Other"},
		},
	}
	res, err := ShrinkTemplate(rec, "Removed", ShrinkOptions{Now: time.Unix(1, 0)})
	if err != nil {
		t.Fatalf("ShrinkTemplate: %v", err)
	}
	if res.RemovedDependsOnEdits != 1 {
		t.Errorf("RemovedDependsOnEdits = %d, want 1", res.RemovedDependsOnEdits)
	}
	out, _ := os.ReadFile(res.ShrunkPath)
	if strings.Contains(string(out), "Removed") {
		t.Errorf("shrunk template still mentions Removed:\n%s", string(out))
	}
}

func TestShrinkTemplatePurgesDependsOnScalar(t *testing.T) {
	tmpl := `Resources:
  Removed:
    Type: AWS::S3::Bucket
  Survivor:
    Type: AWS::Foo::Bar
    DependsOn: Removed
`
	path := writeFixture(t, tmpl)
	rec := StackRecord{
		StackName:    "deps-scalar",
		TemplatePath: path,
		Resources:    []Resource{{LogicalID: "Removed"}, {LogicalID: "Survivor"}},
	}
	res, err := ShrinkTemplate(rec, "Removed", ShrinkOptions{Now: time.Unix(1, 0)})
	if err != nil {
		t.Fatalf("ShrinkTemplate: %v", err)
	}
	out, _ := os.ReadFile(res.ShrunkPath)
	if strings.Contains(string(out), "DependsOn") {
		t.Errorf("DependsOn should be dropped after scalar purge:\n%s", string(out))
	}
}

func TestShrinkTemplateUnknownResource(t *testing.T) {
	path := writeFixture(t, fiveResourceTemplate)
	rec := fixtureFive(path)
	_, err := ShrinkTemplate(rec, "NoSuchOne", ShrinkOptions{})
	if err == nil {
		t.Fatalf("ShrinkTemplate(NoSuchOne) should error")
	}
	if !strings.Contains(err.Error(), "NoSuchOne") {
		t.Errorf("error should name the missing id: %v", err)
	}
}

func TestShrinkCallsUpdateRunner(t *testing.T) {
	path := writeFixture(t, fiveResourceTemplate)
	rec := fixtureFive(path)
	var got UpdateRequest
	prev := SetUpdateRunner(func(_ context.Context, req UpdateRequest) error {
		got = req
		return nil
	})
	t.Cleanup(func() { SetUpdateRunner(prev) })
	res, err := Shrink(context.Background(), rec, "MyTargetGroup", ShrinkOptions{Now: time.Unix(2, 0)})
	if err != nil {
		t.Fatalf("Shrink: %v", err)
	}
	if got.StackName != "alb-dev-stack" {
		t.Errorf("update.StackName = %q", got.StackName)
	}
	if got.TemplatePath != res.ShrunkPath {
		t.Errorf("update.TemplatePath = %q, want %q", got.TemplatePath, res.ShrunkPath)
	}
	if !strings.Contains(got.Reason, "MyTargetGroup") {
		t.Errorf("update.Reason = %q (want mention of MyTargetGroup)", got.Reason)
	}
}

func TestContainsSubRefSemantics(t *testing.T) {
	cases := []struct {
		s      string
		target string
		want   bool
	}{
		{"hello ${Removed}", "Removed", true},
		{"hello ${Removed.Arn}", "Removed", true},
		{"hello ${RemovedSuffix}", "Removed", false},
		{"hello ${Other}", "Removed", false},
		{"prefix ${Removed} suffix ${Other}", "Removed", true},
	}
	for _, c := range cases {
		if got := containsSubRef(c.s, c.target); got != c.want {
			t.Errorf("containsSubRef(%q,%q) = %v, want %v", c.s, c.target, got, c.want)
		}
	}
}
