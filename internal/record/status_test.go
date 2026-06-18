package record

import (
	"strings"
	"testing"
)

// repeat returns a slice of length n filled with s — convenience for the
// "every resource is CREATE_COMPLETE" fixtures.
func repeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

// TestReconcile_Table walks the ADR-0046 truth table row by row. Each row is
// one branch of reconcile; the suite is structured so a regression in any
// arm fails its own named subtest.
func TestReconcile_Table(t *testing.T) {
	tests := []struct {
		name            string
		cfnStatus       string
		resources       []string
		driftDetected   bool
		stackMissing    bool
		wantBroad       BroadStatus
		wantDiscrepancy bool // we only assert presence; the message wording is documented in the func, not pinned in tests.
	}{
		{
			name:      "deployed: CREATE_COMPLETE + all resources CREATE_COMPLETE",
			cfnStatus: "CREATE_COMPLETE",
			resources: repeat("CREATE_COMPLETE", 12),
			wantBroad: BroadDeployed,
		},
		{
			name:      "deployed: UPDATE_COMPLETE counts as healthy",
			cfnStatus: "UPDATE_COMPLETE",
			resources: repeat("CREATE_COMPLETE", 3),
			wantBroad: BroadDeployed,
		},
		{
			name:      "deploying: CREATE_IN_PROGRESS",
			cfnStatus: "CREATE_IN_PROGRESS",
			resources: []string{"CREATE_IN_PROGRESS"},
			wantBroad: BroadDeploying,
		},
		{
			name:      "deploying: UPDATE_IN_PROGRESS",
			cfnStatus: "UPDATE_IN_PROGRESS",
			resources: []string{"UPDATE_IN_PROGRESS"},
			wantBroad: BroadDeploying,
		},
		{
			// The user's specifically-named discrepancy: CFN rolled
			// back but every resource on the way down actually
			// finished CREATE_COMPLETE.
			name:            "partial: ROLLBACK_COMPLETE with 12/12 resources CREATE_COMPLETE",
			cfnStatus:       "ROLLBACK_COMPLETE",
			resources:       repeat("CREATE_COMPLETE", 12),
			wantBroad:       BroadPartial,
			wantDiscrepancy: true,
		},
		{
			name:      "failed: CREATE_FAILED with mixed resource statuses",
			cfnStatus: "CREATE_FAILED",
			resources: []string{"CREATE_COMPLETE", "CREATE_FAILED"},
			wantBroad: BroadFailed,
		},
		{
			name:      "failed: ROLLBACK_COMPLETE with no resources (CFN failed before any resource finished)",
			cfnStatus: "ROLLBACK_COMPLETE",
			resources: nil,
			wantBroad: BroadFailed,
		},
		{
			name:          "drifted: drift detection wins regardless of CFN status",
			cfnStatus:     "CREATE_COMPLETE",
			resources:     repeat("CREATE_COMPLETE", 2),
			driftDetected: true,
			wantBroad:     BroadDrifted,
		},
		{
			name:         "deleted: stack does not exist in account anymore",
			cfnStatus:    "",
			stackMissing: true,
			wantBroad:    BroadDeleted,
		},
		{
			name:      "draft: record exists but CFN status is empty (no deploy ever ran)",
			cfnStatus: "",
			resources: nil,
			wantBroad: BroadDraft,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := reconcile(tc.cfnStatus, tc.resources, tc.driftDetected, tc.stackMissing)
			if got.Broad != tc.wantBroad {
				t.Errorf("Broad = %q, want %q", got.Broad, tc.wantBroad)
			}
			if tc.wantDiscrepancy && got.Discrepancy == "" {
				t.Errorf("Discrepancy = empty, want non-empty for %s/%q", tc.cfnStatus, tc.wantBroad)
			}
			if !tc.wantDiscrepancy && got.Discrepancy != "" {
				t.Errorf("Discrepancy = %q, want empty", got.Discrepancy)
			}
		})
	}
}

// TestReconcile_PartialMessageNamesTheStatusAndCount documents the discrepancy
// note format the UI surfaces: it must mention both the raw CFN status and the
// resource count so the operator understands what disagreement Packwright is
// flagging. We assert presence of both, not the exact wording.
func TestReconcile_PartialMessageNamesTheStatusAndCount(t *testing.T) {
	got := reconcile("ROLLBACK_COMPLETE", repeat("CREATE_COMPLETE", 12), false, false)
	if got.Broad != BroadPartial {
		t.Fatalf("Broad = %q, want partial", got.Broad)
	}
	if !strings.Contains(got.Discrepancy, "ROLLBACK_COMPLETE") {
		t.Errorf("Discrepancy %q should mention the CFN status", got.Discrepancy)
	}
	if !strings.Contains(got.Discrepancy, "12") {
		t.Errorf("Discrepancy %q should mention the resource count", got.Discrepancy)
	}
}

// TestReconcile_CaseAndWhitespace asserts the reconciler is forgiving of the
// CFN status string's casing and surrounding whitespace. AWS itself returns
// upper-snake, but defensive normalisation keeps the function pure-input.
func TestReconcile_CaseAndWhitespace(t *testing.T) {
	got := reconcile(" create_complete ", []string{"CREATE_COMPLETE"}, false, false)
	if got.Broad != BroadDeployed {
		t.Errorf("Broad = %q, want deployed (case/whitespace tolerant)", got.Broad)
	}
}
