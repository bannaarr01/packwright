package audit

import (
	"context"
	"testing"
	"time"
)

// TestBucketAllowsBurst proves a freshly-constructed bucket lets the
// caller spend its full burst without waiting. With burst=3 the third
// Wait should still return immediately.
func TestBucketAllowsBurst(t *testing.T) {
	b := NewBucket(1, 3)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	for i := 0; i < 3; i++ {
		if err := b.Wait(ctx); err != nil {
			t.Fatalf("Wait #%d: %v", i, err)
		}
	}
}

// TestBucketRefillsOverTime feeds the bucket fake-clock advances so the
// test does not actually sleep. After spending the burst, advancing the
// clock by 1s with rate=1 should make one more token available.
func TestBucketRefillsOverTime(t *testing.T) {
	now := time.Unix(0, 0)
	b := NewBucket(1, 1)
	b.now = func() time.Time { return now }
	b.lastFill = now
	b.tokens = 1

	// First call consumes the lone token.
	ctx := context.Background()
	if err := b.Wait(ctx); err != nil {
		t.Fatalf("first Wait: %v", err)
	}

	// Without advancing the clock the bucket is empty; a zero-deadline
	// context should refuse to wait.
	deadCtx, cancel := context.WithDeadline(ctx, now)
	defer cancel()
	if err := b.Wait(deadCtx); err == nil {
		t.Fatal("Wait succeeded on an empty bucket without a clock advance")
	}

	// Advance one second; one token should be back.
	now = now.Add(time.Second)
	if err := b.Wait(ctx); err != nil {
		t.Fatalf("Wait after refill: %v", err)
	}
}

// TestBucketWaitRespectsCancellation cancels the context while a Wait
// is parked on an empty bucket and asserts the call returns ctx.Err()
// promptly.
func TestBucketWaitRespectsCancellation(t *testing.T) {
	b := NewBucket(1, 1)
	// Drain the single token.
	if err := b.Wait(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Wait(ctx); err == nil {
		t.Fatal("Wait did not honour a cancelled context")
	}
}

// TestBucketZeroRateIsNoop asserts that a rate of zero short-circuits
// to immediate return regardless of token state.
func TestBucketZeroRateIsNoop(t *testing.T) {
	b := NewBucket(0, 0)
	if err := b.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

// TestNilBucketIsNoop guards the scanner code path that fetches a
// throttle by name — a missing entry returns nil and the scanner calls
// Wait on it; the call should not panic.
func TestNilBucketIsNoop(t *testing.T) {
	var b *Bucket
	if err := b.Wait(context.Background()); err != nil {
		t.Fatalf("nil bucket Wait: %v", err)
	}
}
