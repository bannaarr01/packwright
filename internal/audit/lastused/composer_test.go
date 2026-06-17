package lastused

import (
	"testing"
	"time"
)

// helper: build a time pointer for table tests.
func tp(t time.Time) *time.Time { return &t }

// alwaysHigh is a ConfidenceFunc that returns High when at least one
// source has a value, and Unknown otherwise. Used by tests that want to
// observe the disagreement detector's decrement in isolation.
func alwaysHigh(sources []LastUsedSource, _ time.Time, _ time.Time) (Confidence, string) {
	for _, s := range sources {
		if s.HasValue() {
			return High, ""
		}
	}
	return Unknown, ""
}

func TestCompose_RecentBest(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-1 * time.Hour)
	older := now.Add(-2 * 24 * time.Hour)

	sig := Compose([]LastUsedSource{
		{Name: "create", Value: tp(older), Cost: 0},
		{Name: "cw.cpu", Value: tp(recent), LookbackDays: 30, Cost: 1},
	}, alwaysHigh, now)

	if !sig.Best.Equal(recent) {
		t.Fatalf("Best = %s, want %s", sig.Best, recent)
	}
	if sig.Confidence != High {
		t.Fatalf("Confidence = %s, want high", sig.Confidence)
	}
	if sig.Notes != "" {
		t.Fatalf("Notes = %q, want empty", sig.Notes)
	}
	if sig.Cost() != 1 {
		t.Fatalf("Cost = %d, want 1", sig.Cost())
	}
}

func TestCompose_DisagreementLowersConfidence(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-1 * time.Hour)
	stale := now.Add(-60 * 24 * time.Hour) // 60d gap from recent

	sig := Compose([]LastUsedSource{
		{Name: "attach", Value: tp(recent)},
		{Name: "cw.io", Value: tp(stale), LookbackDays: 30},
	}, alwaysHigh, now)

	if !sig.Best.Equal(recent) {
		t.Fatalf("Best = %s, want %s", sig.Best, recent)
	}
	if sig.Confidence != Medium {
		t.Fatalf("Confidence = %s, want medium (High decremented)", sig.Confidence)
	}
	if sig.Notes == "" {
		t.Fatalf("Notes empty; want disagreement explanation")
	}
}

func TestCompose_AllNilSourcesIsUnknown(t *testing.T) {
	t.Parallel()

	now := time.Now()
	sig := Compose([]LastUsedSource{
		{Name: "cw.cpu", LookbackDays: 30},
		{Name: "cw.io", LookbackDays: 30},
	}, alwaysHigh, now)

	if !sig.Best.IsZero() {
		t.Fatalf("Best = %s, want zero", sig.Best)
	}
	if sig.Confidence != Unknown {
		t.Fatalf("Confidence = %s, want unknown", sig.Confidence)
	}
}

func TestCompose_DisagreementBelowThresholdNoNote(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	a := now.Add(-1 * time.Hour)
	b := now.Add(-20 * 24 * time.Hour) // 20d < 30d threshold

	sig := Compose([]LastUsedSource{
		{Name: "a", Value: tp(a)},
		{Name: "b", Value: tp(b)},
	}, alwaysHigh, now)

	if sig.Confidence != High {
		t.Fatalf("Confidence = %s, want high (no decrement)", sig.Confidence)
	}
	if sig.Notes != "" {
		t.Fatalf("Notes = %q, want empty", sig.Notes)
	}
}

func TestCompose_DisagreementOnlyOneSourceNoChange(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	sig := Compose([]LastUsedSource{
		{Name: "a", Value: tp(now.Add(-100 * 24 * time.Hour))},
		{Name: "b"}, // nil — should not participate in disagreement math
	}, alwaysHigh, now)

	if sig.Confidence != High {
		t.Fatalf("Confidence = %s, want high (single value can't disagree)", sig.Confidence)
	}
	if sig.Notes != "" {
		t.Fatalf("Notes = %q, want empty", sig.Notes)
	}
}

func TestCompose_DisagreementCannotDecrementBelowUnknown(t *testing.T) {
	t.Parallel()

	// Force the per-kind rule to return Unknown despite values being
	// present. The disagreement detector must not wrap below Unknown.
	zeroRule := func(_ []LastUsedSource, _ time.Time, _ time.Time) (Confidence, string) {
		return Unknown, "kind rule unknown"
	}
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	sig := Compose([]LastUsedSource{
		{Name: "a", Value: tp(now.Add(-1 * time.Hour))},
		{Name: "b", Value: tp(now.Add(-60 * 24 * time.Hour))},
	}, zeroRule, now)

	if sig.Confidence != Unknown {
		t.Fatalf("Confidence = %s, want unknown (no underflow)", sig.Confidence)
	}
	// The kind-rule note and the disagreement note both land.
	if sig.Notes == "" || sig.Notes == "kind rule unknown" {
		t.Fatalf("Notes = %q, want both kind-rule and disagreement notes", sig.Notes)
	}
}

func TestSourceByName(t *testing.T) {
	t.Parallel()

	now := time.Now()
	ss := []LastUsedSource{
		{Name: "first", Value: tp(now)},
		{Name: "second"},
	}
	if got := SourceByName(ss, "first"); got == nil || got.Name != "first" {
		t.Fatalf("SourceByName(first) = %v, want first", got)
	}
	if got := SourceByName(ss, "missing"); got != nil {
		t.Fatalf("SourceByName(missing) = %v, want nil", got)
	}

	sig := LastUsedSignal{Sources: ss}
	if got := sig.SourceByName("second"); got == nil || got.Name != "second" {
		t.Fatalf("signal.SourceByName(second) = %v, want second", got)
	}
}

func TestWithinAndDays(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	if !Within(now.Add(-5*24*time.Hour), now, Days(7)) {
		t.Fatalf("Within: 5d ago should be within 7d window")
	}
	if Within(now.Add(-8*24*time.Hour), now, Days(7)) {
		t.Fatalf("Within: 8d ago should be outside 7d window")
	}
	if Within(time.Time{}, now, Days(7)) {
		t.Fatalf("Within: zero time must never be 'within'")
	}
}

func TestConfidenceString(t *testing.T) {
	t.Parallel()

	cases := map[Confidence]string{
		Unknown: "unknown",
		Low:     "low",
		Medium:  "medium",
		High:    "high",
	}
	for c, want := range cases {
		if got := c.String(); got != want {
			t.Fatalf("%d.String() = %q, want %q", c, got, want)
		}
	}
}
