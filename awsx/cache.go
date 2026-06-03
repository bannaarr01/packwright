// Package awsx wraps the AWS service clients Packwright uses for live
// resource pickers (VPC, subnet, security group, ACM certificate, ALB)
// and caches their results on disk to keep the picker UI responsive.
//
// The package is intentionally self-contained: callers pass in the
// cache home directory and a logger so awsx does not import any other
// Packwright package and can be linked into both the TUI and GUI builds.
package awsx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// DefaultTTL is the cache lifetime applied when callers do not specify one.
// It matches ADR-0010, which fixes the picker cache window at ten minutes.
const DefaultTTL = 10 * time.Minute

// cacheSubdir is appended to the caller-supplied cache home; keeping it inside
// awsx ensures other subsystems can share the same cache root without collision.
const cacheSubdir = "cache/awsx"

// Key identifies a single cache entry. Args ordering does not matter: pathFor
// sorts Args before hashing, so callers may pass them in any order and two
// keys differing only by Args order resolve to the same cache file.
type Key struct {
	Profile string   `json:"profile"`
	Region  string   `json:"region"`
	Fn      string   `json:"fn"`
	Args    []string `json:"args,omitempty"`
}

// Cache is a disk-backed time-to-live cache used by the picker methods on
// Client. It is safe for concurrent use within a single process.
type Cache struct {
	dir string
	ttl time.Duration
	log *slog.Logger
	mu  sync.Mutex
}

// entry is the on-disk JSON envelope. The timestamp is stored in-band so the
// TTL survives copies and rsync where filesystem mtime would not.
type entry struct {
	StoredAt time.Time       `json:"stored_at"`
	Payload  json.RawMessage `json:"payload"`
}

// NewCache returns a Cache rooted under cacheHome. The on-disk directory
// (cacheHome/cache/awsx) is created with 0o700 so the cached AWS picker
// data is not world-readable. A zero ttl falls back to DefaultTTL.
func NewCache(cacheHome string, ttl time.Duration, log *slog.Logger) (*Cache, error) {
	if cacheHome == "" {
		return nil, errors.New("awsx: cache home is empty")
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if log == nil {
		log = slog.Default()
	}
	dir := filepath.Join(cacheHome, cacheSubdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("awsx: creating cache dir %q: %w", dir, err)
	}
	return &Cache{dir: dir, ttl: ttl, log: log}, nil
}

// Dir reports the absolute directory the cache writes its files into.
func (c *Cache) Dir() string { return c.dir }

// TTL reports the cache lifetime applied to every entry.
func (c *Cache) TTL() time.Duration { return c.ttl }

// GetOrFetch returns the cached value for key when one exists and is within
// the cache TTL; otherwise it invokes fetch, stores the result, and returns it.
//
// The mutex is held only across the disk read and the disk write, never across
// fetch itself, so independent pickers (different keys) running on goroutines
// do not block each other for the duration of an AWS round-trip. Two callers
// racing on the same key may both fetch — accepted as a simpler alternative to
// singleflight for MVP-1, since picker calls are infrequent and idempotent.
//
// GetOrFetch is a free function rather than a method on Cache because Go does
// not permit type parameters on methods. Corrupt or unparseable cache files
// are treated as misses (logged at debug) so a single bad write never bricks
// a picker.
func GetOrFetch[T any](ctx context.Context, c *Cache, key Key, fetch func(context.Context) (T, error)) (T, error) {
	var zero T
	if c == nil {
		return fetch(ctx)
	}

	path, err := c.pathFor(key)
	if err != nil {
		return zero, err
	}

	c.mu.Lock()
	cached, hit := c.readLocked(path)
	c.mu.Unlock()
	if hit {
		var out T
		if err := json.Unmarshal(cached, &out); err == nil {
			return out, nil
		}
		c.log.Debug("awsx cache: payload unmarshal failed, refetching", "path", path)
	}

	fresh, err := fetch(ctx)
	if err != nil {
		return zero, err
	}

	payload, err := json.Marshal(fresh)
	if err != nil {
		return fresh, fmt.Errorf("awsx: marshalling cache payload for %s: %w", key.Fn, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.writeLocked(path, payload); err != nil {
		// A cache write failure is non-fatal for the caller: we still return
		// the freshly fetched value. Log it so disk problems are visible.
		c.log.Warn("awsx cache: write failed", "path", path, "err", err)
	}
	return fresh, nil
}

// pathFor returns the absolute file path that key hashes to. The input to the
// hash is the canonical JSON encoding of Key; this lets us reason about cache
// keys as values and round-trip them through the test suite cheaply.
func (c *Cache) pathFor(key Key) (string, error) {
	args := append([]string(nil), key.Args...)
	sort.Strings(args)
	canon := Key{Profile: key.Profile, Region: key.Region, Fn: key.Fn, Args: args}
	raw, err := json.Marshal(canon)
	if err != nil {
		return "", fmt.Errorf("awsx: hashing cache key for %s: %w", key.Fn, err)
	}
	sum := sha256.Sum256(raw)
	return filepath.Join(c.dir, hex.EncodeToString(sum[:])+".json"), nil
}

// readLocked returns the stored payload when the file exists and is within
// the TTL. Callers must hold c.mu.
func (c *Cache) readLocked(path string) (json.RawMessage, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			c.log.Debug("awsx cache: read failed", "path", path, "err", err)
		}
		return nil, false
	}
	var e entry
	if err := json.Unmarshal(raw, &e); err != nil {
		c.log.Debug("awsx cache: entry unmarshal failed", "path", path, "err", err)
		return nil, false
	}
	if time.Since(e.StoredAt) > c.ttl {
		return nil, false
	}
	return e.Payload, true
}

// writeLocked persists a payload atomically via write-temp-then-rename so a
// concurrent reader never sees a partial file. Callers must hold c.mu.
func (c *Cache) writeLocked(path string, payload json.RawMessage) error {
	e := entry{StoredAt: time.Now().UTC(), Payload: payload}
	raw, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshalling envelope: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renaming %s -> %s: %w", tmp, path, err)
	}
	return nil
}
