package manifest

import (
	"sync"
	"time"
)

// debouncer coalesces a stream of Change events on a per-Path basis: it
// emits at most one Change for any given path per quiet window, retaining
// the most recent ChangeOp. It is safe for concurrent push / stop calls
// from multiple goroutines.
//
// The implementation uses a per-entry generation counter to absorb the
// classic time.AfterFunc race where Stop returns false because the
// timer's callback is already running. The callback always re-acquires
// the lock and verifies that its generation still matches the entry; a
// stale callback bails without emitting.
//
// stop blocks on a WaitGroup that tracks every in-flight timer callback.
// That guarantee is what lets the owning Watcher safely close its events
// channel during shutdown: no callback can still be inside emit when
// Watcher's loop runs its channel-close defer.
type debouncer struct {
	quiet time.Duration
	emit  func(Change)

	mu      sync.Mutex
	pending map[string]*pendingChange
	stopped bool

	emits sync.WaitGroup
}

// pendingChange tracks the most recent (un-emitted) op for a single path
// along with the timer that will emit it and a generation counter used to
// reject stale callbacks.
type pendingChange struct {
	op    ChangeOp
	timer *time.Timer
	gen   int
}

// newDebouncer constructs a debouncer with the given quiet window. emit is
// invoked from a background goroutine (the timer's callback) without
// holding the debouncer's lock, so it is free to perform blocking I/O.
func newDebouncer(quiet time.Duration, emit func(Change)) *debouncer {
	return &debouncer{
		quiet:   quiet,
		emit:    emit,
		pending: make(map[string]*pendingChange),
	}
}

// push records a raw change and (re)starts the quiet timer for c.Path.
// The most recent ChangeOp wins so a Write-then-Remove burst emits a
// single Remove, and a Remove-then-Create burst emits a single Update.
//
// When quiet is zero (debouncing disabled for tests) push emits inline.
func (d *debouncer) push(c Change) {
	if d.quiet <= 0 {
		d.emit(c)
		return
	}
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	path := c.Path
	p, ok := d.pending[path]
	if ok {
		p.op = c.Op
		// Stop the in-flight timer. If Stop returns true we cancelled
		// the callback before it could fire, so the WaitGroup slot it
		// would have released needs to be released here instead. If
		// Stop returns false the callback is running (or has run) and
		// will release its slot via the deferred Done below; the gen
		// bump invalidates it so a stale emit can never happen.
		if p.timer.Stop() {
			d.emits.Done()
		}
		p.gen++
	} else {
		p = &pendingChange{op: c.Op}
		d.pending[path] = p
	}
	gen := p.gen
	d.emits.Add(1)
	p.timer = time.AfterFunc(d.quiet, func() {
		defer d.emits.Done()
		d.mu.Lock()
		cur, ok := d.pending[path]
		if !ok || cur.gen != gen || d.stopped {
			d.mu.Unlock()
			return
		}
		op := cur.op
		delete(d.pending, path)
		d.mu.Unlock()
		d.emit(Change{Path: path, Op: op})
	})
	d.mu.Unlock()
}

// stop cancels every pending timer, prevents future emits, and waits for
// any callback that was already past the stopped check to finish emitting.
// Used by Watcher.Close to ensure no Change is delivered after the events
// channel is on its way to closing.
func (d *debouncer) stop() {
	d.mu.Lock()
	d.stopped = true
	for _, p := range d.pending {
		if p.timer.Stop() {
			// Cancelled before firing; release the WaitGroup slot
			// the callback would have released. Stop()=false means
			// the callback is in flight and will Done itself.
			d.emits.Done()
		}
	}
	d.pending = nil
	d.mu.Unlock()
	d.emits.Wait()
}
