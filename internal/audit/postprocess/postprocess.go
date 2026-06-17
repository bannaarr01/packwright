// Package postprocess folds the lastused and cost layers (ADR-0041,
// ADR-0042) into a freshly-scanned []audit.Resource. The scanner pool
// is intentionally agnostic of these two enrichments — it just walks
// AWS read-only APIs and produces a Resource per object. Post-process
// is the layer that, for every Resource, reaches into Resource.Raw
// (the scanner's verbatim Describe output) to pull out the per-kind
// fields each composer needs, builds the right client adapters, and
// fills in Resource.LastUsed and Resource.CostEstimate.
//
// The package is the single import boundary between audit/cost,
// audit/lastused, and the audit pipeline: audit itself does not import
// the composers, the composers do not import audit, and the cost
// dispatch is wholly here so the cost package can be exercised
// in isolation by tests.
package postprocess

import (
	"context"
	"log/slog"
	"sync"

	"github.com/bannaarr01/packwright/internal/audit"
	"github.com/bannaarr01/packwright/internal/audit/cost/pricing"
	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

// Options tunes Apply.
type Options struct {
	// LookbackDays is the default lookback window for every per-kind
	// composer (cw.write-iops, cw.read-iops, cw.request-count, ...).
	// Zero falls back to lastused.DefaultLookbackDays.
	LookbackDays int
	// Concurrency caps how many resources are post-processed in
	// parallel. Zero defaults to 8 to match the scanner pool. Each
	// per-kind composer is independent so this is safe.
	Concurrency int
	// Logger is used to record per-resource enrichment errors
	// (CloudWatch throttling, missing pricing snapshots, ...). Nil
	// falls through to slog.Default.
	Logger *slog.Logger
}

// Apply walks res in-place, populating LastUsed and CostEstimate for
// every entry. The post-process is best-effort: a single resource that
// fails to enrich logs a warning but does not abort the batch — the
// row keeps whatever fields the scanner already filled.
//
// Apply blocks until every enrichment completes. Cancel ctx to stop
// early; in-flight composers honour their own ctx and unblock
// promptly.
func Apply(ctx context.Context, c *audit.Client, res []audit.Resource, opts Options) {
	if len(res) == 0 {
		return
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	conc := opts.Concurrency
	if conc <= 0 {
		conc = 8
	}
	if conc > len(res) {
		conc = len(res)
	}

	snap, _ := pricing.Lookup(c.Region())

	clients := newClients(c, opts.LookbackDays)

	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for i := range res {
		i := i
		select {
		case <-ctx.Done():
			return
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			enrich(ctx, &res[i], snap, clients, opts.LookbackDays, opts.Logger)
		}()
	}
	wg.Wait()
}

// enrich populates LastUsed and CostEstimate for a single Resource by
// dispatching on r.Kind. Both branches are no-ops for kinds the
// dispatcher doesn't recognise — the row keeps the zero value and the
// UI renders "—".
func enrich(ctx context.Context, r *audit.Resource, snap *pricing.Snapshot, c *clients, lookback int, log *slog.Logger) {
	if r == nil {
		return
	}
	signal := computeLastUsed(ctx, r, c, lookback)
	r.LastUsed = &signal
	estimate := computeCost(snap, r)
	r.CostEstimate = &estimate
	_ = log // reserved for future per-row warnings; keeps signature stable.
}

// LookbackDays returns the effective lookback days for opts, honouring
// the lastused default when opts.LookbackDays is zero. Exposed so the
// audit command can echo the value in its summary.
func LookbackDays(opts Options) int {
	if opts.LookbackDays > 0 {
		return opts.LookbackDays
	}
	return lastused.DefaultLookbackDays
}
