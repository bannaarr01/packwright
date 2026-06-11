package bootstrap

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/update"
	"github.com/bannaarr01/packwright/internal/usage"
	"github.com/bannaarr01/packwright/internal/version"
	pwlog "github.com/bannaarr01/packwright/log"
	"github.com/bannaarr01/packwright/meta"
)

// TestInitOpensBothLogs verifies that Init creates the operational log
// (packwright.log) and the usage log (usage.jsonl) under the resolved
// home, and rewires slog.Default to the operational logger.
func TestInitOpensBothLogs(t *testing.T) {
	home := t.TempDir()
	withHome(t, home)
	withSlogDefault(t)
	withUsageDefault(t)
	withLogDefault(t)
	withNoUpdateCheck(t)

	logger := Init("test")
	if logger == nil {
		t.Fatal("Init returned nil logger")
	}
	if logger != slog.Default() {
		t.Errorf("returned logger does not match slog.Default()")
	}
	if logger != pwlog.Default {
		t.Errorf("slog.Default was not rewired to log.Default")
	}

	// Emit one record on each pipeline so the files exist on disk.
	slog.Info("op-log-hello")
	if err := usage.Record(usage.UsageEvent{
		Command: "/bootstrap-test",
		Kind:    usage.KindResource,
		Outcome: usage.OutcomeSuccess,
		Surface: usage.SurfaceTUI,
		Version: "test",
	}); err != nil {
		t.Fatalf("usage.Record: %v", err)
	}

	opLog := filepath.Join(home, "logs", "packwright.log")
	if _, err := os.Stat(opLog); err != nil {
		t.Errorf("packwright.log not created: %v", err)
	}
	usageLog := filepath.Join(home, "logs", usage.Filename)
	if _, err := os.Stat(usageLog); err != nil {
		t.Errorf("usage.jsonl not created: %v", err)
	}
}

// TestInitMissingHomeFallsBackToStderr asserts that when home resolution
// fails, Init returns the stdlib default rather than crashing. The
// fallback is essential for cases like sandboxed CI environments where
// no home directory is available.
func TestInitMissingHomeFallsBackToStderr(t *testing.T) {
	withSlogDefault(t)
	withUsageDefault(t)
	withLogDefault(t)
	withNoUpdateCheck(t)
	// Force home resolution to fail by clearing every env var Home consults.
	t.Setenv("PACKWRIGHT_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", "")
	}

	logger := Init("test")
	if logger == nil {
		t.Fatal("Init returned nil logger when home unresolved")
	}
	// slog.Default must NOT have been swapped to pwlog.Default — log.Init
	// was never reached because home resolution failed first.
	if slog.Default() == pwlog.Default {
		t.Errorf("slog.Default was rewired despite home resolution failure")
	}
}

// TestParseLevel covers the four valid level strings and the fallback.
func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"   warn  ", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"nonsense", slog.LevelInfo},
	}
	for _, tc := range cases {
		if got := parseLevel(tc.in); got != tc.want {
			t.Errorf("parseLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// --- helpers ---

// withHome pins config.Home() to the given directory by setting
// PACKWRIGHT_HOME.
func withHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PACKWRIGHT_HOME", dir)
}

// withSlogDefault snapshots slog.Default and restores it after the test.
func withSlogDefault(t *testing.T) {
	t.Helper()
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })
}

// withLogDefault snapshots pwlog.Default and restores it after the test.
func withLogDefault(t *testing.T) {
	t.Helper()
	orig := pwlog.Default
	t.Cleanup(func() { pwlog.Default = orig })
}

// withUsageDefault snapshots usage.Default and restores it after the test.
func withUsageDefault(t *testing.T) {
	t.Helper()
	orig := usage.Default
	t.Cleanup(func() { usage.Default = orig })
}

// withNoUpdateCheck swaps the package-level checkForUpdate seam for a
// no-op so the test does not spawn a goroutine that would otherwise hit
// the network. Use in every Init() test that does not specifically want
// to exercise the update probe.
func withNoUpdateCheck(t *testing.T) {
	t.Helper()
	orig := checkForUpdate
	checkForUpdate = func(string, *config.Config) {}
	t.Cleanup(func() { checkForUpdate = orig })
}

