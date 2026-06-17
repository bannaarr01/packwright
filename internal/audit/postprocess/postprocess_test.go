package postprocess

import (
	"context"
	"testing"

	"github.com/bannaarr01/packwright/internal/audit"
	"github.com/bannaarr01/packwright/internal/audit/cost"
)

// TestApplyEnrichesEveryResource verifies that Apply populates
// LastUsed and CostEstimate for every Resource it receives, even when
// no AWS clients are configured. The composers fall back to static
// signals (CreateTime, AttachTime) which is honest behaviour for an
// offline test.
func TestApplyEnrichesEveryResource(t *testing.T) {
	c := audit.NewForTest(audit.WithRegion("us-east-1"))
	resources := []audit.Resource{
		{
			Kind: "ec2/volume",
			ID:   "vol-test",
			Raw: map[string]any{
				"volume_type": "gp3",
				"size_gb":     int64(10),
			},
		},
		{
			Kind: "ec2/eip",
			ID:   "eipalloc-test",
		},
	}
	Apply(context.Background(), c, resources, Options{})
	for i, r := range resources {
		if r.LastUsed == nil {
			t.Errorf("resources[%d].LastUsed nil after Apply", i)
		}
		if r.CostEstimate == nil {
			t.Errorf("resources[%d].CostEstimate nil after Apply", i)
		}
	}
}

// TestApplyEC2VolumeCostUsesSnapshot verifies that a gp3 volume with
// a known size produces a positive monthly cost from the embedded
// pricing snapshot — the canonical end-to-end smoke for cost wiring.
func TestApplyEC2VolumeCostUsesSnapshot(t *testing.T) {
	c := audit.NewForTest(audit.WithRegion("us-east-1"))
	resources := []audit.Resource{{
		Kind: "ec2/volume",
		ID:   "vol-test",
		Raw: map[string]any{
			"volume_type": "gp3",
			"size_gb":     int64(100),
		},
	}}
	Apply(context.Background(), c, resources, Options{})
	if resources[0].CostEstimate == nil {
		t.Fatal("CostEstimate nil")
	}
	if resources[0].CostEstimate.MonthlyUSD <= 0 {
		t.Errorf("MonthlyUSD = %v, want positive", resources[0].CostEstimate.MonthlyUSD)
	}
	if resources[0].CostEstimate.Source != cost.SourceSnapshot {
		t.Errorf("Source = %q, want %q", resources[0].CostEstimate.Source, cost.SourceSnapshot)
	}
}
