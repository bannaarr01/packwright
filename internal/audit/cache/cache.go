package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bannaarr01/packwright/internal/audit/cost"
	"github.com/bannaarr01/packwright/internal/audit/lastused"
)

// SchemaVersion is the snapshot file format version this build understands.
// A snapshot on disk whose version field differs is treated as legacy: the
// file is renamed to "*.legacy.json" on next read so the user can inspect
// what changed, and the caller is asked to re-scan.
//
// v1 → v2: added LastUsed and CostEstimate fields to Resource (ADR-0041,
// ADR-0042). Snapshots written by v1 builds are archived to *.legacy.json
// and a fresh scan is forced when a v2 build first opens the cache.
const SchemaVersion = 2

// Defaults.
const (
	// DefaultTTL is the lifetime after which a snapshot surfaces as stale
	// in the UI (yellow "Inventory last refreshed N hours ago" banner).
	DefaultTTL = 24 * time.Hour
	// DefaultThrottle is the minimum interval the cache enforces between
	// two full scans for the same key.
	DefaultThrottle = 60 * time.Second
)

// Sentinel errors.
var (
	// ErrNoSnapshot reports that no snapshot exists on disk for the
	// requested key — either it was never written, or it was archived
	// because its schema version no longer matched SchemaVersion.
	ErrNoSnapshot = errors.New("cache: no snapshot for key")
	// ErrThrottled reports that a full scan was refused because the
	// previous scan for the same key completed less than the throttle
	// window ago. Pass RefreshOptions{Force: true} to override.
	ErrThrottled = errors.New("cache: scan refused: throttle window not yet elapsed")
)

// SkippedScanner records a scanner that did not run during a scan, with
// the human-readable reason (e.g. "AccessDenied").
type SkippedScanner struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// Resource is the cache layer's view of a discovered AWS resource. The
// canonical Resource type lives in internal/audit (ADR-0040); the cache
// duplicates the on-disk shape here so it can be implemented in isolation
// from PR-01. Only Kind is interpreted by the cache (for partial refresh);
// every other field is treated as opaque payload.
//
// SchemaVersion v2 added LastUsed and CostEstimate. The pointers are
// optional — a snapshot may omit them when the post-process step was
// skipped (e.g. --skip-enrichment flag) and the cache still round-trips
// cleanly.
type Resource struct {
	Kind         string                   `json:"kind"`
	ID           string                   `json:"id,omitempty"`
	Region       string                   `json:"region,omitempty"`
	Account      string                   `json:"account,omitempty"`
	Name         string                   `json:"name,omitempty"`
	Tags         map[string]string        `json:"tags,omitempty"`
	CreatedAt    time.Time                `json:"created_at,omitempty"`
	State        string                   `json:"state,omitempty"`
	Raw          map[string]any           `json:"raw,omitempty"`
	LastUsed     *lastused.LastUsedSignal `json:"last_used,omitempty"`
	CostEstimate *cost.CostEstimate       `json:"cost_estimate,omitempty"`
}

// Snapshot is the on-disk envelope for an audit scan. The JSON layout is
// frozen by ADR-0044; new fields must keep an explicit "omitempty" so a
// snapshot written by an older build still round-trips through a newer
// reader as long as SchemaVersion has not changed.
type Snapshot struct {
	Version             int                  `json:"version"`
	ScannedAt           time.Time            `json:"scanned_at"`
	KindScannedAt       map[string]time.Time `json:"kind_scanned_at,omitempty"`
	Profile             string               `json:"profile"`
	Account             string               `json:"account,omitempty"`
	Region              string               `json:"region"`
	LookbackDays        int                  `json:"lookback_days"`
	ScannersRun         []string             `json:"scanners_run,omitempty"`
	ScannersSkipped     []SkippedScanner     `json:"scanners_skipped,omitempty"`
	Resources           []Resource           `json:"resources"`
	ScanCostEstimateUSD float64              `json:"scan_cost_estimate_usd,omitempty"`
}

