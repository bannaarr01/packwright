package audit

import (
	"context"
	"sync"
	"time"
)

// Bucket is a token bucket that paces calls to a single AWS service so
// `/audit` does not exhaust the per-service request budget on a busy
// account. Each bucket carries a steady-state rate (tokens per second)
// and a burst (the maximum number of tokens it will hold while idle).
//
// Bucket is safe for concurrent use by multiple goroutines.
type Bucket struct {
	mu       sync.Mutex
	rate     float64
	burst    float64
	tokens   float64
	lastFill time.Time
	now      func() time.Time // overridable in tests
}

// NewBucket constructs a Bucket that refills at rate tokens per second up
// to a burst-size cap. A rate of 0 disables throttling — Wait returns
// immediately on every call (useful for tests and explicit opt-outs).
//
// The bucket starts full so the first burst of scanners runs without
// waiting; sustained load then converges on rate.
func NewBucket(rate float64, burst int) *Bucket {
	b := &Bucket{
		rate:  rate,
		burst: float64(burst),
		now:   time.Now,
	}
	b.tokens = b.burst
	b.lastFill = b.now()
	return b
}

// Wait consumes one token, blocking until one becomes available or ctx
// is cancelled. A nil or rate-zero bucket is a no-op that returns nil
// — callers' own ctx-aware code paths catch cancellation.
//
// On context cancellation Wait returns ctx.Err() without consuming a
// token, so callers can treat it like any other cancellable AWS call.
func (b *Bucket) Wait(ctx context.Context) error {
	if b == nil || b.rate <= 0 {
		return nil
	}
	for {
		b.mu.Lock()
		b.refill()
		if b.tokens >= 1 {
			b.tokens--
			b.mu.Unlock()
			return nil
		}
		need := 1 - b.tokens
		delay := time.Duration(need / b.rate * float64(time.Second))
		b.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
}

// refill credits tokens accumulated since the last call. Caller holds b.mu.
func (b *Bucket) refill() {
	now := b.now()
	elapsed := now.Sub(b.lastFill).Seconds()
	if elapsed <= 0 {
		return
	}
	b.tokens += elapsed * b.rate
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.lastFill = now
}

// DefaultRate is the per-service token-bucket rate the pool uses when a
// scanner does not declare an explicit override. 5 req/s with a burst of
// 10 is the ADR-0040 default — gentle enough that a busy account does
// not trip API limits, generous enough that a small one finishes fast.
const (
	DefaultRate  = 5.0
	DefaultBurst = 10
)
