// Package update implements Packwright's lightweight, opt-out update check
// against the GitHub Releases API.
//
// On launch (and at most once per 24h per process, in-memory cached) the app
// calls CheckOnce, which performs a single HTTP GET to
//
//	https://api.github.com/repos/<owner>/<repo>/releases/latest   (stable)
//	https://api.github.com/repos/<owner>/<repo>/releases?per_page=1 (prerelease)
//
// and returns a *Latest when GitHub reports a strictly-newer SemVer tag than
// the running version. Failures are silent: the caller continues with no
// banner. Per ADR-0030, this is the only outbound HTTP call the app makes on
// its own.
//
// # Opt-out
//
// Setting PACKWRIGHT_NO_UPDATE_CHECK=1 in the environment, or setting the
// package-level Disabled variable to true (the CLI wires this from
// config.yaml.disable_update_check), short-circuits CheckOnce so it returns
// (nil, nil) without making any HTTP request.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Channel selects which kind of release CheckOnce considers.
type Channel string

// Recognised channels. Values match config.yaml.update_channel.
const (
	ChannelStable     Channel = "stable"
	ChannelPrerelease Channel = "prerelease"
)

// Latest describes the release CheckOnce judged newer than the running
// version. URL points at the human-readable release page so callers (TUI/GUI
// banner) can hand it off to the user's browser.
type Latest struct {
	Tag  string // canonical SemVer tag, e.g. "v1.2.3" or "v1.2.3-rc.1"
	Name string // GitHub release name (often equal to Tag)
	URL  string // HTML release page URL
}

// EnvOptOut is the environment variable that disables CheckOnce entirely.
// Setting it to "1" is the documented opt-out alongside config.yaml.
const EnvOptOut = "PACKWRIGHT_NO_UPDATE_CHECK"

// CacheTTL is the per-channel in-memory cache lifetime. CheckOnce calls within
// this window return the previous result without contacting GitHub. ADR-0030
// fixes this at 24 hours to stay well under GitHub's 60/hr anonymous limit.
const CacheTTL = 24 * time.Hour

// Configuration variables. They are package-level rather than per-call so the
// caller (cmd) can configure them once at startup and tests can override them
// against an httptest.Server. Mutating them concurrently with CheckOnce is not
// supported.
var (
	// HTTPClient performs the GitHub API request. Tests inject a client with
	// a stub Transport (or a transport that always fails, to prove no HTTP
	// happens under the opt-out).
	HTTPClient = &http.Client{Timeout: 10 * time.Second}

	// BaseURL is the GitHub API root. Tests point this at an httptest.Server.
	BaseURL = "https://api.github.com"

	// RepoOwner / RepoName identify the GitHub repo that publishes releases.
	RepoOwner = "bannaarr01"
	RepoName  = "packwright"

	// Disabled, when true, makes CheckOnce return (nil, nil) without an HTTP
	// call. cmd sets this from config.yaml.disable_update_check.
	Disabled = false

	// Getenv reads environment variables. Override in tests to drive the
	// PACKWRIGHT_NO_UPDATE_CHECK opt-out without touching real process env.
	Getenv = os.Getenv

	// Now returns the current time. Override in tests to drive the 24h
	// cache.
	Now = time.Now
)

// cacheEntry stores one CheckOnce result alongside the time it was recorded.
// We cache the API result rather than the post-comparison decision so the
// cache survives version changes within the same process (the running
// currentVersion never changes, but a future caller might).
type cacheEntry struct {
	at      time.Time
	latest  *Latest // raw API result; may be nil when GitHub returned 404
	fetchEr error   // the error CheckOnce will replay for the next 24h
}

// cache is the in-process result cache, keyed by Channel. sync.Map is the
// right shape here: writes are rare (≤1 per channel per 24h), reads are
// hot on every launch path.
var cache sync.Map

// ResetCache clears the in-memory cache. Tests call this between cases.
func ResetCache() { cache = sync.Map{} }

// CheckOnce returns the latest release on the requested channel when it is
// strictly newer (SemVer) than currentVersion. It returns (nil, nil) when:
//
//   - the env var EnvOptOut is set to "1";
//   - the package-level Disabled flag is true;
//   - GitHub reports no newer release than currentVersion;
//   - GitHub returned 404 (no releases yet on this channel).
//
// On HTTP / decode / status errors it returns (nil, err); the caller is
// expected to swallow that and continue silently (per ADR-0030).
//
// Within a process, CheckOnce performs at most one HTTP GET per channel per
// CacheTTL window. Subsequent calls replay the cached result (including any
// error).
func CheckOnce(ctx context.Context, currentVersion string, channel Channel) (*Latest, error) {
	if Disabled {
		return nil, nil
	}
	if Getenv(EnvOptOut) == "1" {
		return nil, nil
	}
	if channel == "" {
		channel = ChannelStable
	}

	latest, err := cachedFetch(ctx, channel)
	if err != nil {
		return nil, err
	}
	if latest == nil {
		return nil, nil
	}
	if !isNewer(latest.Tag, currentVersion) {
		return nil, nil
	}
	return latest, nil
}

// cachedFetch returns the per-channel GitHub result, hitting the network at
// most once per CacheTTL.
func cachedFetch(ctx context.Context, channel Channel) (*Latest, error) {
	if entry, ok := loadCache(channel); ok {
		return entry.latest, entry.fetchEr
	}
	latest, err := fetchLatest(ctx, channel)
	cache.Store(channel, &cacheEntry{at: Now(), latest: latest, fetchEr: err})
	return latest, err
}

