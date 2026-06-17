package lastused

import (
	"fmt"
	"time"
)

// ConfidenceFunc is the per-kind judgement that turns a slice of source
// readings into a Confidence level plus an optional human-readable note.
// best is the maximum non-nil timestamp across sources (zero when every
// source returned nil); now is the reference time used for "within N
// days" comparisons.
//
// The note returned here is the composer's own explanation; the universal
// disagreement detector in [Compose] may append its own note afterwards.
type ConfidenceFunc func(sources []LastUsedSource, best, now time.Time) (Confidence, string)

// Compose builds a [LastUsedSignal] from sources: Best is the most-recent
// non-nil timestamp; Confidence is whatever rule returns; and a universal
// "signals disagree by >DisagreementThresholdDays" detector runs last —
// when the spread between the oldest and newest non-nil source exceeds
// the threshold, Confidence is lowered by one level (clamped at Unknown)
// and a note is appended.
//
// Sources that returned nil values still appear in the resulting Sources
// slice; they just contribute nothing to Best or the disagreement
// detector.
func Compose(sources []LastUsedSource, rule ConfidenceFunc, now time.Time) LastUsedSignal {
	s := LastUsedSignal{Sources: sources}
	best, ok := bestTime(sources)
	if ok {
		s.Best = best
	}
	if rule != nil {
		conf, note := rule(sources, s.Best, now)
		s.Confidence = conf
		s.Notes = note
	}
	if spread, divergent := disagreement(sources); divergent {
		if s.Confidence > Unknown {
			s.Confidence--
		}
		s.Notes = appendNote(s.Notes, fmt.Sprintf(
			"Signals disagree by %dd — confidence lowered.",
			int(spread.Hours()/24),
		))
	}
	return s
}

// bestTime returns the most-recent non-nil timestamp in sources. ok is
// false when every source returned nil.
func bestTime(sources []LastUsedSource) (time.Time, bool) {
	var best time.Time
	found := false
	for _, src := range sources {
		if src.Value == nil {
			continue
		}
		if !found || src.Value.After(best) {
			best = *src.Value
			found = true
		}
	}
	return best, found
}

// disagreement reports whether the spread between the oldest and newest
// non-nil sources exceeds [DisagreementThresholdDays]. At least two
// non-nil sources are required; one source can never disagree with
// itself.
func disagreement(sources []LastUsedSource) (time.Duration, bool) {
	var oldest, newest time.Time
	count := 0
	for _, src := range sources {
		if src.Value == nil {
			continue
		}
		t := *src.Value
		if count == 0 || t.Before(oldest) {
			oldest = t
		}
		if count == 0 || t.After(newest) {
			newest = t
		}
		count++
	}
	if count < 2 {
		return 0, false
	}
	spread := newest.Sub(oldest)
	return spread, spread > time.Duration(DisagreementThresholdDays)*24*time.Hour
}

// appendNote joins existing and additional notes with a "; " separator.
// Empty inputs are skipped.
func appendNote(existing, addition string) string {
	switch {
	case existing == "":
		return addition
	case addition == "":
		return existing
	default:
		return existing + "; " + addition
	}
}

// Within reports whether t falls within the last d of now and is non-zero.
// Used by per-kind confidence rules to ask "did a signal land recently?"
func Within(t, now time.Time, d time.Duration) bool {
	if t.IsZero() {
		return false
	}
	return !t.Before(now.Add(-d))
}

// Days returns a Duration of n days for readability at call sites.
func Days(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }
