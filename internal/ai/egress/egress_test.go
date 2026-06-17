package egress

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recordRT is a fake base RoundTripper that records the hosts it is asked to
// reach and returns a canned 200 response. It lets the tests assert that the
// allowlist refuses blocked hosts *before* the base transport is consulted.
type recordRT struct {
	hosts []string
}

func (r *recordRT) RoundTrip(req *http.Request) (*http.Response, error) {
	r.hosts = append(r.hosts, req.URL.Hostname())
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("ok")),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func mustReq(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request %q: %v", url, err)
	}
	return req
}

func TestTransport_AllowsListedHost(t *testing.T) {
	base := &recordRT{}
	tr := NewTransport(base, "api.anthropic.com")

	resp, err := tr.RoundTrip(mustReq(t, "https://api.anthropic.com/v1/messages"))
	if err != nil {
		t.Fatalf("allowed host returned error: %v", err)
	}
	_ = resp.Body.Close()
	if got := []string{"api.anthropic.com"}; len(base.hosts) != 1 || base.hosts[0] != got[0] {
		t.Fatalf("base transport hosts = %v, want %v", base.hosts, got)
	}
}

func TestTransport_BlocksUnlistedHost(t *testing.T) {
	base := &recordRT{}
	tr := NewTransport(base, "api.anthropic.com")

	_, err := tr.RoundTrip(mustReq(t, "https://evil.example.com/exfil"))
	if err == nil {
		t.Fatal("blocked host returned nil error")
	}
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("error %v is not ErrBlocked", err)
	}
	var be *BlockedError
	if !errors.As(err, &be) {
		t.Fatalf("error %v is not *BlockedError", err)
	}
	if be.Host != "evil.example.com" {
		t.Fatalf("BlockedError.Host = %q, want %q", be.Host, "evil.example.com")
	}
	if len(base.hosts) != 0 {
		t.Fatalf("base transport was consulted for a blocked host: %v", base.hosts)
	}
}

func TestTransport_EmptyAllowlistBlocksEverything(t *testing.T) {
	// The "AI disabled" posture: no hosts allowed => every request refused.
	base := &recordRT{}
	tr := NewTransport(base) // no hosts

	for _, url := range []string{
		"https://api.anthropic.com/v1/messages",
		"https://api.openai.com/v1/chat/completions",
		"http://localhost:11434/api/chat",
	} {
		if _, err := tr.RoundTrip(mustReq(t, url)); !errors.Is(err, ErrBlocked) {
			t.Fatalf("url %q: err = %v, want ErrBlocked", url, err)
		}
	}
	if len(base.hosts) != 0 {
		t.Fatalf("base transport was consulted under an empty allowlist: %v", base.hosts)
	}
}

func TestTransport_EmptyHostStringsSkipped(t *testing.T) {
	// A local provider reports "" as its hostname; that must not whitelist the
	// empty host (which an attacker-controlled relative URL could resolve to).
	tr := NewTransport(nil, "", "api.openai.com", "")
	if tr.Allowed("") {
		t.Fatal("empty host was added to the allowlist")
	}
	if !tr.Allowed("api.openai.com") {
		t.Fatal("api.openai.com should be allowed")
	}
}

func TestClient_WrapsWithoutMutatingBase(t *testing.T) {
	baseRT := &recordRT{}
	base := &http.Client{Transport: baseRT}

	c := Client(base, "api.anthropic.com")
	if c == base {
		t.Fatal("Client returned the same *http.Client; must copy")
	}
	if base.Transport != baseRT {
		t.Fatal("Client mutated the base client's Transport")
	}
	// The base client's transport must become the wrapper's Base so its
	// settings (proxy, TLS, etc.) are preserved for permitted requests.
	tr, ok := c.Transport.(*Transport)
	if !ok {
		t.Fatalf("wrapped client Transport is %T, want *Transport", c.Transport)
	}
	if tr.Base != baseRT {
		t.Fatal("wrapped transport did not preserve the base client's transport as Base")
	}
}

func TestClient_NilBaseYieldsBlockingClientWhenNoHosts(t *testing.T) {
	c := Client(nil)
	_, err := c.Transport.RoundTrip(mustReq(t, "https://api.openai.com/v1/chat/completions"))
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("nil base + no hosts: err = %v, want ErrBlocked", err)
	}
}

// TestClient_EndToEndAllowlist exercises the wrapper against a real
// httptest.Server to confirm permitted traffic actually flows and a sibling
// host on the same loopback is still refused by name.
func TestClient_EndToEndAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "pong")
	}))
	defer srv.Close()

	// srv.URL is http://127.0.0.1:<port>; allow exactly that host.
	host := mustReq(t, srv.URL).URL.Hostname()
	c := Client(srv.Client(), host)

	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("permitted request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "pong" {
		t.Fatalf("body = %q, want pong", body)
	}

	if _, err := c.Get("https://api.anthropic.com/v1/messages"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("off-allowlist request err = %v, want ErrBlocked", err)
	}
}
