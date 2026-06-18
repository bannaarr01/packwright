package update

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bannaarr01/packwright/render/cfn"
)

// DefaultOrphanMaxAge is the age threshold ADR-0048 specifies: change sets
// older than 24 hours that Packwright created and never executed are
// candidates for cleanup.
const DefaultOrphanMaxAge = 24 * time.Hour

// DefaultOrphanScanCooldown is the per-process rate limit on orphan scans.
// ADR-0048 calls for "rate-limited" but doesn't pin a value; 6 hours
// matches the cadence of every other launch-time reconciliation work in
// the app and avoids hammering AWS when the user opens many windows.
const DefaultOrphanScanCooldown = 6 * time.Hour

// OrphanCandidate is a Packwright-owned change set the scanner has decided
// is safe to clean up: it carries our name prefix, has not been executed,
// and is older than MaxAge.
type OrphanCandidate struct {
	cfn.ChangeSetSummary
	// Age is Now() − CreationTime at scan time, rounded to a second.
	Age time.Duration
}

// OrphanScanRequest configures one Scan call.
type OrphanScanRequest struct {
	// StackNames lists the stacks whose change sets are enumerated. The
	// scanner reads from each stack independently and merges results.
	StackNames []string
	// MaxAge overrides DefaultOrphanMaxAge when non-zero. Tests pin this
	// to a small duration to keep test fixture timestamps recent.
	MaxAge time.Duration
	// Now overrides time.Now. Tests use it to set a stable reference
	// point; production callers leave it nil.
	Now func() time.Time
}

// OrphanScanResult is the per-call result. Candidates may be empty (the
// healthy case). Errors enumerates the per-stack ListChangeSets errors; a
// partial failure still returns the candidates the scan did manage to
// surface, because cleanup is best-effort.
type OrphanScanResult struct {
	Candidates []OrphanCandidate
	Errors     map[string]error
	// ScannedAt is the time the scan finished. The rate-limiter uses it
	// to gate subsequent calls.
	ScannedAt time.Time
}

// OrphanScanner enumerates and (on demand) cleans up Packwright-owned
// orphan change sets across multiple stacks. The scanner is rate-limited:
// successive scans within Cooldown return the cached result without hitting
// AWS. Tests reset state via ResetCache.
type OrphanScanner struct {
	API      cfn.ChangeSetAPI
	Cooldown time.Duration

	mu        sync.Mutex
	lastScan  OrphanScanResult
	lastAt    time.Time
	cacheHits int
}

// ResetCache wipes the in-process cache used by the scanner's rate limit.
// Tests call this between cases.
func (s *OrphanScanner) ResetCache() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastScan = OrphanScanResult{}
	s.lastAt = time.Time{}
	s.cacheHits = 0
}

// CacheHits returns the number of cached scans served since the last
// network-backed scan. Exported for tests.
func (s *OrphanScanner) CacheHits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cacheHits
}

// Scan enumerates orphan change sets across req.StackNames, applies the
// "packwright-* / un-executed / older than MaxAge" filter, and returns a
// sorted list of candidates. Successive calls within Cooldown return the
// cached result.
func (s *OrphanScanner) Scan(ctx context.Context, req OrphanScanRequest) (OrphanScanResult, error) {
	if s == nil {
		return OrphanScanResult{}, errors.New("update: OrphanScanner is nil")
	}
	if s.API == nil {
		return OrphanScanResult{}, errors.New("update: OrphanScanner.API is nil")
	}

	now := time.Now
	if req.Now != nil {
		now = req.Now
	}
	maxAge := req.MaxAge
	if maxAge <= 0 {
		maxAge = DefaultOrphanMaxAge
	}
	cooldown := s.Cooldown
	if cooldown <= 0 {
		cooldown = DefaultOrphanScanCooldown
	}

	s.mu.Lock()
	if !s.lastAt.IsZero() && now().Sub(s.lastAt) < cooldown {
		s.cacheHits++
		cached := s.lastScan
		s.mu.Unlock()
		return cached, nil
	}
	s.mu.Unlock()

	candidates := []OrphanCandidate{}
	errs := map[string]error{}
	for _, name := range req.StackNames {
		if name == "" {
			continue
		}
		sums, err := cfn.ListChangeSets(ctx, s.API, name)
		if err != nil {
			errs[name] = err
			continue
		}
		for _, sum := range sums {
			if !isOrphanCandidate(sum, now(), maxAge) {
				continue
			}
			candidates = append(candidates, OrphanCandidate{
				ChangeSetSummary: sum,
				Age:              now().Sub(sum.CreationTime).Round(time.Second),
			})
		}
	}

	// Sort by oldest first — that's the cleanup order a UI would walk in,
	// and it's the stable shape tests assert against.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].CreationTime.Before(candidates[j].CreationTime)
	})

	res := OrphanScanResult{
		Candidates: candidates,
		Errors:     errs,
		ScannedAt:  now(),
	}
	s.mu.Lock()
	s.lastScan = res
	s.lastAt = res.ScannedAt
	s.cacheHits = 0
	s.mu.Unlock()
	return res, nil
}

// Cleanup deletes the supplied candidates. It returns a map of
// change-set-id → error for the ones that failed. The healthy path returns
// an empty map. After a successful cleanup the scanner's cache is
// invalidated so the next Scan reflects the post-delete state.
func (s *OrphanScanner) Cleanup(ctx context.Context, candidates []OrphanCandidate) map[string]error {
	if s == nil || s.API == nil {
		return map[string]error{"": errors.New("update: scanner has no API")}
	}
	errs := map[string]error{}
	for _, c := range candidates {
		if err := cfn.DeleteChangeSet(ctx, s.API, c.ChangeSetID, c.StackName); err != nil {
			errs[c.ChangeSetID] = fmt.Errorf("delete %s: %w", c.ChangeSetID, err)
		}
	}
	if len(errs) == 0 {
		s.ResetCache()
	}
	return errs
}

// isOrphanCandidate is the predicate ADR-0048 §"Orphan cleanup" pins down:
// a Packwright-named change set, not yet executed, older than maxAge.
func isOrphanCandidate(sum cfn.ChangeSetSummary, now time.Time, maxAge time.Duration) bool {
	if !cfn.IsPackwrightChangeSet(sum.ChangeSetName) {
		return false
	}
	if sum.CreationTime.IsZero() {
		return false
	}
	if now.Sub(sum.CreationTime) < maxAge {
		return false
	}
	// "Un-executed" means: change set creation succeeded
	// (CREATE_COMPLETE) and the user never ran it
	// (ExecutionStatus AVAILABLE or empty). A change set that already
	// executed cannot be deleted server-side anyway.
	if !isCreateComplete(sum.Status) {
		return false
	}
	if !isUnexecuted(sum.ExecutionStatus) {
		return false
	}
	return true
}

func isCreateComplete(status string) bool {
	switch status {
	case "CREATE_COMPLETE":
		return true
	}
	return false
}

func isUnexecuted(executionStatus string) bool {
	switch executionStatus {
	case "", "AVAILABLE", "UNAVAILABLE":
		return true
	}
	return false
}