// Config tunes Store behavior. The zero value is valid and applies
// DefaultTTL and DefaultThrottle.
type Config struct {
	// TTL is the snapshot lifetime after which Read marks the result
	// Stale. Zero means DefaultTTL.
	TTL time.Duration
	// KindTTL optionally overrides TTL on a per-kind basis. Looked up by
	// callers (e.g. via TTLFor) when computing per-row staleness.
	KindTTL map[string]time.Duration
	// Throttle is the minimum interval between full scans for the same
	// key. Zero means DefaultThrottle.
	Throttle time.Duration
	// Now lets tests inject a deterministic clock. Zero means time.Now
	// in UTC.
	Now func() time.Time
}

// Store reads and writes audit snapshots beneath a single directory.
// All public methods are safe for concurrent use within one process.
type Store struct {
	dir      string
	ttl      time.Duration
	kindTTL  map[string]time.Duration
	throttle time.Duration
	now      func() time.Time
	mu       sync.Mutex
}

// NewStore creates a Store rooted at dir, creating the directory (mode
// 0o700) if it does not yet exist. The cached snapshots can contain
// account-identifying data so the directory is not world-readable.
func NewStore(dir string, cfg Config) (*Store, error) {
	if dir == "" {
		return nil, errors.New("cache: store dir is empty")
	}
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	throttle := cfg.Throttle
	if throttle <= 0 {
		throttle = DefaultThrottle
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("cache: create store dir %q: %w", dir, err)
	}
	return &Store{
		dir:      dir,
		ttl:      ttl,
		kindTTL:  cfg.KindTTL,
		throttle: throttle,
		now:      now,
	}, nil
}

// Dir reports the directory this Store reads and writes under.
func (s *Store) Dir() string { return s.dir }

// TTL reports the default TTL applied to snapshots.
func (s *Store) TTL() time.Duration { return s.ttl }

// TTLFor reports the effective TTL for kind, falling back to the default
// TTL when no per-kind override is configured.
func (s *Store) TTLFor(kind string) time.Duration {
	if kind != "" {
		if v, ok := s.kindTTL[kind]; ok && v > 0 {
			return v
		}
	}
	return s.ttl
}

// Throttle reports the minimum interval between full scans.
func (s *Store) Throttle() time.Duration { return s.throttle }

// ReadResult is the outcome of a Read call: the deserialized snapshot,
// whether it is older than the TTL (Stale), and how long ago the scan
// completed (Age).
type ReadResult struct {
	Snapshot *Snapshot
	Stale    bool
	Age      time.Duration
}

