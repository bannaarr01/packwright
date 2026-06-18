package record

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
)

// MaxRefreshConcurrency caps how many DescribeStacks calls the launch-time
// reconciliation runs in flight. ADR-0046 §"Reconciliation on launch" pins
// this at 8 — high enough that 20–30 stacks finish in well under a second,
// low enough that the API quota and the user's terminal aren't slammed.
const MaxRefreshConcurrency = 8

// Stale is the flag a refreshed record carries when DescribeStacks failed:
// the on-disk record is left as last seen and surfaced to the UI with a
// "stale" badge so the operator knows the status field may not match AWS.
//
// PR-02 introduces the flag only as a return value from RefreshActiveStacks;
// PR-09 / PR-10 land the UI rendering and the on-disk persistence of the
// flag.
type Stale struct {
	StackName string
	Reason    string
}

// RefreshResult bundles the outcome of a single record's refresh.
type RefreshResult struct {
	Record *StackRecord
	Stale  *Stale
}

// RefreshActiveStacks does a lightweight status refresh for every record
// under (project, env). For each stack it issues DescribeStacks (no resource
// fetch), updates the record's Status.CFN, broad, and reconciled_at, and
// writes the file back. Outputs / resources are left untouched — they
// re-harvest only when the user opens the row.
//
// Errors against AWS are tolerated: the record stays as last seen and is
// returned with a Stale annotation so the front-end can flag it. A nil cfn
// (no profile loaded yet) short-circuits to the on-disk records with no
// staleness flag — they are simply not refreshed.
//
// Concurrency is bounded by MaxRefreshConcurrency. The function blocks until
// every stack has been refreshed or skipped.
func RefreshActiveStacks(ctx context.Context, cfn cloudFormationAPI, store *Store, project, env string) ([]RefreshResult, error) {
	if store == nil {
		return nil, errors.New("record: RefreshActiveStacks: store is nil")
	}
	records, err := store.List(project, env)
	if err != nil {
		return nil, fmt.Errorf("record: list: %w", err)
	}
	if cfn == nil {
		// No live AWS; surface the on-disk view unchanged.
		out := make([]RefreshResult, 0, len(records))
		for _, rec := range records {
			out = append(out, RefreshResult{Record: rec})
		}
		return out, nil
	}

	results := make([]RefreshResult, len(records))
	sem := make(chan struct{}, MaxRefreshConcurrency)
	var wg sync.WaitGroup

	for i, rec := range records {
		i, rec := i, rec
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = refreshOne(ctx, cfn, store, rec)
		}()
	}
	wg.Wait()
	return results, nil
}

// refreshOne runs DescribeStacks on a single record and writes the updated
// status back, or returns the prior record with a Stale flag when AWS does
// not cooperate.
func refreshOne(ctx context.Context, cfn cloudFormationAPI, store *Store, rec *StackRecord) RefreshResult {
	out, err := cfn.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{
		StackName: aws.String(rec.StackName),
	})
	if err != nil {
		if isStackNotFound(err) {
			now := nowFunc()
			rec.Status = Status{
				CFN:          "",
				Broad:        BroadDeleted,
				ReconciledAt: now,
			}
			rec.LastUpdatedAt = now
			if writeErr := store.Write(rec); writeErr != nil {
				return RefreshResult{
					Record: rec,
					Stale:  &Stale{StackName: rec.StackName, Reason: "write deleted: " + writeErr.Error()},
				}
			}
			return RefreshResult{Record: rec}
		}
		return RefreshResult{
			Record: rec,
			Stale:  &Stale{StackName: rec.StackName, Reason: err.Error()},
		}
	}
	if len(out.Stacks) == 0 {
		return RefreshResult{
			Record: rec,
			Stale:  &Stale{StackName: rec.StackName, Reason: "DescribeStacks returned no stacks"},
		}
	}
	stack := out.Stacks[0]
	cfnStatus := string(stack.StackStatus)
	verdict := reconcile(cfnStatus, nil, false, false)
	// Without a resource fetch we cannot detect the partial-discrepancy
	// case at refresh time — that lights up only after a full harvest. If
	// the prior record was partial AND CFN is still in a failed/rollback
	// state, preserve the prior discrepancy so the UI doesn't lose the
	// note. A clean status assigns Discrepancy = "" so a previously-set
	// note is dropped when the disagreement no longer holds.
	if verdict.Broad == BroadFailed && rec.Status.Broad == BroadPartial {
		verdict.Broad = BroadPartial
		verdict.Discrepancy = rec.Status.Discrepancy
	}
	rec.Status.CFN = cfnStatus
	rec.Status.Broad = verdict.Broad
	rec.Status.Discrepancy = verdict.Discrepancy
	rec.Status.ReconciledAt = nowFunc()
	if stack.LastUpdatedTime != nil {
		rec.LastUpdatedAt = stack.LastUpdatedTime.UTC()
	}
	if err := store.Write(rec); err != nil {
		return RefreshResult{
			Record: rec,
			Stale:  &Stale{StackName: rec.StackName, Reason: "write refreshed: " + err.Error()},
		}
	}
	return RefreshResult{Record: rec}
}
