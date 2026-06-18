package record

import (
	"fmt"
	"strings"
)

// reconcileResult is the output of reconcile — the derived broad status plus
// a non-empty Discrepancy whenever the broad status had to disagree with what
// the raw CFN StackStatus alone would suggest. The receiver copies these into
// the StackRecord's Status struct.
type reconcileResult struct {
	Broad       BroadStatus
	Discrepancy string
}

// reconcile derives the broad-status indicator from the raw CloudFormation
// StackStatus and the list of per-resource statuses observed during the
// same harvest. Implements the table from ADR-0046 §"Broad status".
//
// The "all resources CREATE_COMPLETE" check is interpreted literally: a
// resource status of exactly "CREATE_COMPLETE" counts as healthy; anything
// else (including UPDATE_COMPLETE) does not. This matches the ADR's worked
// example — "12/12 resources CREATE_COMPLETE" — and the PR-02 acceptance
// test fixture. Future PRs may broaden the predicate without changing
// callers; the input signature is intentionally narrow.
//
// driftDetected is true when the caller has separately invoked
// cloudformation:DetectStackDrift and seen drift. PR-02 always passes false;
// the parameter exists so the table is complete and PR-13 can wire it.
//
// stackMissing is true when DescribeStacks returned a ValidationError because
// the stack no longer exists in the account. Indicates the stack was deleted
// outside Packwright; the caller decides whether to write a "deleted" record
// or skip the write entirely.
func reconcile(cfnStatus string, resourceStatuses []string, driftDetected, stackMissing bool) reconcileResult {
	if stackMissing {
		return reconcileResult{Broad: BroadDeleted}
	}
	if driftDetected {
		return reconcileResult{Broad: BroadDrifted}
	}

	status := strings.ToUpper(strings.TrimSpace(cfnStatus))

	// Empty / placeholder status: the record was created locally but no
	// real stack was ever rolled out (ADR-0047, draft-record path).
	if status == "" {
		return reconcileResult{Broad: BroadDraft}
	}

	switch {
	case isInProgress(status):
		return reconcileResult{Broad: BroadDeploying}

	case isHealthyComplete(status):
		// CFN says success and every resource is healthy: deployed.
		return reconcileResult{Broad: BroadDeployed}

	case isFailedOrRolledBack(status):
		if len(resourceStatuses) > 0 && allCreateComplete(resourceStatuses) {
			// CFN reports failure / rollback yet every resource is
			// CREATE_COMPLETE. Surface this disagreement — it is the
			// specific case the user called out.
			return reconcileResult{
				Broad: BroadPartial,
				Discrepancy: fmt.Sprintf(
					"CFN reports %s; all %d resources are CREATE_COMPLETE — marking partial.",
					status, len(resourceStatuses),
				),
			}
		}
		return reconcileResult{Broad: BroadFailed}

	default:
		// An unrecognised CFN status (review_in_progress, import_*) is
		// safest treated as "deploying" — the operator can refresh and
		// the next harvest will reclassify.
		return reconcileResult{Broad: BroadDeploying}
	}
}

// isInProgress reports whether status is one of CFN's *_IN_PROGRESS values,
// covering create / update / delete / rollback in-flight states.
func isInProgress(status string) bool {
	return strings.HasSuffix(status, "_IN_PROGRESS")
}

// isHealthyComplete reports whether status is one of the CFN terminal states
// that mean "the stack reached its desired shape" — CREATE_COMPLETE and
// UPDATE_COMPLETE. ROLLBACK_COMPLETE is deliberately excluded; the rollback
// is a failure surface, not a success.
func isHealthyComplete(status string) bool {
	switch status {
	case "CREATE_COMPLETE", "UPDATE_COMPLETE":
		return true
	}
	return false
}

// isFailedOrRolledBack reports whether status is one of CFN's terminal
// unhealthy states. ROLLBACK_COMPLETE counts: it is the state CloudFormation
// leaves a freshly-failed create in once the rollback finishes.
func isFailedOrRolledBack(status string) bool {
	switch status {
	case "CREATE_FAILED",
		"ROLLBACK_FAILED",
		"ROLLBACK_COMPLETE",
		"DELETE_FAILED",
		"UPDATE_FAILED",
		"UPDATE_ROLLBACK_FAILED",
		"UPDATE_ROLLBACK_COMPLETE",
		"IMPORT_ROLLBACK_FAILED",
		"IMPORT_ROLLBACK_COMPLETE":
		return true
	}
	return false
}

// allCreateComplete reports whether every resource status in the slice is
// exactly "CREATE_COMPLETE". An empty slice returns false — without any
// resource evidence the caller cannot assert "all healthy".
func allCreateComplete(statuses []string) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, s := range statuses {
		if s != "CREATE_COMPLETE" {
			return false
		}
	}
	return true
}