// Read returns the cached snapshot for k. If no snapshot exists on disk,
// or the snapshot's schema version does not match SchemaVersion (in
// which case the file is renamed to *.legacy.json as a side effect),
// the call returns ErrNoSnapshot. Snapshots older than the TTL are
// returned with Stale=true so the UI can render the refresh banner.
func (s *Store) Read(k Key) (*ReadResult, error) {
	if err := k.Validate(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, err := s.readLocked(k)
	if err != nil {
		return nil, err
	}
	age := s.now().Sub(snap.ScannedAt)
	return &ReadResult{
		Snapshot: snap,
		Stale:    age > s.ttl,
		Age:      age,
	}, nil
}

// ScanResult is what a ScanFunc returns. The cache copies these fields
// into the new Snapshot envelope verbatim.
type ScanResult struct {
	Resources           []Resource
	Account             string
	ScannersRun         []string
	ScannersSkipped     []SkippedScanner
	ScanCostEstimateUSD float64
}

// ScanFunc runs a full audit scan and returns the resources discovered.
// The cache layer treats it as opaque: ScanFunc is responsible for the
// scanner-pool / throttling concerns described in ADR-0040.
type ScanFunc func(ctx context.Context) (ScanResult, error)

// RefreshOptions tunes a Refresh call.
type RefreshOptions struct {
	// Force bypasses the 60-second throttle check. Surfaced in the UI
	// as the "--force" flag on /audit refresh.
	Force bool
}

// Refresh runs a full scan, persists the result atomically, and returns
// the new snapshot. The 60-second throttle (Config.Throttle) is enforced
// against the existing snapshot's scanned_at field — i.e. the previous
// scan's completion time — unless opts.Force is set, in which case the
// scan runs unconditionally.
//
// ErrThrottled is returned when the throttle refuses the scan; the
// existing snapshot is left untouched.
func (s *Store) Refresh(ctx context.Context, k Key, scan ScanFunc, opts RefreshOptions) (*Snapshot, error) {
	if err := k.Validate(); err != nil {
		return nil, err
	}
	if scan == nil {
		return nil, errors.New("cache: refresh: scan func is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if !opts.Force {
		if existing, err := s.readLocked(k); err == nil {
			if age := s.now().Sub(existing.ScannedAt); age >= 0 && age < s.throttle {
				return nil, fmt.Errorf("%w: last scan %s ago (window %s)",
					ErrThrottled, age.Round(time.Second), s.throttle)
			}
		}
		// readLocked may have failed with ErrNoSnapshot (fine — nothing
		// to throttle against) or a filesystem error. We only suppress
		// ErrNoSnapshot; other errors should not silently refuse a scan
		// either, so propagate them.
		// (Note: readLocked returns ErrNoSnapshot for parse failures via
		// the legacy-rename path; that is the intended behavior.)
	}

	result, err := scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("cache: scan failed: %w", err)
	}

	now := s.now()
	kindScanned := make(map[string]time.Time, len(result.ScannersRun))
	for _, kind := range result.ScannersRun {
		kindScanned[kind] = now
	}

	snap := &Snapshot{
		Version:             SchemaVersion,
		ScannedAt:           now,
		KindScannedAt:       kindScanned,
		Profile:             k.Profile,
		Account:             result.Account,
		Region:              k.Region,
		LookbackDays:        k.LookbackDays,
		ScannersRun:         result.ScannersRun,
		ScannersSkipped:     result.ScannersSkipped,
		Resources:           result.Resources,
		ScanCostEstimateUSD: result.ScanCostEstimateUSD,
	}
	if err := s.writeLocked(k, snap); err != nil {
		return nil, err
	}
	return snap, nil
}

// Wipe removes the snapshot file for k, if one exists. The legacy
// archive (if any) is left in place so the user can still inspect it.
func (s *Store) Wipe(k Key) error {
	if err := k.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.path(k)
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cache: wipe %q: %w", p, err)
	}
	return nil
}

// path returns the absolute path of the snapshot file for k.
func (s *Store) path(k Key) string {
	return filepath.Join(s.dir, k.Filename())
}

// legacyPath returns the absolute path of the legacy archive for k.
func (s *Store) legacyPath(k Key) string {
	return filepath.Join(s.dir, k.LegacyFilename())
}

// readLocked reads, parses, and version-checks the snapshot for k.
// Callers must hold s.mu. A schema-version mismatch renames the on-disk
// file to *.legacy.json and returns ErrNoSnapshot so the caller treats
// the snapshot as missing and triggers a fresh scan.
func (s *Store) readLocked(k Key) (*Snapshot, error) {
	p := s.path(k)
	raw, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoSnapshot
		}
		return nil, fmt.Errorf("cache: read %q: %w", p, err)
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("cache: parse %q: %w", p, err)
	}
	if snap.Version != SchemaVersion {
		if err := os.Rename(p, s.legacyPath(k)); err != nil {
			return nil, fmt.Errorf("cache: archive legacy snapshot %q: %w", p, err)
		}
		return nil, ErrNoSnapshot
	}
	return &snap, nil
}

// writeLocked serializes snap to the snapshot path for k via the
// write-temp-then-rename idiom so a concurrent reader never observes a
// partial file. Callers must hold s.mu.
func (s *Store) writeLocked(k Key, snap *Snapshot) error {
	if snap == nil {
		return errors.New("cache: write: snapshot is nil")
	}
	if snap.Version == 0 {
		snap.Version = SchemaVersion
	}
	raw, err := marshalSnapshot(snap)
	if err != nil {
		return fmt.Errorf("cache: marshal snapshot for %s: %w", k, err)
	}
	p := s.path(k)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("cache: write %q: %w", tmp, err)
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cache: rename %q -> %q: %w", tmp, p, err)
	}
	return nil
}

// marshalSnapshot is the single source of truth for the on-disk JSON
// encoding. encoding/json sorts map keys, so identical Snapshot values
// produce byte-identical output across writes, which the round-trip
// test depends on.
func marshalSnapshot(snap *Snapshot) ([]byte, error) {
	return json.MarshalIndent(snap, "", "  ")
}