func loadCache(channel Channel) (*cacheEntry, bool) {
	v, ok := cache.Load(channel)
	if !ok {
		return nil, false
	}
	e := v.(*cacheEntry)
	if Now().Sub(e.at) >= CacheTTL {
		return nil, false
	}
	return e, true
}

// release mirrors the subset of the GitHub Releases API response we care
// about. Unknown fields are ignored by encoding/json.
type release struct {
	TagName    string `json:"tag_name"`
	Name       string `json:"name"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

func (r release) toLatest() *Latest {
	return &Latest{Tag: r.TagName, Name: r.Name, URL: r.HTMLURL}
}

// fetchLatest performs the one outbound HTTP GET. For the stable channel it
// uses GitHub's /releases/latest endpoint, which already filters out drafts
// and prereleases. For the prerelease channel it pulls the most recent entry
// from /releases (sorted by created_at desc by GitHub).
func fetchLatest(ctx context.Context, channel Channel) (*Latest, error) {
	url, multi := buildURL(channel)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("update: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "packwright")

	resp, err := HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: github get: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// fall through
	case http.StatusNotFound:
		// No releases published yet — treat as "no update available".
		return nil, nil
	default:
		return nil, fmt.Errorf("update: github status %s", strconv.Itoa(resp.StatusCode))
	}

	if multi {
		var list []release
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
			return nil, fmt.Errorf("update: decode releases: %w", err)
		}
		for _, r := range list {
			if r.Draft {
				continue
			}
			return r.toLatest(), nil
		}
		return nil, nil
	}

	var r release
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("update: decode release: %w", err)
	}
	return r.toLatest(), nil
}

// buildURL returns the GitHub API URL for the channel and reports whether the
// response is a JSON array (vs a single object).
func buildURL(channel Channel) (url string, isArray bool) {
	switch channel {
	case ChannelPrerelease:
		return fmt.Sprintf("%s/repos/%s/%s/releases?per_page=1", BaseURL, RepoOwner, RepoName), true
	default:
		return fmt.Sprintf("%s/repos/%s/%s/releases/latest", BaseURL, RepoOwner, RepoName), false
	}
}

// isNewer reports whether tag is a strictly-newer SemVer than current.
//
// Both inputs are expected in the "vMAJOR.MINOR.PATCH[-PRERELEASE]" form that
// our release tags use. If either side fails to parse (notably current="dev"
// for local builds) we return false: better to skip a banner than nag every
// developer launch with a spurious "update available".
func isNewer(tag, current string) bool {
	a, ok := parseSemver(tag)
	if !ok {
		return false
	}
	b, ok := parseSemver(current)
	if !ok {
		return false
	}
	return semverLess(b, a)
}

// semver is the parsed form of "vMAJOR.MINOR.PATCH[-PRERELEASE]". Build
// metadata ("+build") is ignored per SemVer §10.
type semver struct {
	major, minor, patch int
	pre                 string // empty when not a prerelease
}

// parseSemver accepts tags with or without the leading "v". It returns false
// for inputs that don't match the SemVer 2.0 core grammar — including "dev",
// "main", commit shas, and other non-release markers.
func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(s, "v")
	if i := strings.Index(s, "+"); i >= 0 {
		s = s[:i]
	}
	var pre string
	if i := strings.Index(s, "-"); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	nums := [3]int{}
	for i, p := range parts {
		if p == "" {
			return semver{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		nums[i] = n
	}
	return semver{major: nums[0], minor: nums[1], patch: nums[2], pre: pre}, true
}

// semverLess reports whether a < b under SemVer 2.0 precedence, including
// the prerelease tie-breaker (§11): a version with a prerelease tag is lower
// than the same core version without one, and prerelease identifiers compare
// per dot-separated segment (numeric < alphanumeric, numerics compared as
// numbers, alphanumerics lexicographically).
func semverLess(a, b semver) bool {
	if a.major != b.major {
		return a.major < b.major
	}
	if a.minor != b.minor {
		return a.minor < b.minor
	}
	if a.patch != b.patch {
		return a.patch < b.patch
	}
	if a.pre == b.pre {
		return false
	}
	if a.pre == "" {
		return false // 1.0.0 > 1.0.0-rc.1
	}
	if b.pre == "" {
		return true
	}
	return preLess(a.pre, b.pre)
}

// preLess compares two non-empty prerelease strings per SemVer §11.
func preLess(a, b string) bool {
	ap := strings.Split(a, ".")
	bp := strings.Split(b, ".")
	for i := 0; i < len(ap) && i < len(bp); i++ {
		if c := compareIdent(ap[i], bp[i]); c != 0 {
			return c < 0
		}
	}
	return len(ap) < len(bp)
}

func compareIdent(a, b string) int {
	an, aErr := strconv.Atoi(a)
	bn, bErr := strconv.Atoi(b)
	switch {
	case aErr == nil && bErr == nil:
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		default:
			return 0
		}
	case aErr == nil:
		return -1 // numeric identifiers always have lower precedence
	case bErr == nil:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

// ValidChannel reports whether s is a recognised channel literal. The empty
// string is accepted: CheckOnce treats it as ChannelStable.
func ValidChannel(s string) bool {
	switch Channel(s) {
	case ChannelStable, ChannelPrerelease, "":
		return true
	default:
		return false
	}
}
