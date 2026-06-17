package cache

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fixedClock returns a deterministic time function that advances only
// when callers call .add. Tests use this to drive TTL/throttle paths
// without sleeping.
type fixedClock struct {
	atomicNanos atomic.Int64
}

func newFixedClock(t time.Time) *fixedClock {
	c := &fixedClock{}
	c.atomicNanos.Store(t.UTC().UnixNano())
	return c
}

func (c *fixedClock) now() time.Time {
	return time.Unix(0, c.atomicNanos.Load()).UTC()
}

func (c *fixedClock) add(d time.Duration) {
	c.atomicNanos.Add(int64(d))
}

// sampleResources returns a small mix of resources covering two kinds so
// merging behavior is observable.
func sampleResources() []Resource {
	return []Resource{
		{
			Kind:      "ec2/instance",
			ID:        "i-1",
			Region:    "ap-northeast-1",
			Account:   "654654333582",
			Name:      "web-1",
			Tags:      map[string]string{"env": "prod"},
			CreatedAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
			State:     "running",
		},
		{
			Kind:    "ec2/volume",
			ID:      "vol-1",
			Region:  "ap-northeast-1",
			Account: "654654333582",
			State:   "available",
		},
	}
}

func sampleScanResult() ScanResult {
	return ScanResult{
		Resources:           sampleResources(),
		Account:             "654654333582",
		ScannersRun:         []string{"ec2/instance", "ec2/volume"},
		ScannersSkipped:     []SkippedScanner{{Kind: "s3/bucket", Reason: "AccessDenied"}},
		ScanCostEstimateUSD: 0.04,
	}
}

