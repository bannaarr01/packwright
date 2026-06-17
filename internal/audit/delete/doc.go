// Package delete implements MVP-6 PR-04's human-initiated deletion
// workflow: a staging tray (Add/List/Remove/Clear persisted at
// <home>/audit/tray.json), a dependency probe that surfaces AWS
// references via read-only Describe* calls, a batch-consent contract
// requiring per-row checkboxes plus a typed "DELETE" confirmation,
// and an executor that runs the user's selected Delete* calls in
// dependency-sorted order while emitting per-row progress events on
// the [stream.EventBus].
//
// The same Delete* helpers also back the audit/delete-* write tools
// registered into [tools.Default]. When the AI invokes one of those
// tools the call flows through the standard consent gate in
// [internal/ai/consent]; when a human drives the batch flow, the
// in-package [Executor] uses the helpers directly and never re-prompts
// (the typed-DELETE confirmation in [Batch] is the data-layer gate).
//
// Per ADR-0043, deletion of RDS DB instances, S3 buckets, and KMS
// keys is explicitly excluded from v1 and no audit/delete-* tool is
// registered for those kinds.
package delete
