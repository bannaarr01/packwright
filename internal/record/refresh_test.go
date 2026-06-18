package record

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
)

// seedRecord writes a deployed-state record to store so the refresh helpers
// have something to read.
func seedRecord(t *testing.T, store *Store) *StackRecord {
	t.Helper()
	rec := freshRecord()
	if err := store.Write(rec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return rec
}

func TestRefreshActiveStacks_NilCFN_ReturnsOnDiskUnchanged(t *testing.T) {
	store := NewStore(t.TempDir())
	seedRecord(t, store)
	got, err := RefreshActiveStacks(context.Background(), nil, store, "acme", "dev")
	if err != nil {
		t.Fatalf("RefreshActiveStacks: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("results = %d, want 1", len(got))
	}
	if got[0].Stale != nil {
		t.Errorf("Stale = %+v, want nil for the nil-cfn path", got[0].Stale)
	}
}

func TestRefreshActiveStacks_HappyPath_UpdatesStatus(t *testing.T) {
	pinTime(t, time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC))
	store := NewStore(t.TempDir())
	prior := seedRecord(t, store)
	prior.Status.CFN = "UPDATE_IN_PROGRESS"
	prior.Status.Broad = BroadDeploying
	if err := store.Write(prior); err != nil {
		t.Fatalf("re-seed: %v", err)
	}

	cfn := newFakeCFN()
	cfn.stacks[prior.StackName] = []cfntypes.Stack{{
		StackName:       aws.String(prior.StackName),
		StackStatus:     cfntypes.StackStatusUpdateComplete,
		LastUpdatedTime: aws.Time(nowFunc()),
	}}

	results, err := RefreshActiveStacks(context.Background(), cfn, store, "acme", "dev")
	if err != nil {
		t.Fatalf("RefreshActiveStacks: %v", err)
	}
	if len(results) != 1 || results[0].Stale != nil {
		t.Fatalf("results = %+v", results)
	}
	got, err := store.Read("acme", "dev", prior.StackName)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Status.Broad != BroadDeployed {
		t.Errorf("Broad = %q, want deployed", got.Status.Broad)
	}
	if !got.Status.ReconciledAt.Equal(nowFunc()) {
		t.Errorf("ReconciledAt = %v, want pinned-now", got.Status.ReconciledAt)
	}
}

// TestRefreshActiveStacks_ClearsStaleDiscrepancy is the regression test for
// the review finding: a prior partial-discrepancy note must not survive into
// a subsequent clean refresh.
func TestRefreshActiveStacks_ClearsStaleDiscrepancy(t *testing.T) {
	pinTime(t, time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC))
	store := NewStore(t.TempDir())
	prior := freshRecord()
	prior.Status.CFN = "ROLLBACK_COMPLETE"
	prior.Status.Broad = BroadPartial
	prior.Status.Discrepancy = "CFN reports ROLLBACK_COMPLETE; all 12 resources are CREATE_COMPLETE — marking partial."
	if err := store.Write(prior); err != nil {
		t.Fatal(err)
	}

	cfn := newFakeCFN()
	cfn.stacks[prior.StackName] = []cfntypes.Stack{{
		StackName:   aws.String(prior.StackName),
		StackStatus: cfntypes.StackStatusUpdateComplete,
	}}

	if _, err := RefreshActiveStacks(context.Background(), cfn, store, "acme", "dev"); err != nil {
		t.Fatalf("RefreshActiveStacks: %v", err)
	}
	got, err := store.Read("acme", "dev", prior.StackName)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Status.Broad != BroadDeployed {
		t.Errorf("Broad = %q, want deployed", got.Status.Broad)
	}
	if got.Status.Discrepancy != "" {
		t.Errorf("Discrepancy = %q, want empty once status is clean", got.Status.Discrepancy)
	}
}

func TestRefreshActiveStacks_PreservesPartialWhenRollbackStillOpen(t *testing.T) {
	pinTime(t, time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC))
	store := NewStore(t.TempDir())
	prior := freshRecord()
	prior.Status.CFN = "ROLLBACK_COMPLETE"
	prior.Status.Broad = BroadPartial
	prior.Status.Discrepancy = "prior-note"
	if err := store.Write(prior); err != nil {
		t.Fatal(err)
	}

	cfn := newFakeCFN()
	cfn.stacks[prior.StackName] = []cfntypes.Stack{{
		StackName:   aws.String(prior.StackName),
		StackStatus: cfntypes.StackStatusRollbackComplete,
	}}

	if _, err := RefreshActiveStacks(context.Background(), cfn, store, "acme", "dev"); err != nil {
		t.Fatalf("RefreshActiveStacks: %v", err)
	}
	got, err := store.Read("acme", "dev", prior.StackName)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Status.Broad != BroadPartial || got.Status.Discrepancy != "prior-note" {
		t.Errorf("partial discrepancy not preserved: broad=%q discrepancy=%q",
			got.Status.Broad, got.Status.Discrepancy)
	}
}

func TestRefreshActiveStacks_DescribeError_MarksStale(t *testing.T) {
	store := NewStore(t.TempDir())
	prior := seedRecord(t, store)

	cfn := newFakeCFN()
	cfn.errCalls[prior.StackName] = errExploded{}

	results, err := RefreshActiveStacks(context.Background(), cfn, store, "acme", "dev")
	if err != nil {
		t.Fatalf("RefreshActiveStacks: %v", err)
	}
	if len(results) != 1 || results[0].Stale == nil {
		t.Fatalf("expected one Stale result, got %+v", results)
	}
}

func TestRefreshActiveStacks_StackNotFound_MarksDeleted(t *testing.T) {
	pinTime(t, time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC))
	store := NewStore(t.TempDir())
	prior := seedRecord(t, store)

	cfn := newFakeCFN()
	cfn.errCalls[prior.StackName] = stackNotFoundError{}

	if _, err := RefreshActiveStacks(context.Background(), cfn, store, "acme", "dev"); err != nil {
		t.Fatalf("RefreshActiveStacks: %v", err)
	}
	got, err := store.Read("acme", "dev", prior.StackName)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Status.Broad != BroadDeleted {
		t.Errorf("Broad = %q, want deleted", got.Status.Broad)
	}
}

// errExploded is a generic non-AWS error used to simulate transport failures
// that refreshOne should surface as Stale rather than mark as deleted.
type errExploded struct{}

func (errExploded) Error() string { return "boom" }
