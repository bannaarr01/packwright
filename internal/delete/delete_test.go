package delete

import (
	"context"
	"errors"
	"testing"
)

// fixtureFive returns a stack record with five resources, none meta.
// The record is the canonical "5-resource alb-dev-stack" used in the
// DOD: removing one resource that no other references should leave a
// clean four-resource template.
func fixtureFive(templatePath string) StackRecord {
	return StackRecord{
		StackName:    "alb-dev-stack",
		TemplatePath: templatePath,
		ManifestPath: "manifests/alb.manifest.yaml",
		Resources: []Resource{
			{LogicalID: "ALB", Type: "AWS::ElasticLoadBalancingV2::LoadBalancer"},
			{LogicalID: "Listener", Type: "AWS::ElasticLoadBalancingV2::Listener"},
			{LogicalID: "Bucket", Type: "AWS::S3::Bucket"},
			{LogicalID: "Logs", Type: "AWS::Logs::LogGroup"},
			{LogicalID: "MyTargetGroup", Type: "AWS::ElasticLoadBalancingV2::TargetGroup"},
		},
	}
}

func TestResolveTemplateShrinkWhenSurvivorsRemain(t *testing.T) {
	rec := fixtureFive("/tmp/alb.yaml")
	res, err := Resolve(rec, "MyTargetGroup")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if res.Mode != ModeTemplateShrink {
		t.Errorf("Mode = %q, want %q", res.Mode, ModeTemplateShrink)
	}
	if res.NeedsPrompt {
		t.Errorf("NeedsPrompt = true, want false (4 survivors)")
	}
	if res.Remaining != 4 {
		t.Errorf("Remaining = %d, want 4", res.Remaining)
	}
	if res.Target.LogicalID != "MyTargetGroup" {
		t.Errorf("Target.LogicalID = %q", res.Target.LogicalID)
	}
}

func TestResolveLastResourcePrompts(t *testing.T) {
	rec := StackRecord{
		StackName:    "solo",
		TemplatePath: "/tmp/solo.yaml",
		Resources: []Resource{
			{LogicalID: "OnlyOne", Type: "AWS::S3::Bucket"},
		},
	}
	res, err := Resolve(rec, "OnlyOne")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if res.Mode != ModeStackDelete {
		t.Errorf("Mode = %q, want %q (default for last-resource prompt)", res.Mode, ModeStackDelete)
	}
	if !res.NeedsPrompt {
		t.Errorf("NeedsPrompt = false, want true")
	}
	if res.Remaining != 0 {
		t.Errorf("Remaining = %d, want 0", res.Remaining)
	}
}

func TestResolveMetaResourcesAreExcluded(t *testing.T) {
	rec := StackRecord{
		StackName:    "with-wait",
		TemplatePath: "/tmp/x.yaml",
		Resources: []Resource{
			{LogicalID: "Bucket", Type: "AWS::S3::Bucket"},
			{LogicalID: "WaitCond", Type: "AWS::CloudFormation::WaitCondition", Meta: true},
		},
	}
	res, err := Resolve(rec, "Bucket")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if !res.NeedsPrompt {
		t.Errorf("NeedsPrompt = false (meta resource counted as survivor)")
	}
	if res.Remaining != 0 {
		t.Errorf("Remaining = %d, want 0 (Meta excluded)", res.Remaining)
	}
}

func TestResolveResourceNotFound(t *testing.T) {
	rec := fixtureFive("/tmp/alb.yaml")
	_, err := Resolve(rec, "NoSuchID")
	if !errors.Is(err, ErrResourceNotFound) {
		t.Errorf("err = %v, want ErrResourceNotFound", err)
	}
}

func TestResolveRejectsMetaTargets(t *testing.T) {
	rec := StackRecord{
		StackName:    "with-wait",
		TemplatePath: "/tmp/x.yaml",
		Resources: []Resource{
			{LogicalID: "Bucket", Type: "AWS::S3::Bucket"},
			{LogicalID: "WaitCond", Type: "AWS::CloudFormation::WaitCondition", Meta: true},
		},
	}
	_, err := Resolve(rec, "WaitCond")
	if err == nil {
		t.Fatalf("Resolve(meta) should error, got nil")
	}
}

func TestResolveEmptyLogicalID(t *testing.T) {
	rec := fixtureFive("/tmp/alb.yaml")
	_, err := Resolve(rec, "")
	if err == nil {
		t.Fatalf("Resolve(empty id) should error, got nil")
	}
}

func TestResolveEmptyStack(t *testing.T) {
	rec := StackRecord{
		StackName:    "only-meta",
		TemplatePath: "/tmp/x.yaml",
		Resources: []Resource{
			{LogicalID: "WaitCond", Meta: true},
		},
	}
	_, err := Resolve(rec, "WaitCond")
	if err == nil {
		t.Fatalf("Resolve(only-meta) should error, got nil")
	}
}

func TestParseMode(t *testing.T) {
	cases := []struct {
		in   string
		want Mode
		err  bool
	}{
		{"", "", false},
		{"template-shrink", ModeTemplateShrink, false},
		{"stack-delete", ModeStackDelete, false},
		{"adopt-and-delete", ModeAdoptAndDelete, false},
		{"nope", "", true},
	}
	for _, c := range cases {
		got, err := ParseMode(c.in)
		if c.err {
			if err == nil {
				t.Errorf("ParseMode(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseMode(%q) unexpected error: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseMode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSetUpdateRunnerRestores(t *testing.T) {
	ctx := context.Background()
	if err := runUpdate(ctx, UpdateRequest{}); !errors.Is(err, ErrUpdateRunnerNotSet) {
		t.Fatalf("default runUpdate err = %v, want ErrUpdateRunnerNotSet", err)
	}
	called := false
	prev := SetUpdateRunner(func(_ context.Context, _ UpdateRequest) error {
		called = true
		return nil
	})
	t.Cleanup(func() { SetUpdateRunner(prev) })
	if err := runUpdate(ctx, UpdateRequest{StackName: "x"}); err != nil {
		t.Fatalf("runUpdate after SetUpdateRunner: %v", err)
	}
	if !called {
		t.Errorf("custom runner was not called")
	}
}

func TestSetUpdateRunnerNilRestoresFallback(t *testing.T) {
	prev := SetUpdateRunner(func(_ context.Context, _ UpdateRequest) error { return nil })
	defer SetUpdateRunner(prev)
	SetUpdateRunner(nil)
	if err := runUpdate(context.Background(), UpdateRequest{}); !errors.Is(err, ErrUpdateRunnerNotSet) {
		t.Errorf("after SetUpdateRunner(nil), err = %v, want ErrUpdateRunnerNotSet", err)
	}
}
