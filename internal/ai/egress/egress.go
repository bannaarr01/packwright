// Package egress enforces an outbound-host allowlist on the HTTP traffic an
// AI session is permitted to make (MVP-5 ADR-0033 / ADR-0034, exit criteria 4
// and 5).
//
// The threat model is narrow and specific: once a user opts in to AI, a third
// party (the LLM provider) enters the trust boundary. Packwright must guarantee
// that an AI session talks to exactly one host — the configured provider — and
// nothing else, so a compromised prompt, a buggy provider impl, or a malicious
// tool-use payload cannot exfiltrate data to an arbitrary URL. The allowlist is
// the mechanism that pins that guarantee:
//
//   - When AI is enabled, the engine builds the provider's HTTP client through
//     [Client], seeding the allowlist with exactly the active provider's
//     [provider.Provider.Hostname]. Any request to a different host is refused
//     before a connection is opened.
//   - When AI is disabled, no provider and therefore no client is constructed
//     at all (the [ai.Enabled] gate short-circuits first). Independently, an
//     empty allowlist — the zero value here — blocks every host, so even a
//     mis-wired caller cannot leak. This is the "ai.enabled:false removes the
//     provider from the egress allowlist" property.
//
// The allowlist matches on host only (no port, no scheme, no path); that is the
// right granularity for "which third party may we talk to". A local provider
// such as Ollama reports an empty hostname and is simply never added — local
// loopback traffic is out of the SaaS-exfiltration threat model.
package egress

import (
	"errors"
	"fmt"
	"net/http"
)

// ErrBlocked is the sentinel every allowlist rejection wraps. Callers branch on
// it with errors.Is(err, egress.ErrBlocked); the concrete [*BlockedError]
// carries the offending host for logging.
var ErrBlocked = errors.New("egress: outbound host not on allowlist")

// BlockedError reports an attempt to reach a host outside the allowlist. It
// wraps [ErrBlocked] so errors.Is matches the sentinel while errors.As recovers
// the host for a security-event log line.
type BlockedError struct {
	// Host is the rejected request's hostname (req.URL.Hostname()).
	Host string
}

// Error renders the rejection message.
func (e *BlockedError) Error() string {
	return fmt.Sprintf("egress: host %q is not on the AI session allowlist", e.Host)
}

// Is reports BlockedError as an instance of ErrBlocked.
func (e *BlockedError) Is(target error) bool { return target == ErrBlocked }

// Transport is an http.RoundTripper that refuses any request whose host is not
// on the allowlist, delegating permitted requests to Base. It is safe for
// concurrent use: the allowlist is fixed at construction and never mutated.
type Transport struct {
	// Base carries permitted requests. A nil Base falls back to
	// http.DefaultTransport at round-trip time.
	Base http.RoundTripper

	// allowed is the set of permitted hostnames. Unexported and never mutated
	// after NewTransport so the zero/empty case ("block everything") cannot be
	// loosened by a caller holding the Transport.
	allowed map[string]struct{}
}

// NewTransport wraps base in an allowlist permitting only the named hosts. A
// nil base defers to http.DefaultTransport. Empty host strings are skipped (so
// a local provider's "" hostname does not accidentally whitelist the empty
// host); passing no hosts yields a transport that blocks everything.
func NewTransport(base http.RoundTripper, hosts ...string) *Transport {
	allowed := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		if h == "" {
			continue
		}
		allowed[h] = struct{}{}
	}
	return &Transport{Base: base, allowed: allowed}
}

// Allowed reports whether host is permitted by this transport.
func (t *Transport) Allowed(host string) bool {
	_, ok := t.allowed[host]
	return ok
}

// RoundTrip enforces the allowlist. A request to a non-allowlisted host returns
// a *BlockedError (wrapping ErrBlocked) without touching Base — no connection
// is opened, no bytes leave the process. Permitted requests pass through to
// Base unchanged.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	if _, ok := t.allowed[host]; !ok {
		return nil, &BlockedError{Host: host}
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// Client returns a shallow copy of base whose Transport enforces an allowlist
// of the named hosts. The copy means the caller's client (and any transport it
// already carries, which becomes the new Base) is never mutated. A nil base
// yields a fresh http.Client. Passing no hosts produces a client that blocks
// every outbound request — the deliberate posture for "AI disabled".
func Client(base *http.Client, hosts ...string) *http.Client {
	var c http.Client
	if base != nil {
		c = *base
	}
	c.Transport = NewTransport(c.Transport, hosts...)
	return &c
}
