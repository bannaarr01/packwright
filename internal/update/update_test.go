package update

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// withTestServer rewires the package globals to point at srv and restores the
// originals on cleanup. It also clears the cache so each test starts fresh.
func withTestServer(t *testing.T, srv *httptest.Server) {
	t.Helper()

	origBase := BaseURL
	origClient := HTTPClient
	origOwner := RepoOwner
	origRepo := RepoName
	origGetenv := Getenv
	origNow := Now
	origDisabled := Disabled

	BaseURL = srv.URL
	HTTPClient = srv.Client()
	RepoOwner = "acme"
	RepoName = "packwright"
	Getenv = func(string) string { return "" }
	Now = time.Now
	Disabled = false
	ResetCache()

	t.Cleanup(func() {
		BaseURL = origBase
		HTTPClient = origClient
		RepoOwner = origOwner
		RepoName = origRepo
		Getenv = origGetenv
		Now = origNow
		Disabled = origDisabled
		ResetCache()
	})
}

// stableHandler returns a /releases/latest JSON object describing tag.
func stableHandler(t *testing.T, tag string, hits *int32) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		if !strings.HasSuffix(r.URL.Path, "/releases/latest") {
			t.Errorf("unexpected path %q, want suffix /releases/latest", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.github+json")
		_, _ = w.Write([]byte(`{
			"tag_name":  "` + tag + `",
			"name":      "Packwright ` + tag + `",
			"html_url":  "https://example.com/release/` + tag + `",
			"draft":     false,
			"prerelease": false
		}`))
	})
}

func TestCheckOnce_StableNewerThanCurrent(t *testing.T) {
	srv := httptest.NewServer(stableHandler(t, "v1.2.3", nil))
	defer srv.Close()
	withTestServer(t, srv)

	got, err := CheckOnce(context.Background(), "v1.2.0", ChannelStable)
	if err != nil {
		t.Fatalf("CheckOnce err = %v, want nil", err)
	}
	if got == nil {
		t.Fatal("CheckOnce returned nil Latest, want v1.2.3 result")
	}
	if got.Tag != "v1.2.3" {
		t.Errorf("Tag = %q, want %q", got.Tag, "v1.2.3")
	}
	if got.URL != "https://example.com/release/v1.2.3" {
		t.Errorf("URL = %q, want example release URL", got.URL)
	}
}

func TestCheckOnce_StableNotNewerReturnsNil(t *testing.T) {
	srv := httptest.NewServer(stableHandler(t, "v1.2.0", nil))
	defer srv.Close()
	withTestServer(t, srv)

	got, err := CheckOnce(context.Background(), "v1.2.0", ChannelStable)
	if err != nil {
		t.Fatalf("CheckOnce err = %v, want nil", err)
	}
	if got != nil {
		t.Errorf("CheckOnce = %+v, want nil for equal versions", got)
	}
}

func TestCheckOnce_DefaultChannelIsStable(t *testing.T) {
	srv := httptest.NewServer(stableHandler(t, "v2.0.0", nil))
	defer srv.Close()
	withTestServer(t, srv)

	got, err := CheckOnce(context.Background(), "v1.0.0", "")
	if err != nil {
		t.Fatalf("CheckOnce err = %v, want nil", err)
	}
	if got == nil || got.Tag != "v2.0.0" {
		t.Errorf("CheckOnce = %+v, want v2.0.0", got)
	}
}

func TestCheckOnce_PrereleaseChannelHitsReleasesEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/releases") {
			t.Errorf("path = %q, want suffix /releases", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("per_page"); got != "1" {
			t.Errorf("per_page = %q, want %q", got, "1")
		}
		w.Header().Set("Content-Type", "application/vnd.github+json")
		_, _ = w.Write([]byte(`[
			{
				"tag_name":  "v1.3.0-rc.1",
				"name":      "RC 1",
				"html_url":  "https://example.com/release/v1.3.0-rc.1",
				"draft":     false,
				"prerelease": true
			}
		]`))
	}))
	defer srv.Close()
	withTestServer(t, srv)

	got, err := CheckOnce(context.Background(), "v1.2.0", ChannelPrerelease)
	if err != nil {
		t.Fatalf("CheckOnce err = %v, want nil", err)
	}
	if got == nil || got.Tag != "v1.3.0-rc.1" {
		t.Errorf("CheckOnce = %+v, want v1.3.0-rc.1", got)
	}
}

