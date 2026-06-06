package awsx

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestCache(t *testing.T, ttl time.Duration) *Cache {
	t.Helper()
	c, err := NewCache(t.TempDir(), ttl, nil)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	return c
}

func TestNewCacheValidatesArgs(t *testing.T) {
	if _, err := NewCache("", time.Minute, nil); err == nil {
		t.Fatal("NewCache(\"\") = nil, want error")
	}
}

func TestNewCacheUsesDefaultTTL(t *testing.T) {
	c, err := NewCache(t.TempDir(), 0, nil)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	if c.TTL() != DefaultTTL {
		t.Fatalf("TTL = %v, want %v", c.TTL(), DefaultTTL)
	}
}

func TestGetOrFetchCachesWithinTTL(t *testing.T) {
	c := newTestCache(t, time.Hour)
	calls := 0
	fetch := func(context.Context) ([]string, error) {
		calls++
		return []string{"a", "b"}, nil
	}
	key := Key{Profile: "p", Region: "r", Fn: "Fn"}

	got1, err := GetOrFetch(context.Background(), c, key, fetch)
	if err != nil {
		t.Fatalf("first GetOrFetch: %v", err)
	}
	got2, err := GetOrFetch(context.Background(), c, key, fetch)
	if err != nil {
		t.Fatalf("second GetOrFetch: %v", err)
	}

	if calls != 1 {
		t.Fatalf("fetch calls = %d, want 1 (cache miss then hit)", calls)
	}
	if !equalStrings(got1, got2) {
		t.Fatalf("results differ: %v vs %v", got1, got2)
	}
}

func TestGetOrFetchRefetchesAfterTTL(t *testing.T) {
	c := newTestCache(t, time.Hour)
	calls := 0
	fetch := func(context.Context) (int, error) {
		calls++
		return calls, nil
	}
	key := Key{Profile: "p", Region: "r", Fn: "Fn"}

	if _, err := GetOrFetch(context.Background(), c, key, fetch); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Backdate the stored_at by rewriting the cache file so the entry
	// looks older than the TTL. This avoids real sleeps in tests.
	path, err := c.pathFor(key)
	if err != nil {
		t.Fatalf("pathFor: %v", err)
	}
	expireEntry(t, path)

	got, err := GetOrFetch(context.Background(), c, key, fetch)
	if err != nil {
		t.Fatalf("post-expiry: %v", err)
	}
	if calls != 2 {
		t.Fatalf("fetch calls = %d, want 2 (miss after expiry)", calls)
	}
	if got != 2 {
		t.Fatalf("got = %d, want 2", got)
	}
}

func TestGetOrFetchKeyArgsOrderingIsStable(t *testing.T) {
	c := newTestCache(t, time.Hour)
	calls := 0
	fetch := func(context.Context) (string, error) {
		calls++
		return "x", nil
	}
	a := Key{Profile: "p", Region: "r", Fn: "Fn", Args: []string{"alpha", "beta"}}
	b := Key{Profile: "p", Region: "r", Fn: "Fn", Args: []string{"beta", "alpha"}}

	if _, err := GetOrFetch(context.Background(), c, a, fetch); err != nil {
		t.Fatalf("a: %v", err)
	}
	if _, err := GetOrFetch(context.Background(), c, b, fetch); err != nil {
		t.Fatalf("b: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (reordered Args must hash identically)", calls)
	}
}

func TestGetOrFetchSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	key := Key{Profile: "p", Region: "r", Fn: "Fn"}

	// First "run": populate the cache.
	c1, err := NewCache(dir, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewCache run1: %v", err)
	}
	first := 0
	_, err = GetOrFetch(context.Background(), c1, key, func(context.Context) (string, error) {
		first++
		return "hello", nil
	})
	if err != nil {
		t.Fatalf("run1 fetch: %v", err)
	}
	if first != 1 {
		t.Fatalf("run1 calls = %d, want 1", first)
	}

	// Second "run": fresh Cache value on the same dir must read what run 1 wrote.
	c2, err := NewCache(dir, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewCache run2: %v", err)
	}
	second := 0
	got, err := GetOrFetch(context.Background(), c2, key, func(context.Context) (string, error) {
		second++
		return "world", nil
	})
	if err != nil {
		t.Fatalf("run2 fetch: %v", err)
	}
	if second != 0 {
		t.Fatalf("run2 calls = %d, want 0 (cache should round-trip across runs)", second)
	}
	if got != "hello" {
		t.Fatalf("run2 result = %q, want %q (cached value)", got, "hello")
	}
}

func TestGetOrFetchTreatsCorruptFileAsMiss(t *testing.T) {
	c := newTestCache(t, time.Hour)
	key := Key{Profile: "p", Region: "r", Fn: "Fn"}
	path, err := c.pathFor(key)
	if err != nil {
		t.Fatalf("pathFor: %v", err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}

	calls := 0
	got, err := GetOrFetch(context.Background(), c, key, func(context.Context) (string, error) {
		calls++
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("GetOrFetch: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (corrupt file should be a miss)", calls)
	}
	if got != "ok" {
		t.Fatalf("got = %q, want %q", got, "ok")
	}
}

func TestGetOrFetchPropagatesFetchError(t *testing.T) {
	c := newTestCache(t, time.Hour)
	_, err := GetOrFetch(context.Background(), c, Key{Fn: "Fn"}, func(context.Context) (string, error) {
		return "", errFake
	})
	if err == nil || !strings.Contains(err.Error(), errFake.Error()) {
		t.Fatalf("err = %v, want one wrapping %v", err, errFake)
	}
}

func TestNewCacheCreatesDirInsideHome(t *testing.T) {
	home := t.TempDir()
	c, err := NewCache(home, time.Hour, nil)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	want := filepath.Join(home, "cache", "awsx")
	if c.Dir() != want {
		t.Fatalf("Dir = %q, want %q", c.Dir(), want)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", want)
	}
}

// expireEntry rewrites the entry envelope at path so its StoredAt is far in
// the past, simulating TTL expiry without real time travel.
func expireEntry(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read entry: %v", err)
	}
	var e entry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("unmarshal entry: %v", err)
	}
	e.StoredAt = time.Now().Add(-24 * time.Hour)
	out, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatalf("write entry: %v", err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// errFake is a sentinel for fetch-failure tests.
var errFake = &fakeErr{"fake fetch failure"}

type fakeErr struct{ s string }

func (e *fakeErr) Error() string { return e.s }
