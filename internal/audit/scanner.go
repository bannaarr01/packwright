// Package audit implements the read-only inventory walk that powers the
// `/audit` command (ADR-0040). It defines the Scanner contract, a typed
// registry that enforces the read-only-by-construction invariant, a
// per-service token bucket, and the worker pool that drives every
// registered scanner concurrently.
//
// Concrete scanners live in internal/audit/scanners; each registers itself
// with [Default] from its init function. Tests build isolated registries
// with [NewRegistry] so package-level state never leaks between cases.
package audit

import (
	"context"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/cost"
	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

// Scanner is the unit of inventory work: one resource kind, one set of
// read-only AWS calls. Implementations live in internal/audit/scanners.
//
// The contract is intentionally narrow:
//
//   - Kind is the stable, slash-delimited identifier reported in events and
//     surfaced in the UI ("ec2/instance", "rds/db-snapshot", ...).
//   - Permissions returns the IAM actions Scan touches, as a constant slice.
//     The registry validates them against a Describe*/List*/Get* allowlist
//     at Register time, so a scanner that names a mutating action never
//     ships (ADR-0040).
//   - Scan walks the service paginators fully and returns the complete set
//     of resources it found. Partial pages are never returned; on error the
//     scanner returns nil and the error so the pool can surface it as a
//     row-level warning rather than failing the whole audit.
//
// Scan must paginate via the SDK paginator helpers (or an equivalent
// NextToken loop) so accounts with thousands of resources are handled
// without truncation.
type Scanner interface {
	// Kind returns the stable kind identifier (e.g. "ec2/instance").
	Kind() string
	// Permissions returns the IAM actions Scan touches. The slice is
	// expected to be constant — the registry uses it to enforce the
	// read-only invariant.
	Permissions() []string
	// Scan walks the AWS APIs for this kind and returns every resource the
	// caller's credentials can see, fully paginated. The pool emits
	// Started before Scan and Done/Error after it returns — the
	// scanner itself only emits Progress (per page) and Warn (for
	// recoverable per-resource failures) via the ScannerEmitter.
	Scan(ctx context.Context, c *Client, emit ScannerEmitter) ([]Resource, error)
}

// Resource is the audit-layer view of a single AWS resource. Fields are
// a superset of what every scanner can populate; a scanner leaves
// unknown fields zero. LastUsed and CostEstimate are populated by the
// post-processing step in internal/audit/postprocess after every
// scanner returns (ADR-0041 / ADR-0042).
type Resource struct {
	Kind         string
	ID           string
	Region       string
	Account      string
	Name         string
	Tags         map[string]string
	CreatedAt    time.Time
	State        string
	Raw          map[string]any
	LastUsed     *LastUsedSignal
	CostEstimate *CostEstimate
}

// LastUsedSignal is the per-resource idleness summary surfaced on every
// Resource. The canonical definition lives in internal/audit/lastused
// — this alias keeps the audit-layer call sites readable
// (Resource.LastUsed is *LastUsedSignal, not *lastused.LastUsedSignal).
type LastUsedSignal = lastused.LastUsedSignal

// CostEstimate is the per-resource cost summary surfaced on every
// Resource. Alias to cost.CostEstimate for the same readability reason
// as LastUsedSignal.
type CostEstimate = cost.CostEstimate

// EventType identifies which lifecycle event a scanner emitted.
type EventType int

// Event types mirror the audit.scanner.* names in ADR-0040.
const (
	// EventStarted fires once per scanner before the first paginator call.
	EventStarted EventType = iota
	// EventProgress fires with a running count after each page so the UI
	// can show a live "scanned N so far" strip.
	EventProgress
	// EventDone fires once per scanner with the final resource count.
	EventDone
	// EventError fires when a scanner returns a non-nil error. The pool
	// captures it and surfaces it as a row-level warning rather than
	// aborting the audit.
	EventError
	// EventWarn fires when a scanner observes a partial-permission case
	// (e.g. AccessDenied on a sub-call) and wants to keep going.
	EventWarn
)

// Event is one entry in the scanner event stream. Both UIs render the
// stream as a live progress strip.
type Event struct {
	Type  EventType
	Kind  string
	Count int
	Err   error
	Msg   string
}

// ScannerEmitter is the narrow event sink a Scanner writes to during
// Scan. The pool supplies a kind-bound instance so scanners do not need
// to thread their own Kind() through every call.
//
// Tests that exercise a single scanner pass [NoopEmitter] (drops every
// event) or build a [RecordingEmitter] (collects events into a slice).
type ScannerEmitter interface {
	// Progress reports the running count of resources collected so far.
	// The pool turns it into an EventProgress with the scanner's kind.
	Progress(count int)
	// Warn reports a non-fatal observation (e.g. a per-resource sub-call
	// returned AccessDenied) so the UI can flag partial results without
	// abandoning the scan.
	Warn(msg string)
}

// NoopEmitter discards every event. Used by tests that only care about
// the resources a Scan call returns.
type NoopEmitter struct{}

// Progress implements ScannerEmitter.
func (NoopEmitter) Progress(int) {}

// Warn implements ScannerEmitter.
func (NoopEmitter) Warn(string) {}

// RecordingEmitter captures every Progress/Warn call into in-memory
// slices so tests can assert exact event sequences without channel
// plumbing.
type RecordingEmitter struct {
	Counts []int
	Warns  []string
}

// Progress implements ScannerEmitter.
func (r *RecordingEmitter) Progress(count int) { r.Counts = append(r.Counts, count) }

// Warn implements ScannerEmitter.
func (r *RecordingEmitter) Warn(msg string) { r.Warns = append(r.Warns, msg) }
