// Package sources holds the generic signal-gathering helpers shared by
// every per-kind composer in the parent lastused package: CloudWatch
// metric scans (cw_metric.go), CloudWatch Logs most-recent-event
// lookups (cw_logs.go), and ENI status-change reads (eni.go).
//
// Each helper declares the narrowest possible client interface — just
// the AWS calls it actually needs — so production wiring can adapt the
// SDK in a separate glue layer and tests can drive every branch with
// fakes. No file in this package imports the AWS SDK.
//
// Helpers return a [lastused.LastUsedSource] rather than a raw
// timestamp; that way "no datapoint in window" is a first-class outcome
// (Value == nil) and per-kind composers don't have to special-case it.
package sources

import (
	"time"

	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

// Static returns a source whose Value is t (nil when t is nil) and
// whose name is n, with zero Cost. Useful for surfacing already-known
// timestamps (LaunchTime, CreateTime, SnapshotCreateTime, ...) as
// first-class sources alongside the AWS-call-driven ones.
func Static(n string, t *time.Time) lastused.LastUsedSource {
	return lastused.LastUsedSource{Name: n, Value: CopyTimePtr(t)}
}

// CopyTimePtr returns a fresh pointer to *t, or nil when t is nil.
// Every source builder that accepts a *time.Time from a client must
// call this before storing it on a [lastused.LastUsedSource] so callers
// cannot mutate a source's Value through their own retained pointer.
// Exposed for use by per-kind composers that hand-roll their own source
// builders for kind-specific clients (ECR, AMI, access logs, etc.).
func CopyTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}
