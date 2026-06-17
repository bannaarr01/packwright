package cost

import "testing"

// TestSumAggregates verifies that Sum totals every line's MonthlyUSD
// and sets Currency only when the total is positive.
func TestSumAggregates(t *testing.T) {
	lines := []CostLine{
		{Component: "storage", Amount: "10 GB", MonthlyUSD: 1.0},
		{Component: "iops", Amount: "100 IOPS", MonthlyUSD: 0.5},
	}
	got := Sum(SourceSnapshot, Medium, "", lines)
	if got.MonthlyUSD != 1.5 {
		t.Fatalf("MonthlyUSD = %v, want 1.5", got.MonthlyUSD)
	}
	if got.Currency != "USD" {
		t.Fatalf("Currency = %q, want USD", got.Currency)
	}
	if got.Source != SourceSnapshot {
		t.Fatalf("Source = %q, want %q", got.Source, SourceSnapshot)
	}
	if got.Confidence != Medium {
		t.Fatalf("Confidence = %v, want Medium", got.Confidence)
	}
}

// TestUnavailableSetsUnknown verifies Unavailable returns a zero-cost
// estimate flagged as SourceUnknown so the UI renders "—".
func TestUnavailableSetsUnknown(t *testing.T) {
	got := Unavailable("no data")
	if got.Source != SourceUnknown {
		t.Fatalf("Source = %q, want %q", got.Source, SourceUnknown)
	}
	if got.Confidence != Unknown {
		t.Fatalf("Confidence = %v, want Unknown", got.Confidence)
	}
	if got.MonthlyUSD != 0 {
		t.Fatalf("MonthlyUSD = %v, want 0", got.MonthlyUSD)
	}
	if !got.IsZero() {
		t.Fatalf("IsZero = false, want true")
	}
	if got.Notes != "no data" {
		t.Fatalf("Notes = %q, want %q", got.Notes, "no data")
	}
}

// TestConfidenceString sanity-checks the human-readable labels.
func TestConfidenceString(t *testing.T) {
	cases := []struct {
		in   Confidence
		want string
	}{
		{Unknown, "unknown"},
		{Low, "low"},
		{Medium, "medium"},
		{High, "high"},
		{Confidence(99), "unknown"},
	}
	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Errorf("(%d).String() = %q, want %q", c.in, got, c.want)
		}
	}
}
