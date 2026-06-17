// Package cache persists AWS audit inventory scans to disk and gates
// re-scans with a TTL, a 60-second throttle, and a per-kind partial
// refresh path. See ADR-0044 for the layout and rationale.
package cache

import (
	"fmt"
	"strings"
)

// Key identifies a unique snapshot. One snapshot exists per
// (profile, region, lookback-days) tuple, so switching any of these
// fields uses (or creates) a different snapshot file.
type Key struct {
	// Profile is the AWS profile the scan ran under.
	Profile string
	// Region is the AWS region the scan targeted.
	Region string
	// LookbackDays is the size of the look-back window for last-used
	// signals, in days. Used as part of the cache key so a different
	// window does not silently share a snapshot.
	LookbackDays int
}

// Filename returns the snapshot's basename inside the snapshots dir.
// Format: "<profile>-<region>-<lookback>.json".
func (k Key) Filename() string {
	return fmt.Sprintf("%s-%s-%d.json", k.Profile, k.Region, k.LookbackDays)
}

// LegacyFilename returns the basename a snapshot is renamed to when its
// on-disk schema version no longer matches SchemaVersion.
func (k Key) LegacyFilename() string {
	return strings.TrimSuffix(k.Filename(), ".json") + ".legacy.json"
}

// String returns a stable, log-friendly representation of the key.
func (k Key) String() string {
	return fmt.Sprintf("%s/%s/%dd", k.Profile, k.Region, k.LookbackDays)
}

// Validate reports whether the key is well-formed. Empty profile or
// region values are rejected so the filename cannot collapse into an
// ambiguous "-region-30.json" form.
func (k Key) Validate() error {
	if strings.TrimSpace(k.Profile) == "" {
		return fmt.Errorf("cache: key profile is empty")
	}
	if strings.TrimSpace(k.Region) == "" {
		return fmt.Errorf("cache: key region is empty")
	}
	if k.LookbackDays < 0 {
		return fmt.Errorf("cache: key lookback days is negative: %d", k.LookbackDays)
	}
	if strings.ContainsAny(k.Profile, "/\\") || strings.ContainsAny(k.Region, "/\\") {
		return fmt.Errorf("cache: key profile/region must not contain path separators")
	}
	return nil
}