func TestCheckOnce_24hCacheServesSecondCall(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(stableHandler(t, "v1.5.0", &hits))
	defer srv.Close()
	withTestServer(t, srv)

	for i := 0; i < 3; i++ {
		got, err := CheckOnce(context.Background(), "v1.0.0", ChannelStable)
		if err != nil {
			t.Fatalf("CheckOnce[%d] err = %v", i, err)
		}
		if got == nil || got.Tag != "v1.5.0" {
			t.Fatalf("CheckOnce[%d] = %+v, want v1.5.0", i, got)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("HTTP hit count = %d, want 1 (cache should serve calls 2+)", n)
	}
}

func TestCheckOnce_CacheExpiresAfterTTL(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(stableHandler(t, "v1.5.0", &hits))
	defer srv.Close()
	withTestServer(t, srv)

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var now atomic.Value
	now.Store(base)
	Now = func() time.Time { return now.Load().(time.Time) }

	if _, err := CheckOnce(context.Background(), "v1.0.0", ChannelStable); err != nil {
		t.Fatalf("CheckOnce first call err = %v", err)
	}
	now.Store(base.Add(CacheTTL + time.Second))
	if _, err := CheckOnce(context.Background(), "v1.0.0", ChannelStable); err != nil {
		t.Fatalf("CheckOnce second call err = %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("HTTP hit count = %d, want 2 (cache should expire after TTL)", n)
	}
}

func TestCheckOnce_PerChannelCache(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/vnd.github+json")
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			_, _ = w.Write([]byte(`{"tag_name":"v1.0.0","name":"","html_url":""}`))
			return
		}
		_, _ = w.Write([]byte(`[{"tag_name":"v1.1.0-rc.1","name":"","html_url":""}]`))
	}))
	defer srv.Close()
	withTestServer(t, srv)

	if _, err := CheckOnce(context.Background(), "v0.9.0", ChannelStable); err != nil {
		t.Fatalf("stable call err = %v", err)
	}
	if _, err := CheckOnce(context.Background(), "v0.9.0", ChannelPrerelease); err != nil {
		t.Fatalf("prerelease call err = %v", err)
	}
	if n := atomic.LoadInt32(&hits); n != 2 {
		t.Errorf("HTTP hits = %d, want 2 (one per channel)", n)
	}
}

// failingTransport satisfies http.RoundTripper but never reaches a real
// server — every Do() call fails. Used to prove that the opt-out short-
// circuits before any network activity.
type failingTransport struct{ called int32 }

func (f *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	atomic.AddInt32(&f.called, 1)
	return nil, errors.New("failingTransport: refused to make a request")
}

func TestCheckOnce_EnvOptOutSkipsHTTP(t *testing.T) {
	origClient := HTTPClient
	origBase := BaseURL
	origGetenv := Getenv
	origDisabled := Disabled
	t.Cleanup(func() {
		HTTPClient = origClient
		BaseURL = origBase
		Getenv = origGetenv
		Disabled = origDisabled
		ResetCache()
	})

	ft := &failingTransport{}
	HTTPClient = &http.Client{Transport: ft}
	BaseURL = "https://api.github.com" // not reached
	Disabled = false
	Getenv = func(k string) string {
		if k == EnvOptOut {
			return "1"
		}
		return ""
	}
	ResetCache()

	got, err := CheckOnce(context.Background(), "v1.0.0", ChannelStable)
	if err != nil {
		t.Fatalf("CheckOnce err = %v, want nil", err)
	}
	if got != nil {
		t.Errorf("CheckOnce = %+v, want nil under env opt-out", got)
	}
	if c := atomic.LoadInt32(&ft.called); c != 0 {
		t.Errorf("transport called %d times, want 0 under env opt-out", c)
	}
}

func TestCheckOnce_DisabledFlagSkipsHTTP(t *testing.T) {
	origClient := HTTPClient
	origGetenv := Getenv
	origDisabled := Disabled
	t.Cleanup(func() {
		HTTPClient = origClient
		Getenv = origGetenv
		Disabled = origDisabled
		ResetCache()
	})

	ft := &failingTransport{}
	HTTPClient = &http.Client{Transport: ft}
	Getenv = func(string) string { return "" }
	Disabled = true
	ResetCache()

	got, err := CheckOnce(context.Background(), "v1.0.0", ChannelStable)
	if err != nil {
		t.Fatalf("CheckOnce err = %v, want nil", err)
	}
	if got != nil {
		t.Errorf("CheckOnce = %+v, want nil when Disabled is true", got)
	}
	if c := atomic.LoadInt32(&ft.called); c != 0 {
		t.Errorf("transport called %d times, want 0 when Disabled", c)
	}
}

func TestCheckOnce_GitHub404TreatedAsNoUpdate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()
	withTestServer(t, srv)

	got, err := CheckOnce(context.Background(), "v1.0.0", ChannelStable)
	if err != nil {
		t.Errorf("CheckOnce err = %v, want nil on 404", err)
	}
	if got != nil {
		t.Errorf("CheckOnce = %+v, want nil on 404", got)
	}
}

func TestCheckOnce_GitHub5xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	withTestServer(t, srv)

	_, err := CheckOnce(context.Background(), "v1.0.0", ChannelStable)
	if err == nil {
		t.Fatal("CheckOnce err = nil, want non-nil on 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v, want it to mention HTTP 500", err)
	}
}

func TestCheckOnce_CachesErrorsToo(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "rate-limited", http.StatusForbidden)
	}))
	defer srv.Close()
	withTestServer(t, srv)

	for i := 0; i < 3; i++ {
		if _, err := CheckOnce(context.Background(), "v1.0.0", ChannelStable); err == nil {
			t.Fatalf("CheckOnce[%d] err = nil, want non-nil", i)
		}
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Errorf("HTTP hits = %d, want 1 (errors should also be cached)", n)
	}
}

