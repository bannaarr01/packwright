package monitor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bannaarr01/packwright/internal/monitorx"
)

// Update is one panel-refresh event streamed back from Result.Updates. The
// engine emits an Update per panel per tick, regardless of whether the
// refresh succeeded — on failure, Err is set and Data is nil. Renderers
// treat an Update with non-nil Err as an in-place error card (ADR-0015).
type Update struct {
	// PanelID is the manifest-declared id of the panel that produced this
	// update.
	PanelID string
	// Kind is the panel kind, e.g. "cloudwatch/metric". Carrying it on
	// the update saves the renderer from looking it up.
	Kind string
	// Time is the wall-clock time the refresh completed.
	Time time.Time
	// Data is the typed payload (SeriesData / LogLinesData / HealthData).
	// nil iff Err is non-nil.
	Data monitorx.PanelData
	// Err is the per-panel refresh error, if any. Per-panel errors do not
	// stop the dashboard; the next tick will retry the panel.
	Err error
}

// Result is what Run returns once the dashboard loop is alive. The caller
// consumes Updates until the channel closes (or until they're done with the
// dashboard, then calls Stop) and calls Wait to learn how the loop
// terminated. Wait returns ctx.Err() — typically context.Canceled when
// Stop was called.
//
// Stop and Wait do not require Updates to be drained first: panel
// goroutines blocked on a full channel honour parent-context cancellation,
// so they unblock and exit when Stop fires. Any items buffered before
// Stop remain readable through Updates after Wait returns.
type Result struct {
	// Updates is the stream of per-panel refresh events. Closed by the
	// engine after Stop is called (or the parent ctx is cancelled) and
	// every in-flight refresh has returned.
	Updates <-chan Update
	stop    func()
	wait    func() error
}

// Stop cancels the dashboard's context. Idempotent. Stop returns
// immediately; the loop's goroutines drain asynchronously and Wait blocks
// until they're gone.
func (r *Result) Stop() { r.stop() }

// Wait blocks until the dashboard loop has fully torn down. Safe to call
// multiple times; every call returns the same error. Parent-context cancel
// is reported as ctx.Err() (typically context.Canceled), not as a panel
// error.
func (r *Result) Wait() error { return r.wait() }

// Run starts a dashboard loop for spec and returns a streaming Result. The
// loop:
//
//  1. builds every panel through monitorx.Build (build failures surface as
//     a one-shot Update with Err set, and the panel is dropped from the
//     schedule);
//  2. fires the first tick immediately so the UI is never blank;
//  3. ticks at spec.RefreshEvery, fanning each tick out to every panel in
//     its own goroutine — a slow panel cannot block sibling panels;
//  4. exits when ctx is cancelled or Stop is called.
//
// Each tick uses a refresh budget equal to the dashboard's RefreshEvery
// (so a stuck panel cannot still be running when the next tick fires).
// Cancellation propagates: when the parent context dies, every in-flight
// Refresh sees ctx.Err() and returns promptly.
func (r *Runner) Run(parent context.Context, spec *Spec, _ Inputs) (*Result, error) {
	if spec == nil {
		return nil, errors.New("monitor: Run called with nil spec")
	}
	if len(spec.Panels) == 0 {
		return nil, errors.New("monitor: spec has no panels")
	}
	interval := spec.RefreshEvery
	if interval <= 0 {
		interval = DefaultRefreshEvery
	}

	ctx, cancel := context.WithCancel(parent)
	// Buffer = panels * 2 so one full tick of live refreshes plus any
	// synchronous build-error Updates pre-written before the loop starts
	// always fit without blocking. Consumers that fall further behind
	// back-pressure the next tick's goroutines on the channel send; they
	// can still unblock via <-ctx.Done() on shutdown.
	updates := make(chan Update, len(spec.Panels)*2)

	// Build panels up-front. A panel that fails to build never enters the
	// schedule, but we emit one Update so the renderer paints its error
	// card on first paint.
	scheduled := make([]scheduledPanel, 0, len(spec.Panels))
	now := r.deps.Now()
	for i, ps := range spec.Panels {
		p, err := monitorx.Build(ps.Kind, ps.Spec)
		if err != nil {
			updates <- Update{PanelID: ps.ID, Kind: ps.Kind, Time: now, Err: err}
			r.deps.Log.Warn("monitor: panel failed to build",
				"index", i, "id", ps.ID, "kind", ps.Kind, "err", err)
			continue
		}
		scheduled = append(scheduled, scheduledPanel{id: ps.ID, kind: ps.Kind, panel: p})
	}

	done := make(chan error, 1)

	go r.loop(ctx, interval, scheduled, updates, done)

	stopOnce := sync.OnceFunc(cancel)
	waitOnce := sync.OnceValue(func() error { return <-done })

	return &Result{
		Updates: updates,
		stop:    stopOnce,
		wait:    waitOnce,
	}, nil
}

// loop owns the dashboard's lifetime: it fires the initial tick, ranges on
// the ticker until ctx is cancelled, and waits for every fan-out goroutine
// to drain before closing updates and reporting the terminal error on done.
func (r *Runner) loop(
	ctx context.Context,
	interval time.Duration,
	scheduled []scheduledPanel,
	updates chan<- Update,
	done chan<- error,
) {
	// allActive tracks every goroutine the loop spawns — panel
	// refreshers and per-tick watchers alike — so we can drain cleanly
	// on shutdown before closing updates.
	var allActive sync.WaitGroup

	defer func() {
		allActive.Wait()
		close(updates)
		done <- ctx.Err()
	}()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial tick so the UI is never blank.
	r.fanOut(ctx, interval, scheduled, updates, &allActive)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.fanOut(ctx, interval, scheduled, updates, &allActive)
		}
	}
}

// fanOut spawns one goroutine per panel for a single tick. Each goroutine
// runs the panel's Refresh under tickCtx (a context that cancels either
// when its tick's budget expires or when the parent dashboard is stopped)
// and forwards the result onto updates. A slow panel only ever blocks its
// own tickCtx; it does not gate sibling panels.
func (r *Runner) fanOut(
	parent context.Context,
	budget time.Duration,
	scheduled []scheduledPanel,
	updates chan<- Update,
	all *sync.WaitGroup,
) {
	tickCtx, tickCancel := context.WithTimeout(parent, budget)

	var tick sync.WaitGroup
	for _, sp := range scheduled {
		tick.Add(1)
		all.Add(1)
		go func(sp scheduledPanel) {
			defer tick.Done()
			defer all.Done()
			data, err := sp.panel.Refresh(tickCtx, r.deps)
			u := Update{
				PanelID: sp.id,
				Kind:    sp.kind,
				Time:    r.deps.Now(),
				Err:     err,
			}
			if err == nil {
				u.Data = data
			} else {
				r.deps.Log.Debug("monitor: panel refresh failed",
					"id", sp.id, "kind", sp.kind, "err", err)
			}
			select {
			case updates <- u:
			case <-parent.Done():
			}
		}(sp)
	}

	// Release tickCtx's resources promptly once this tick's panels are
	// done; running in its own goroutine keeps the main loop unblocked.
	all.Add(1)
	go func() {
		defer all.Done()
		tick.Wait()
		tickCancel()
	}()
}

// scheduledPanel pairs a built panel with the manifest metadata the engine
// stamps onto every Update for it.
type scheduledPanel struct {
	id    string
	kind  string
	panel monitorx.Panel
}