func makeStore(t *testing.T, clock *fixedClock) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir, Config{
		TTL:      24 * time.Hour,
		Throttle: 60 * time.Second,
		Now:      clock.now,
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func sampleKey() Key {
	return Key{Profile: "babe", Region: "ap-northeast-1", LookbackDays: 30}
}

// scanFn returns a ScanFunc that always returns the given result.
func scanFn(r ScanResult) ScanFunc {
	return func(_ context.Context) (ScanResult, error) { return r, nil }
}

// TestKeyFilename pins the on-disk basename so the filesystem layout
// matches ADR-0044.
func TestKeyFilename(t *testing.T) {
	k := sampleKey()
	if got, want := k.Filename(), "babe-ap-northeast-1-30.json"; got != want {
		t.Errorf("Filename(): %q, want %q", got, want)
	}
	if got, want := k.LegacyFilename(), "babe-ap-northeast-1-30.legacy.json"; got != want {
		t.Errorf("LegacyFilename(): %q, want %q", got, want)
	}
}

func TestKeyValidate(t *testing.T) {
	cases := []struct {
		name    string
		key     Key
		wantErr bool
	}{
		{"ok", sampleKey(), false},
		{"empty profile", Key{Profile: "", Region: "r", LookbackDays: 30}, true},
		{"empty region", Key{Profile: "p", Region: "", LookbackDays: 30}, true},
		{"negative lookback", Key{Profile: "p", Region: "r", LookbackDays: -1}, true},
		{"slash in profile", Key{Profile: "a/b", Region: "r", LookbackDays: 30}, true},
		{"slash in region", Key{Profile: "p", Region: "a/b", LookbackDays: 30}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.key.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

// TestRoundTripBytesEqual covers the DoD: a snapshot round-trips cleanly
// (write, read, marshal-again, compare bytes-equal). Because the cache
// is the writer in both round-trip stages and marshalSnapshot is the
// single source of truth for the on-disk encoding, the second pass must
// produce byte-identical output.
func TestRoundTripBytesEqual(t *testing.T) {
	clock := newFixedClock(time.Date(2026, 6, 2, 15, 43, 0, 0, time.UTC))
	s := makeStore(t, clock)
	k := sampleKey()

	if _, err := s.Refresh(context.Background(), k, scanFn(sampleScanResult()), RefreshOptions{}); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}
	first, err := os.ReadFile(s.path(k))
	if err != nil {
		t.Fatalf("read first: %v", err)
	}

	// Round-trip through the store: read the snapshot, then re-marshal
	// it using the exact same encoder.
	res, err := s.Read(k)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	second, err := marshalSnapshot(res.Snapshot)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("snapshot did not round-trip byte-for-byte\nfirst:\n%s\nsecond:\n%s", first, second)
	}

	// And a second Refresh at the same instant (no clock advance) is
	// throttled, so the only way bytes change is via a different
	// snapshot value, not a different encoding pass.
	if _, err := s.Refresh(context.Background(), k, scanFn(sampleScanResult()), RefreshOptions{Force: true}); err != nil {
		t.Fatalf("forced Refresh: %v", err)
	}
	third, err := os.ReadFile(s.path(k))
	if err != nil {
		t.Fatalf("read third: %v", err)
	}
	if !reflect.DeepEqual(first, third) {
		t.Errorf("forced re-write at the same instant changed bytes\nfirst:\n%s\nthird:\n%s", first, third)
	}
}

// TestSnapshotShapeMatchesADR0044 asserts that the on-disk JSON keys
// match the layout ADR-0044 documents. Future schema changes need an
// explicit version bump, which this test forces a contributor to
// consider.
func TestSnapshotShapeMatchesADR0044(t *testing.T) {
	clock := newFixedClock(time.Date(2026, 6, 2, 15, 43, 0, 0, time.UTC))
	s := makeStore(t, clock)
	k := sampleKey()
	if _, err := s.Refresh(context.Background(), k, scanFn(sampleScanResult()), RefreshOptions{}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	raw, err := os.ReadFile(s.path(k))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{
		"version", "scanned_at", "profile", "account",
		"region", "lookback_days", "scanners_run",
		"scanners_skipped", "resources", "scan_cost_estimate_usd",
	} {
		if _, ok := generic[key]; !ok {
			t.Errorf("snapshot missing required key %q", key)
		}
	}
	if v, _ := generic["version"].(float64); int(v) != SchemaVersion {
		t.Errorf("snapshot version = %v, want %d", generic["version"], SchemaVersion)
	}
}

// TestTTLStaleFlag covers TTL + Stale: just before TTL the snapshot is
// fresh; just after TTL it surfaces as Stale.
func TestTTLStaleFlag(t *testing.T) {
	clock := newFixedClock(time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	s, err := NewStore(t.TempDir(), Config{
		TTL:      time.Hour,
		Throttle: 60 * time.Second,
		Now:      clock.now,
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	k := sampleKey()
	if _, err := s.Refresh(context.Background(), k, scanFn(sampleScanResult()), RefreshOptions{}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// Fresh just under TTL.
	clock.add(59 * time.Minute)
	res, err := s.Read(k)
	if err != nil {
		t.Fatalf("Read fresh: %v", err)
	}
	if res.Stale {
		t.Errorf("snapshot marked stale before TTL: age=%s", res.Age)
	}

	// Stale just over TTL.
	clock.add(2 * time.Minute)
	res, err = s.Read(k)
	if err != nil {
		t.Fatalf("Read stale: %v", err)
	}
	if !res.Stale {
		t.Errorf("snapshot not marked stale past TTL: age=%s", res.Age)
	}
}

// TestThrottleRefuses covers the DoD: a 60s-throttled second scan is
// correctly refused, and Force bypasses it.
func TestThrottleRefuses(t *testing.T) {
	clock := newFixedClock(time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	s := makeStore(t, clock)
	k := sampleKey()

	if _, err := s.Refresh(context.Background(), k, scanFn(sampleScanResult()), RefreshOptions{}); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}

	// Within the 60s window: refused.
	clock.add(30 * time.Second)
	scanned := atomic.Int32{}
	throttledScan := func(_ context.Context) (ScanResult, error) {
		scanned.Add(1)
		return sampleScanResult(), nil
	}
	_, err := s.Refresh(context.Background(), k, throttledScan, RefreshOptions{})
	if !errors.Is(err, ErrThrottled) {
		t.Fatalf("refresh within throttle: err=%v, want ErrThrottled", err)
	}
	if scanned.Load() != 0 {
		t.Errorf("scan func invoked while throttled (calls=%d)", scanned.Load())
	}

	// Force bypasses the throttle.
	if _, err := s.Refresh(context.Background(), k, throttledScan, RefreshOptions{Force: true}); err != nil {
		t.Fatalf("forced refresh: %v", err)
	}
	if scanned.Load() != 1 {
		t.Errorf("forced scan func calls=%d, want 1", scanned.Load())
	}

	// Past the 60s window: allowed without Force.
	clock.add(2 * time.Minute)
	if _, err := s.Refresh(context.Background(), k, throttledScan, RefreshOptions{}); err != nil {
		t.Fatalf("refresh past throttle: %v", err)
	}
	if scanned.Load() != 2 {
		t.Errorf("post-throttle scan func calls=%d, want 2", scanned.Load())
	}
}

// TestThrottleNoExistingSnapshot covers the edge case where Refresh is
// called for a key that has no prior snapshot: no throttle window to
// enforce, so the scan must run.
func TestThrottleNoExistingSnapshot(t *testing.T) {
	clock := newFixedClock(time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	s := makeStore(t, clock)
	calls := atomic.Int32{}
	scan := func(_ context.Context) (ScanResult, error) {
		calls.Add(1)
		return sampleScanResult(), nil
	}
	if _, err := s.Refresh(context.Background(), sampleKey(), scan, RefreshOptions{}); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("expected 1 scan call, got %d", calls.Load())
	}
}

// TestSchemaVersionBumpRenamesLegacy covers the DoD: a snapshot whose
// version no longer matches SchemaVersion is renamed to *.legacy.json
// on next read, and the read reports ErrNoSnapshot so the caller
// triggers a fresh scan.
func TestSchemaVersionBumpRenamesLegacy(t *testing.T) {
	clock := newFixedClock(time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	s := makeStore(t, clock)
	k := sampleKey()

	// Write a fake legacy snapshot (version 0) directly to disk so we
	// don't have to manipulate the SchemaVersion constant.
	legacyPayload := map[string]any{
		"version":       SchemaVersion - 1,
		"scanned_at":    clock.now(),
		"profile":       k.Profile,
		"region":        k.Region,
		"lookback_days": k.LookbackDays,
		"resources":     []any{},
	}
	raw, err := json.MarshalIndent(legacyPayload, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy: %v", err)
	}
	if err := os.WriteFile(s.path(k), raw, 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}

	// Read: ErrNoSnapshot + rename to *.legacy.json.
	if _, err := s.Read(k); !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("Read: err=%v, want ErrNoSnapshot", err)
	}
	if _, err := os.Stat(s.path(k)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("snapshot still exists at %q after legacy rename", s.path(k))
	}
	legacyPath := filepath.Join(s.dir, k.LegacyFilename())
	if _, err := os.Stat(legacyPath); err != nil {
		t.Errorf("legacy file missing at %q: %v", legacyPath, err)
	}

	// A subsequent Refresh should succeed (no throttle to enforce —
	// the legacy file is no longer treated as an existing snapshot).
	if _, err := s.Refresh(context.Background(), k, scanFn(sampleScanResult()), RefreshOptions{}); err != nil {
		t.Fatalf("Refresh after legacy rename: %v", err)
	}
	if _, err := os.Stat(s.path(k)); err != nil {
		t.Errorf("fresh snapshot missing after Refresh: %v", err)
	}
}

// TestRefreshKindMerges covers per-kind partial refresh: only resources
// of the named kind are replaced, the top-level ScannedAt is preserved
// (so the stale banner stays honest), and KindScannedAt[kind] is bumped.
func TestRefreshKindMerges(t *testing.T) {
	clock := newFixedClock(time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	s := makeStore(t, clock)
	k := sampleKey()
	if _, err := s.Refresh(context.Background(), k, scanFn(sampleScanResult()), RefreshOptions{}); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}
	beforeRes, _ := s.Read(k)
	originalScannedAt := beforeRes.Snapshot.ScannedAt

	clock.add(2 * time.Hour)
	freshVolumes := []Resource{
		{Kind: "ec2/volume", ID: "vol-1", State: "available"},
		{Kind: "ec2/volume", ID: "vol-2", State: "in-use"},
	}
	snap, err := s.RefreshKind(context.Background(), k, "ec2/volume",
		func(_ context.Context, _ string) ([]Resource, error) { return freshVolumes, nil })
	if err != nil {
		t.Fatalf("RefreshKind: %v", err)
	}

	// The merged snapshot must still contain the ec2/instance resource
	// from the baseline scan.
	var instanceCount, volumeCount int
	for _, r := range snap.Resources {
		switch r.Kind {
		case "ec2/instance":
			instanceCount++
		case "ec2/volume":
			volumeCount++
		}
	}
	if instanceCount != 1 {
		t.Errorf("instance count after partial refresh = %d, want 1", instanceCount)
	}
	if volumeCount != 2 {
		t.Errorf("volume count after partial refresh = %d, want 2", volumeCount)
	}

	// Top-level ScannedAt must be unchanged (partial refresh ≠ full
	// scan). Per-kind timestamp must be bumped to now.
	if !snap.ScannedAt.Equal(originalScannedAt) {
		t.Errorf("top-level ScannedAt changed across partial refresh: was %s, now %s",
			originalScannedAt, snap.ScannedAt)
	}
	if got, want := snap.KindScannedAt["ec2/volume"], clock.now(); !got.Equal(want) {
		t.Errorf("KindScannedAt[ec2/volume] = %s, want %s", got, want)
	}
}

// TestRefreshKindRequiresBaseline covers the documented contract that
// partial refresh requires an existing snapshot.
func TestRefreshKindRequiresBaseline(t *testing.T) {
	clock := newFixedClock(time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	s := makeStore(t, clock)
	_, err := s.RefreshKind(context.Background(), sampleKey(), "ec2/volume",
		func(_ context.Context, _ string) ([]Resource, error) { return nil, nil })
	if !errors.Is(err, ErrNoSnapshot) {
		t.Errorf("RefreshKind without baseline: err=%v, want ErrNoSnapshot", err)
	}
}

// TestRefreshKindClearsSkip ensures a successful per-kind re-scan removes
// any prior "skipped" record for that kind from the snapshot.
func TestRefreshKindClearsSkip(t *testing.T) {
	clock := newFixedClock(time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	s := makeStore(t, clock)
	k := sampleKey()
	baseline := sampleScanResult()
	baseline.ScannersSkipped = []SkippedScanner{{Kind: "ec2/volume", Reason: "transient"}}
	if _, err := s.Refresh(context.Background(), k, scanFn(baseline), RefreshOptions{}); err != nil {
		t.Fatalf("baseline Refresh: %v", err)
	}

	snap, err := s.RefreshKind(context.Background(), k, "ec2/volume",
		func(_ context.Context, _ string) ([]Resource, error) {
			return []Resource{{Kind: "ec2/volume", ID: "vol-9"}}, nil
		})
	if err != nil {
		t.Fatalf("RefreshKind: %v", err)
	}
	for _, sk := range snap.ScannersSkipped {
		if sk.Kind == "ec2/volume" {
			t.Errorf("ec2/volume still in ScannersSkipped after successful re-scan: %v", sk)
		}
	}
}

// TestAtomicWriteLeavesNoTmp covers the atomic-write rule: after Refresh
// returns, no ".tmp" file should remain in the store directory.
func TestAtomicWriteLeavesNoTmp(t *testing.T) {
	clock := newFixedClock(time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	s := makeStore(t, clock)
	k := sampleKey()
	if _, err := s.Refresh(context.Background(), k, scanFn(sampleScanResult()), RefreshOptions{}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// TestWipeRemovesSnapshot covers Wipe.
func TestWipeRemovesSnapshot(t *testing.T) {
	clock := newFixedClock(time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	s := makeStore(t, clock)
	k := sampleKey()
	if _, err := s.Refresh(context.Background(), k, scanFn(sampleScanResult()), RefreshOptions{}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if err := s.Wipe(k); err != nil {
		t.Fatalf("Wipe: %v", err)
	}
	if _, err := os.Stat(s.path(k)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("snapshot still on disk after Wipe")
	}
	// Wiping a missing snapshot is a no-op.
	if err := s.Wipe(k); err != nil {
		t.Errorf("Wipe (missing): %v", err)
	}
}

// TestTTLForKindOverride covers per-kind TTL overrides.
func TestTTLForKindOverride(t *testing.T) {
	clock := newFixedClock(time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	s, err := NewStore(t.TempDir(), Config{
		TTL:      24 * time.Hour,
		KindTTL:  map[string]time.Duration{"ec2/volume": 6 * time.Hour},
		Throttle: 60 * time.Second,
		Now:      clock.now,
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if got, want := s.TTLFor("ec2/volume"), 6*time.Hour; got != want {
		t.Errorf("TTLFor(ec2/volume) = %s, want %s", got, want)
	}
	if got, want := s.TTLFor("ec2/instance"), 24*time.Hour; got != want {
		t.Errorf("TTLFor(ec2/instance) = %s, want %s (default)", got, want)
	}
	if got, want := s.TTLFor(""), 24*time.Hour; got != want {
		t.Errorf("TTLFor(\"\") = %s, want %s (default)", got, want)
	}
}

// TestReadMissing covers the no-snapshot path.
func TestReadMissing(t *testing.T) {
	clock := newFixedClock(time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	s := makeStore(t, clock)
	if _, err := s.Read(sampleKey()); !errors.Is(err, ErrNoSnapshot) {
		t.Errorf("Read missing: err=%v, want ErrNoSnapshot", err)
	}
}

// TestNewStoreDefaults covers the zero-value Config path.
func TestNewStoreDefaults(t *testing.T) {
	s, err := NewStore(t.TempDir(), Config{})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if s.TTL() != DefaultTTL {
		t.Errorf("TTL = %s, want %s", s.TTL(), DefaultTTL)
	}
	if s.Throttle() != DefaultThrottle {
		t.Errorf("Throttle = %s, want %s", s.Throttle(), DefaultThrottle)
	}
}
