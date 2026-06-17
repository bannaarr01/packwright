package cache

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// KindScanFunc runs a single-kind scan and returns the resources for
// that kind only.
type KindScanFunc func(ctx context.Context, kind string) ([]Resource, error)

// RefreshKind re-scans a single resource kind and merges the result
// into the existing snapshot, leaving every other kind's data untouched.
// The per-kind timestamp (KindScannedAt[kind]) is updated to "now"; the
// top-level ScannedAt — i.e. the time of the most recent *full* scan —
// is deliberately left alone so the stale banner remains honest after
// partial refreshes.
//
// RefreshKind requires an existing snapshot to merge into: if no
// baseline exists, ErrNoSnapshot is returned. The 60-second full-scan
// throttle does not apply to partial refresh because per-kind scans
// are much cheaper.
func (s *Store) RefreshKind(ctx context.Context, k Key, kind string, scan KindScanFunc) (*Snapshot, error) {
	if err := k.Validate(); err != nil {
		return nil, err
	}
	if kind == "" {
		return nil, errors.New("cache: refresh kind: kind is empty")
	}
	if scan == nil {
		return nil, errors.New("cache: refresh kind: scan func is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	snap, err := s.readLocked(k)
	if err != nil {
		return nil, err
	}

	fresh, err := scan(ctx, kind)
	if err != nil {
		return nil, fmt.Errorf("cache: scan kind %q: %w", kind, err)
	}

	snap.Resources = mergeKind(snap.Resources, kind, fresh)
	if snap.KindScannedAt == nil {
		snap.KindScannedAt = make(map[string]time.Time)
	}
	snap.KindScannedAt[kind] = s.now()
	snap.ScannersRun = appendUnique(snap.ScannersRun, kind)
	// A successful re-scan clears any prior skip record for this kind:
	// the user just got a fresh signal that the scanner ran cleanly.
	snap.ScannersSkipped = removeSkipped(snap.ScannersSkipped, kind)

	if err := s.writeLocked(k, snap); err != nil {
		return nil, err
	}
	return snap, nil
}

// mergeKind returns a new slice containing every resource in existing
// whose Kind does not equal kind, followed by every resource in fresh.
// Order is otherwise preserved so the on-disk JSON is stable across
// repeated partial refreshes of unrelated kinds.
func mergeKind(existing []Resource, kind string, fresh []Resource) []Resource {
	merged := make([]Resource, 0, len(existing)+len(fresh))
	for _, r := range existing {
		if r.Kind != kind {
			merged = append(merged, r)
		}
	}
	merged = append(merged, fresh...)
	return merged
}

// appendUnique appends v to xs unless v is already present, preserving
// existing order.
func appendUnique(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	return append(xs, v)
}

// removeSkipped returns xs with every entry whose Kind equals kind
// removed.
func removeSkipped(xs []SkippedScanner, kind string) []SkippedScanner {
	if len(xs) == 0 {
		return xs
	}
	out := make([]SkippedScanner, 0, len(xs))
	for _, x := range xs {
		if x.Kind != kind {
			out = append(out, x)
		}
	}
	return out
}