func TestCheckOnce_UnparseableCurrentVersionReturnsNil(t *testing.T) {
	srv := httptest.NewServer(stableHandler(t, "v1.0.0", nil))
	defer srv.Close()
	withTestServer(t, srv)

	got, err := CheckOnce(context.Background(), "dev", ChannelStable)
	if err != nil {
		t.Fatalf("CheckOnce err = %v, want nil", err)
	}
	if got != nil {
		t.Errorf("CheckOnce = %+v, want nil for unparseable current=%q", got, "dev")
	}
}

func TestCheckOnce_UserAgentAndAcceptHeaders(t *testing.T) {
	var gotAccept, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0","name":"","html_url":""}`))
	}))
	defer srv.Close()
	withTestServer(t, srv)

	if _, err := CheckOnce(context.Background(), "v0.9.0", ChannelStable); err != nil {
		t.Fatalf("CheckOnce err = %v", err)
	}
	if gotAccept != "application/vnd.github+json" {
		t.Errorf("Accept = %q, want application/vnd.github+json", gotAccept)
	}
	if gotUA != "packwright" {
		t.Errorf("User-Agent = %q, want %q", gotUA, "packwright")
	}
}

func TestCheckOnce_RequestURLIncludesOwnerAndRepo(t *testing.T) {
	var gotURL *url.URL
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL
		_, _ = w.Write([]byte(`{"tag_name":"v0.0.0","name":"","html_url":""}`))
	}))
	defer srv.Close()
	withTestServer(t, srv)

	if _, err := CheckOnce(context.Background(), "v0.0.0", ChannelStable); err != nil {
		t.Fatalf("CheckOnce err = %v", err)
	}
	if gotURL == nil {
		t.Fatal("server received no request")
	}
	want := "/repos/acme/packwright/releases/latest"
	if gotURL.Path != want {
		t.Errorf("Path = %q, want %q", gotURL.Path, want)
	}
}

func TestSemverCompare(t *testing.T) {
	cases := []struct {
		tag, current string
		want         bool
		name         string
	}{
		{"v1.2.3", "v1.2.2", true, "patch bump"},
		{"v1.3.0", "v1.2.9", true, "minor bump"},
		{"v2.0.0", "v1.9.9", true, "major bump"},
		{"v1.2.3", "v1.2.3", false, "equal"},
		{"v1.2.3", "v1.2.4", false, "older"},
		{"v1.0.0", "v1.0.0-rc.1", true, "release > rc"},
		{"v1.0.0-rc.1", "v1.0.0", false, "rc < release"},
		{"v1.0.0-rc.2", "v1.0.0-rc.1", true, "rc.2 > rc.1"},
		{"v1.0.0-rc.10", "v1.0.0-rc.2", true, "numeric prerelease ordering"},
		{"v1.0.0-alpha", "v1.0.0-beta", false, "alpha < beta lexicographic"},
		{"v1.0.0+meta", "v1.0.0", false, "build metadata ignored"},
		{"v1.2.3", "dev", false, "current unparseable"},
		{"main", "v1.0.0", false, "tag unparseable"},
		{"1.2.3", "1.2.2", true, "leading v optional"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNewer(tc.tag, tc.current); got != tc.want {
				t.Errorf("isNewer(%q, %q) = %v, want %v", tc.tag, tc.current, got, tc.want)
			}
		})
	}
}

func TestBannerDefaultWritesToStderrSink(t *testing.T) {
	origOut := bannerOut
	origBanner := Banner
	t.Cleanup(func() {
		bannerOut = origOut
		Banner = origBanner
	})

	var buf bytes.Buffer
	bannerOut = &buf
	Banner = defaultBanner

	Banner(&Latest{Tag: "v1.2.3", URL: "https://example.com/v1.2.3"})

	out := buf.String()
	if !strings.Contains(out, "v1.2.3") || !strings.Contains(out, "https://example.com/v1.2.3") {
		t.Errorf("default banner output = %q, want it to mention tag + URL", out)
	}
}

func TestBannerDefaultNilLatestNoOp(t *testing.T) {
	origOut := bannerOut
	t.Cleanup(func() { bannerOut = origOut })
	var buf bytes.Buffer
	bannerOut = &buf

	defaultBanner(nil)

	if buf.Len() != 0 {
		t.Errorf("banner wrote %q for nil Latest, want no output", buf.String())
	}
}

func TestValidChannel(t *testing.T) {
	for _, in := range []string{"stable", "prerelease", ""} {
		if !ValidChannel(in) {
			t.Errorf("ValidChannel(%q) = false, want true", in)
		}
	}
	for _, in := range []string{"nightly", "STABLE", "rc"} {
		if ValidChannel(in) {
			t.Errorf("ValidChannel(%q) = true, want false", in)
		}
	}
}
