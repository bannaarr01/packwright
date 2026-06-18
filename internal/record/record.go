// Package record owns the on-disk "stack record" — Packwright's local typed
// snapshot of every CloudFormation stack it deploys.
//
// A record is the merged output of two read-only CloudFormation calls (per
// ADR-0046): DescribeStacks (status, outputs, parameters) plus
// DescribeStackResources (every logical→physical mapping). Records survive
// process restarts and are the input to the update / scale / cascading-delete
// flows landing in later PRs.
//
// Schema versioning. Every record carries SchemaVersion. The loader rejects
// unknown major versions so an older Packwright binary cannot silently
// misread a newer file. Migrations land in their own PR if and when the shape
// changes.
package record

import "time"

// SchemaVersion is the on-disk schema tag every record carries. Bump the
// major component when the shape changes incompatibly; minor / patch
// adjustments must remain backward-readable.
const SchemaVersion = "packwright.stack-record.v1"

// BroadStatus is the high-level state Packwright surfaces in the UI badge,
// computed from the raw CloudFormation StackStatus + the per-resource statuses
// (see status.go). It is deliberately coarser than CloudFormation's own status
// enum so the UI can render one of seven dispositions instead of forty.
type BroadStatus string

// Broad-status values. The set is closed; see ADR-0046 §"Broad status".
const (
	BroadDraft     BroadStatus = "draft"
	BroadDeploying BroadStatus = "deploying"
	BroadDeployed  BroadStatus = "deployed"
	BroadPartial   BroadStatus = "partial"
	BroadFailed    BroadStatus = "failed"
	BroadDrifted   BroadStatus = "drifted"
	BroadDeleted   BroadStatus = "deleted"
)

// HistoryKind identifies which engine action produced a HistoryEntry.
type HistoryKind string

// Recognised history kinds. Future PRs add more (e.g. KindUpdate from PR-06).
const (
	KindCreate        HistoryKind = "create"
	KindUpdate        HistoryKind = "update"
	KindScale         HistoryKind = "scale"
	KindDeleteAttempt HistoryKind = "delete-attempt"
)

// HistoryResult is the success/failure verdict captured per history entry.
type HistoryResult string

// Recognised history results.
const (
	ResultSuccess HistoryResult = "success"
	ResultFailure HistoryResult = "failure"
)

// MaxHistoryEntries caps how many entries we keep before dropping the oldest.
// Older entries remain in the structured log; the record is meant to be small.
const MaxHistoryEntries = 50

// StackRecord is the v1 on-disk shape. JSON tags mirror the field order shown
// in ADR-0046's example payload so a `cat`'d file reads top-to-bottom in the
// same order documented there.
type StackRecord struct {
	SchemaVersion string         `json:"schema_version"`
	StackName     string         `json:"stack_name"`
	Manifest      ManifestRef    `json:"manifest"`
	Project       string         `json:"project"`
	Env           string         `json:"env"`
	Profile       string         `json:"profile"`
	Region        string         `json:"region"`
	Account       string         `json:"account"`
	Status        Status         `json:"status"`
	DeployedAt    time.Time      `json:"deployed_at"`
	LastUpdatedAt time.Time      `json:"last_updated_at"`
	Parameters    Parameters     `json:"parameters"`
	Outputs       []Output       `json:"outputs"`
	Resources     []Resource     `json:"resources"`
	History       []HistoryEntry `json:"history"`
}

// ManifestRef pins the record back to the manifest file that produced it. The
// slash command (`/alb`) doubles as a stable identity across renamed source
// paths; Source is the on-disk location at deploy time for human grep.
type ManifestRef struct {
	Slash  string `json:"slash"`
	Source string `json:"source"`
}

// Status bundles the raw CFN status, the derived broad status, the timestamp
// of the last reconciliation, and an optional human-readable note covering
// any mismatch between the two (the "CFN reports rollback but all resources
// are healthy" case from the user's directive).
type Status struct {
	CFN          string      `json:"cfn"`
	Broad        BroadStatus `json:"broad"`
	ReconciledAt time.Time   `json:"reconciled_at"`
	Discrepancy  string      `json:"discrepancy,omitempty"`
}

// Parameters is the deployed-stack parameter map keyed by parameter name. It
// is a typed alias so callers don't accidentally compare it to a plain
// map[string]string from elsewhere.
type Parameters map[string]string

// Output is one CloudFormation stack output. Slice order mirrors the order
// the AWS API returned the outputs in — keeps the file stable round-trip.
type Output struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Resource is one entry from DescribeStackResources, with the four fields
// the cascading-delete UI (ADR-0053) needs to render a per-resource row.
type Resource struct {
	LogicalID  string `json:"logical_id"`
	PhysicalID string `json:"physical_id"`
	Type       string `json:"type"`
	Status     string `json:"status"`
}

// HistoryEntry is one terminal-state event appended to the record after a
// successful harvest. PR-02 only writes KindCreate entries; later PRs append
// update / scale / delete-attempt rows from their own call sites.
type HistoryEntry struct {
	At             time.Time     `json:"at"`
	Kind           HistoryKind   `json:"kind"`
	Result         HistoryResult `json:"result"`
	ChangesetID    string        `json:"changeset_id,omitempty"`
	ParametersDiff string        `json:"parameters_diff,omitempty"`
}
