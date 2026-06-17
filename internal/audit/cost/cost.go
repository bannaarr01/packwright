// Package cost implements per-resource cost estimation for the /audit
// command (ADR-0042 / MVP-6 PR-03). The package is split in three:
//
//   - cost.go (this file) defines the public types every consumer reads
//     (CostEstimate, CostLine, CostSource, Confidence) and the small
//     helpers per-kind computers share.
//   - pricing/ holds the AWS Pricing API client and the embedded
//     snapshot fallback. Per-kind computers consult pricing first; when
//     it has nothing they emit a zero-cost estimate with Confidence=Low
//     and a note explaining why.
//   - per_kind/ hosts one cost computer per audit Kind() string. Each
//     computer takes a region plus a kind-specific input struct and
//     returns a CostEstimate.
//
// Cost is computed once per scan, from the post-scan loop in
// internal/audit/postprocess. Per-kind computers are pure: they read
// pricing data, do arithmetic, and return a CostEstimate. No AWS calls
// fire here — Cost Explorer is the exception and lives in cost/explorer
// where it is opt-in.
package cost

// Source identifies where a CostEstimate's number came from. ADR-0042
// defines two production sources; "snapshot" is a synonym for
// pricing-api that surfaces when the live API is unreachable and the
// embedded snapshot served the request.
type Source string

const (
	// SourcePricingAPI marks an estimate built from a live Pricing API
	// response (or its in-memory cache).
	SourcePricingAPI Source = "pricing-api"
	// SourceSnapshot marks an estimate built from the embedded pricing
	// snapshot. Confidence is one notch lower than a live response.
	SourceSnapshot Source = "snapshot"
	// SourceCostExplorer marks an estimate built from a Cost Explorer
	// query. Only emitted when audit.cost_explorer.enabled is true.
	SourceCostExplorer Source = "cost-explorer"
	// SourceUnknown marks an estimate the computer could not produce —
	// MonthlyUSD is zero, Confidence is Unknown, Notes explains why.
	SourceUnknown Source = "unknown"
)

// Confidence is the four-level scale the UI renders next to a row's
// MonthlyUSD. Mirrors lastused.Confidence so the two columns can share
// rendering logic.
type Confidence int

// Confidence levels, ordered from least to most certain.
const (
	// Unknown means the computer produced no estimate at all.
	Unknown Confidence = iota
	// Low means the estimate uses a snapshot at least 30 days old, an
	// assumed instance type, or a heuristic over CloudWatch data.
	Low
	// Medium means the estimate uses a fresh snapshot or an indirect
	// signal (e.g. EBS storage but no IOPS / throughput).
	Medium
	// High means the estimate uses live Pricing API data and every
	// component (instance hours, storage, IOPS) was directly observed.
	High
)

// String returns the lowercase label for c. Out-of-range values render
// as "unknown" so logs never panic.
func (c Confidence) String() string {
	switch c {
	case High:
		return "high"
	case Medium:
		return "medium"
	case Low:
		return "low"
	default:
		return "unknown"
	}
}

// CostEstimate is the per-resource cost summary populated by ADR-0042.
// The audit pipeline attaches one to every Resource the scanner pool
// produces; rows with Source=SourceUnknown render in the UI without a
// dollar amount and explain why in Notes.
type CostEstimate struct {
	// MonthlyUSD is the on-demand monthly cost estimate in US dollars.
	// Always positive; zero only when Source is SourceUnknown or the
	// resource genuinely costs nothing (a tagged but unused EIP that
	// has been associated to a running instance, for example).
	MonthlyUSD float64
	// Currency is always "USD" in v1 — the field is present so v2 can
	// surface multi-currency Cost Explorer queries without a schema
	// migration. Empty when MonthlyUSD is zero.
	Currency string
	// Source identifies where the MonthlyUSD value came from.
	Source Source
	// Confidence reports how strongly MonthlyUSD should be trusted.
	Confidence Confidence
	// Breakdown is the per-component decomposition: an EBS volume
	// might have "gp3 storage 10 GB" and "gp3 IOPS 3000 baseline" as
	// separate lines, summing to MonthlyUSD. Empty when the cost is
	// a single line item (e.g. a fixed-hourly NAT gateway).
	Breakdown []CostLine
	// Notes is the computer's human-readable explanation: an "assumed
	// instance type" caveat, the snapshot age, why Confidence dropped
	// a level. Empty when nothing surprising happened.
	Notes string
}

// IsZero reports whether the estimate carries no dollar amount the UI
// should render — either the literal zero value or an Unavailable
// placeholder. The UI renders "—" in both cases instead of "$0.00".
func (e CostEstimate) IsZero() bool {
	if e.MonthlyUSD > 0 {
		return false
	}
	if e.Source == SourceUnknown {
		return true
	}
	return e.Source == "" && len(e.Breakdown) == 0
}

// CostLine is one component of a CostEstimate's Breakdown. Each line is
// a human-readable description with the monthly subtotal in USD.
type CostLine struct {
	// Component is the name of the resource component, e.g.
	// "EBS gp3 storage", "RDS db.t3.medium hours", "S3 Standard".
	Component string
	// Amount is the human-readable usage line, e.g.
	// "10 GB × $0.10/GB/month" or "730h × $0.0833/h".
	Amount string
	// MonthlyUSD is the monthly subtotal for this line. The sum of
	// every line's MonthlyUSD equals CostEstimate.MonthlyUSD.
	MonthlyUSD float64
}

// Unavailable returns a CostEstimate with Source=SourceUnknown and the
// supplied note. Per-kind computers return this when no pricing data is
// available for the resource's region / kind / instance type.
func Unavailable(note string) CostEstimate {
	return CostEstimate{
		Source:     SourceUnknown,
		Confidence: Unknown,
		Notes:      note,
	}
}

// Sum returns a CostEstimate whose MonthlyUSD is the sum of every
// line's MonthlyUSD, with the supplied source, confidence, and notes.
// Per-kind computers build their Breakdown first, then call Sum to
// fold it into the final estimate.
func Sum(source Source, confidence Confidence, notes string, lines []CostLine) CostEstimate {
	total := 0.0
	for _, l := range lines {
		total += l.MonthlyUSD
	}
	currency := ""
	if total > 0 {
		currency = "USD"
	}
	return CostEstimate{
		MonthlyUSD: total,
		Currency:   currency,
		Source:     source,
		Confidence: confidence,
		Breakdown:  lines,
		Notes:      notes,
	}
}

// HoursPerMonth is the constant Packwright uses to convert hourly AWS
// prices to monthly USD. AWS itself uses 730 (24×365÷12) for its on-
// demand pricing pages; matching that keeps Packwright's number within
// the ±10% target ADR-0042 sets.
const HoursPerMonth = 730.0
