package redact

import (
	"encoding/json"
	"fmt"
)

// Opts toggles the optional redactions from ADR-0037. The zero value
// is "strict baseline": only the always-on patterns fire and no
// optional rules are enabled. Callers that want the recommended
// production defaults (account IDs redacted, private IPs preserved)
// should start from DefaultOpts and then override individual fields.
type Opts struct {
	// RedactAccountIDs strips AWS 12-digit account IDs (in ARNs and
	// in standalone text). Default ON via DefaultOpts. Single-account
	// and homelab users typically set this to false to keep ARNs
	// human-readable in the AI conversation.
	RedactAccountIDs bool

	// RedactPrivateIPs strips RFC1918 addresses (10/8, 172.16/12,
	// 192.168/16). Off by default; turn it on when VPC topology is
	// itself sensitive.
	RedactPrivateIPs bool

	// InternalHostPatterns is a list of Go RE2 patterns. Any substring
	// that matches one is replaced with "<internal-host>". Empty by
	// default; invalid patterns are silently skipped so a typo in
	// config never causes the redactor to fail open.
	InternalHostPatterns []string

	// SecretFields is the list of form-field names that this session
	// has declared as type: secret (ADR-0007). Their values are
	// stripped as "<redacted:form_secret>" wherever they appear in the
	// payload, in both JSON ("name": "value") and text (name=value)
	// shapes. Matching is case-insensitive.
	SecretFields []string
}

// DefaultOpts returns the recommended production baseline: account IDs
// redacted, private IPs preserved, no host patterns configured, and no
// form-secret fields registered. Callers should start here and override
// individual fields rather than constructing an Opts from scratch, so
// that a future field added to Opts inherits a safe default.
func DefaultOpts() Opts {
	return Opts{RedactAccountIDs: true}
}

// Redacted is the result of Apply and the exact value the UI's
// "Context sent" pane renders. Text is the payload as it will be sent
// over the wire; Counts is a per-hint tally of how many substitutions
// fired, so the pane can summarise the scrub ("3 aws_access_key,
// 1 jwt, 5 account") without inspecting Text again.
type Redacted struct {
	// Text is the redacted payload, ready to send to the LLM provider.
	Text string

	// Counts maps each redaction hint (the HintXxx constants) to the
	// number of times that pattern fired. Hints that did not fire are
	// not present in the map.
	Counts map[string]int
}

// Total reports the total number of substitutions across all hints.
// Useful for the UI to show a single "N items redacted" badge.
func (r Redacted) Total() int {
	n := 0
	for _, c := range r.Counts {
		n += c
	}
	return n
}

// Apply runs every applicable redaction pattern over payload and
// returns the result. The patterns are applied in the order described
// in patterns.go — most specific first — so the broader rules cannot
// clobber a more specific hint that an earlier rule produced.
//
// payload is converted to bytes as follows:
//   - string and []byte are passed through unchanged so the redactor
//     does not introduce JSON quoting into a payload that is already
//     human-readable text (an error message, a stack trace, a single
//     log line).
//   - nil produces an empty payload.
//   - everything else is rendered via json.MarshalIndent with two-
//     space indent so the "Context sent" pane is legible to humans.
//     If marshalling fails (e.g. an unmarshallable channel field), the
//     redactor falls back to fmt.Sprintf("%+v", payload) rather than
//     returning an error — failing closed on the outbound path means
//     dropping the call, which is worse than redacting a coarser
//     representation.
//
// Apply is safe for concurrent use: the package-level compiled
// regexes are immutable and the counts map is built fresh per call.
func Apply(payload any, opts Opts) Redacted {
	data := toBytes(payload)
	counts := make(map[string]int)
	for _, p := range buildPatterns(opts) {
		data = p.apply(data, counts)
	}
	return Redacted{Text: string(data), Counts: counts}
}

func toBytes(payload any) []byte {
	switch v := payload.(type) {
	case nil:
		return nil
	case string:
		return []byte(v)
	case []byte:
		// Defensive copy so a downstream regex replace cannot mutate
		// the caller's buffer when no pattern matches.
		out := make([]byte, len(v))
		copy(out, v)
		return out
	default:
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return []byte(fmt.Sprintf("%+v", payload))
		}
		return b
	}
}
