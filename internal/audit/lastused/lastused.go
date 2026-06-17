// Package lastused builds a defensible "when was this resource last used?"
// answer by composing multiple per-service signals.
//
// AWS does not expose a single uniform "last accessed" field across services,
// so PR-02 of MVP-6 follows ADR-0041: every resource kind has its own
// composer that collects two or three signals (a creation timestamp, a
// CloudWatch metric's last non-zero datapoint, an ENI status change, etc.),
// reports Best = max(signal_times), and attaches a Confidence the UI can
// use to decide how prominent the row should be. When the signals diverge
// by more than 30 days the composer lowers Confidence by one level and
// writes a human-readable note explaining the conflict.
//
// This package is deliberately free of AWS SDK imports. Each signal source
// declares a narrow client interface (see [sources] and the per_kind
// composers); production code wires the SDK to those interfaces in a
// separate glue layer, and tests inject fakes.
package lastused

import "time"

// Confidence is the four-level scale the UI renders next to a row's Best
// timestamp. Higher values mean the heuristic is more sure; Unknown is
// reserved for the "we gathered no signals at all" outcome and never
// appears alongside a non-zero Best.
//
// Confidence is ordered so the universal disagreement detector can lower
// it by one step (see [Compose]).
type Confidence int

// The four confidence levels, ordered from least to most certain.
const (
	// Unknown means no signal returned a value; Best is the zero time.
	Unknown Confidence = iota
	// Low means signals exist but the most-recent activity is stale, or
	// the per-kind rule has flagged the resource as a likely orphan.
	Low
	// Medium means we have a credible recent signal but only an indirect
	// one (e.g. an attachment timestamp, not actual IO).
	Medium
	// High means a direct usage signal (request count, IO, connections)
	// was observed recently within the configured lookback.
	High
)

// String returns the canonical lowercase label for c. Unknown values that
// are out of range render as "unknown" so logs never panic.
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

// LastUsedSignal is the per-resource result the UI renders. Best is the
// most-recent timestamp across the consulted Sources; Confidence reports
// how strongly that value should be trusted; Notes carries any human
// readable context the composer wants to surface (e.g. an orphan flag
// or a "signals disagree" explanation).
//
// A LastUsedSignal with zero Sources and zero Best is the "we have no
// idea" outcome — Confidence is Unknown and the UI should treat the row
// as "?".
type LastUsedSignal struct {
	// Best is the most-recent value across Sources. Zero when every
	// source returned nil.
	Best time.Time
	// Confidence is the per-kind judgement after the universal
	// disagreement detector has run.
	Confidence Confidence
	// Sources is every signal we consulted, including those that
	// returned no value. The UI exposes this on row-open.
	Sources []LastUsedSource
	// Notes is the composer's human-readable explanation. Empty when
	// nothing surprising happened.
	Notes string
}

// Cost reports the total AWS API call units the signal cost to gather,
// summed across [LastUsedSource.Cost]. PR-05 surfaces this in the audit
// summary so a user can choose between cheap-and-fast and thorough-and-
// slow scans.
func (s LastUsedSignal) Cost() int {
	total := 0
	for _, src := range s.Sources {
		total += src.Cost
	}
	return total
}

// SourceByName returns the source named n, or nil when no source by that
// name was consulted. Per-kind confidence rules use it to branch on
// whether a specific signal landed.
func (s LastUsedSignal) SourceByName(n string) *LastUsedSource {
	return SourceByName(s.Sources, n)
}

// LastUsedSource is one signal the composer consulted. Value is nil when
// the signal had no datapoints in the lookback window (e.g. zero traffic
// for a NAT gateway); a nil-valued source still appears in
// [LastUsedSignal.Sources] so the UI can show "we tried but found
// nothing."
type LastUsedSource struct {
	// Name is a short identifier the UI can render verbatim, e.g.
	// "ebs.attached", "cw.write-iops", "ecr.image-pushed".
	Name string
	// Value is the timestamp the source produced, or nil when no
	// datapoint was found within the lookback window.
	Value *time.Time
	// LookbackDays is how far back this source looked. Zero when the
	// source does not have a meaningful lookback (e.g. a creation
	// timestamp).
	LookbackDays int
	// Cost is the rough number of AWS API calls this source used.
	// Aggregated by [LastUsedSignal.Cost].
	Cost int
}

// SourceByName returns the source named n from ss, or nil when no source
// by that name is present. Package-level helper used by confidence rules
// before a [LastUsedSignal] has been assembled.
func SourceByName(ss []LastUsedSource, n string) *LastUsedSource {
	for i := range ss {
		if ss[i].Name == n {
			return &ss[i]
		}
	}
	return nil
}

// HasValue reports whether the source returned a non-nil timestamp.
func (s LastUsedSource) HasValue() bool { return s.Value != nil }

// Default configuration values for the audit, shared by sources and
// per-kind composers. Both can be overridden at the call site.
const (
	// DefaultLookbackDays is the default CloudWatch metric lookback
	// window per ADR-0041 (configurable via audit.lookback_days /
	// `/audit --lookback=...`).
	DefaultLookbackDays = 30
	// DisagreementThresholdDays is the spread between the oldest and
	// newest non-nil source values that triggers the universal "signals
	// disagree" detector in [Compose]: when the spread exceeds this
	// threshold, Confidence is lowered by one level and a Note is
	// written.
	DisagreementThresholdDays = 30
)