// TestInitUpdateCheckBannersNewerRelease runs Init against an
// httptest.Server that mimics the GitHub Releases API. The test asserts
// that update.Banner is invoked exactly once with the parsed tag, and
// that no banner fires once PACKWRIGHT_NO_UPDATE_CHECK=1 short-circuits
// the probe. This pins ADR-0030's "first launch after a new release
// surfaces a banner" exit criterion to a single test.
func TestInitUpdateCheckBannersNewerRelease(t *testing.T) {
	home := t.TempDir()
	withHome(t, home)
	withSlogDefault(t)
	withUsageDefault(t)
	withLogDefault(t)

	// Pretend the running build is v0.4.0 so the GitHub v0.5.0 response
	// satisfies isNewer(). The Dev sentinel would short-circuit instead.
	// Both version sources need the override: meta.Version is what
	// bootstrap passes into update.CheckOnce; internal/version.Version is
	// what other call sites (e.g. requires gate) read. Setting both keeps
	// the two in sync until they are folded into one variable.
	restore := version.Set("v0.4.0")
	t.Cleanup(restore)
	origMeta := meta.Version
	meta.Version = "v0.4.0"
	t.Cleanup(func() { meta.Version = origMeta })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /repos/<owner>/<repo>/releases/latest is the only path the
		// stable channel hits. Match it exactly so a regression that
		// changed the URL would surface as a 404 here.
		want := "/repos/" + update.RepoOwner + "/" + update.RepoName + "/releases/latest"
		if r.URL.Path != want {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.github+json")
		_, _ = w.Write([]byte(
			`{"tag_name":"v0.5.0","name":"v0.5.0","html_url":"https://example.invalid/v0.5.0"}`,
		))
	}))
	t.Cleanup(srv.Close)

	prevBase, prevClient := update.BaseURL, update.HTTPClient
	t.Cleanup(func() {
		update.BaseURL, update.HTTPClient = prevBase, prevClient
		update.ResetCache()
		update.Banner = bannerOriginal
	})
	update.BaseURL = srv.URL
	update.HTTPClient = srv.Client()
	update.ResetCache()

	var (
		mu      sync.Mutex
		seen    []update.Latest
		called  atomic.Int32
		bannerD = make(chan struct{})
	)
	bannerOriginal = update.Banner
	update.Banner = func(l *update.Latest) {
		if l == nil {
			return
		}
		mu.Lock()
		seen = append(seen, *l)
		mu.Unlock()
		if called.Add(1) == 1 {
			close(bannerD)
		}
	}

	_ = Init("test")

	select {
	case <-bannerD:
	case <-time.After(2 * time.Second):
		t.Fatal("update.Banner not invoked within 2s of Init")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 {
		t.Fatalf("Banner called %d times, want 1", len(seen))
	}
	if seen[0].Tag != "v0.5.0" {
		t.Errorf("Banner Tag = %q, want %q", seen[0].Tag, "v0.5.0")
	}
}

// bannerOriginal is the per-test snapshot of update.Banner that
// TestInitUpdateCheckBannersNewerRelease's t.Cleanup restores. Package
// level so the assignment inside the test happens before the cleanup
// closure captures it.
var bannerOriginal update.BannerFunc

// TestInitUpdateCheckHonoursOptOut pins the env-variable opt-out path:
// PACKWRIGHT_NO_UPDATE_CHECK=1 must skip CheckOnce entirely so no HTTP
// is issued. We assert this by replacing HTTPClient with a panic-on-use
// stub and confirming the goroutine completes without tripping it.
func TestInitUpdateCheckHonoursOptOut(t *testing.T) {
	home := t.TempDir()
	withHome(t, home)
	withSlogDefault(t)
	withUsageDefault(t)
	withLogDefault(t)

	t.Setenv("PACKWRIGHT_NO_UPDATE_CHECK", "1")

	prevClient := update.HTTPClient
	t.Cleanup(func() {
		update.HTTPClient = prevClient
		update.ResetCache()
	})
	update.HTTPClient = &http.Client{
		Transport: panicTransport(t),
		Timeout:   time.Second,
	}
	update.ResetCache()

	prevBanner := update.Banner
	t.Cleanup(func() { update.Banner = prevBanner })
	update.Banner = func(*update.Latest) {
		t.Fatalf("Banner invoked despite PACKWRIGHT_NO_UPDATE_CHECK=1")
	}

	_ = Init("test")

	// Give the goroutine a chance to run; it should return immediately
	// because update.CheckOnce short-circuits before any HTTP call.
	select {
	case <-time.After(200 * time.Millisecond):
		// Expected: no banner, no panic — the goroutine finished or never
		// touched the transport. The panicTransport / Banner stubs above
		// would have failed the test if they had been called.
	case <-context.Background().Done():
	}
}

// panicTransport returns an http.RoundTripper that fails the test on
// any request. It is used as a sentinel: a probe that does not actually
// hit the network leaves it untouched and the test passes; a regression
// that re-enables egress under opt-out trips it and fails loudly.
func panicTransport(t *testing.T) http.RoundTripper {
	t.Helper()
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("bootstrap issued an HTTP request despite opt-out: %s %s",
			req.Method, req.URL)
		return nil, http.ErrAbortHandler
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
