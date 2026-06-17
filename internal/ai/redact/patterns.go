// Package redact implements the Packwright outbound AI redactor per
// ADR-0037. Every payload bound for the LLM provider passes through
// Apply before HTTP send so secrets, AWS keys, JWTs, and (by default)
// AWS account IDs never leave the machine. The package is deliberately
// independent of the local-log redactor (ADR-0018) — same shapes show
// up in both, but the threat models differ and the two implementations
// are kept separate on purpose.
//
// File layout:
//
//   - patterns.go  — the ordered pattern set and the small machinery
//     that lets each pattern report how many matches it scrubbed.
//   - redact.go    — public types (Opts, Redacted), DefaultOpts, and
//     Apply, the central entry point providers must call before HTTP
//     send.
//   - context.go   — typed builders that assemble the initial context
//     block for each "Ask AI" entry point (error card, monitor panel,
//     blank chat) and run it through Apply.
package redact

import (
	"bytes"
	"regexp"
)

// pattern bundles a regex with the closure that applies it to a byte
// buffer and reports how many matches it scrubbed. The closure form
// (rather than a fixed regex + replacement template) lets the broader
// field-name rules skip values that an earlier, more specific rule
// already redacted, which RE2 cannot express on its own because it has
// no negative lookahead.
type pattern struct {
	hint  string
	apply func(in []byte, counts map[string]int) []byte
}

// staticRepl builds a pattern that replaces every regex match with a
// fixed replacement. The full match is replaced; capture groups are not
// preserved. Use templateRepl when a portion of the match must survive.
func staticRepl(hint string, re *regexp.Regexp, replacement []byte) pattern {
	return pattern{
		hint: hint,
		apply: func(in []byte, counts map[string]int) []byte {
			matches := re.FindAllIndex(in, -1)
			if len(matches) == 0 {
				return in
			}
			counts[hint] += len(matches)
			return re.ReplaceAll(in, replacement)
		},
	}
}

// templateRepl builds a pattern that replaces every regex match using
// Go's $-syntax replacement template, so capture groups in the regex
// can survive into the output. Used for the Bearer rule, which must
// preserve the literal "Bearer" prefix.
func templateRepl(hint string, re *regexp.Regexp, tmpl []byte) pattern {
	return pattern{
		hint: hint,
		apply: func(in []byte, counts map[string]int) []byte {
			matches := re.FindAllIndex(in, -1)
			if len(matches) == 0 {
				return in
			}
			counts[hint] += len(matches)
			return re.ReplaceAll(in, tmpl)
		},
	}
}

// fieldRepl builds a "key+separator+value" rule. Group 1 is the
// preserved key prefix (e.g. `"password":` for JSON or `password=` for
// text format) and the remainder of the match is the value to scrub.
// Values that already look like a redaction marker are left untouched,
// so a later broad rule does not clobber a more specific hint that an
// earlier rule produced.
func fieldRepl(hint string, re *regexp.Regexp, quoted bool) pattern {
	var replacement []byte
	if quoted {
		replacement = []byte(`"<redacted:` + hint + `>"`)
	} else {
		replacement = []byte(`<redacted:` + hint + `>`)
	}
	return pattern{
		hint: hint,
		apply: func(in []byte, counts map[string]int) []byte {
			n := 0
			out := re.ReplaceAllFunc(in, func(match []byte) []byte {
				sub := re.FindSubmatchIndex(match)
				if len(sub) < 4 {
					return match
				}
				prefixEnd := sub[3]
				value := match[prefixEnd:]
				trimmed := bytes.TrimLeft(value, " \t")
				if bytes.HasPrefix(trimmed, []byte(`<redacted:`)) ||
					bytes.HasPrefix(trimmed, []byte(`"<redacted:`)) {
					return match
				}
				n++
				buf := make([]byte, 0, prefixEnd+len(replacement))
				buf = append(buf, match[:prefixEnd]...)
				buf = append(buf, replacement...)
				return buf
			})
			if n > 0 {
				counts[hint] += n
			}
			return out
		},
	}
}

// Pre-compiled built-in regexes. These are package-level vars so each
// Apply call reuses the same compiled state (regexp.Regexp is safe for
// concurrent use).
var (
	reAWSAccessKey = regexp.MustCompile(`A[KS]IA[0-9A-Z]{16}`)
	reJWT          = regexp.MustCompile(`eyJ[A-Za-z0-9_\-]+\.eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+`)
	reBearer       = regexp.MustCompile(`(?i)(Bearer)[ \t]+\S+`)
	reAWSSecret    = regexp.MustCompile(
		`((?i)(?:aws[_\-]?)?secret[_\-]?(?:access[_\-]?)?key["':=\s]+)[A-Za-z0-9/+]{40}`,
	)
	reAWSSession = regexp.MustCompile(
		`((?i)session[_\-]?token["':=\s]+)[A-Za-z0-9/+]{100,}={0,2}`,
	)
	reSecretFieldJSON = regexp.MustCompile(
		`("[A-Za-z0-9_\-]*(?i:password|token|secret|key|credential)[A-Za-z0-9_\-]*"\s*:\s*)` +
			`(?:"(?:[^"\\]|\\.)*"|true|false|null|-?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?)`,
	)
	reSecretFieldText = regexp.MustCompile(
		`(\b[A-Za-z0-9_\-]*(?i:password|token|secret|key|credential)[A-Za-z0-9_\-]*=)` +
			`("(?:[^"\\]|\\.)*"|\S+)`,
	)
	reAccountID = regexp.MustCompile(`\b\d{12}\b`)
	rePrivateIP = regexp.MustCompile(
		`\b(?:10\.\d{1,3}\.\d{1,3}\.\d{1,3}` +
			`|172\.(?:1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}` +
			`|192\.168\.\d{1,3}\.\d{1,3})\b`,
	)
)

// hint constants are the keys that show up in Redacted.Counts. Using
// constants instead of bare strings means a renderer of the "Context
// sent" pane can switch over a closed set without rebuild churn when
// the patterns evolve.
const (
	HintAWSAccessKey    = "aws_access_key"
	HintAWSSecret       = "aws_secret"
	HintAWSSessionToken = "aws_session_token"
	HintJWT             = "jwt"
	HintBearer          = "bearer"
	HintSecretField     = "secret_field"
	HintFormSecret      = "form_secret"
	HintAccount         = "account"
	HintPrivateIP       = "private_ip"
	HintInternalHost    = "internal_host"
)

// buildPatterns assembles the ordered pattern list for the given Opts.
// Order matters: the most specific rules run first so their hints
// survive the broader field-name and account-ID rules that come later.
// Optional patterns (account IDs, private IPs, internal hosts, named
// form secrets) only appear when Opts enables them, so the cost of a
// disabled toggle is zero.
func buildPatterns(opts Opts) []pattern {
	pats := []pattern{
		// 1. AWS access key IDs (long-term AKIA, short-term ASIA).
		staticRepl(HintAWSAccessKey, reAWSAccessKey, []byte(`<redacted:aws_access_key>`)),
		// 2. JWTs — three base64url segments where the first two start
		// with `eyJ` (base64 of `{"`).
		staticRepl(HintJWT, reJWT, []byte(`<redacted:jwt>`)),
		// 3. Bearer-token headers. The literal "Bearer" is preserved
		// via $1 so the auth scheme is still readable.
		templateRepl(HintBearer, reBearer, []byte(`$1 <redacted>`)),
		// 4. AWS secret access key — 40-char base64 next to a "secret
		// key" context word. Context-anchored to avoid scrubbing every
		// 40-char base64 string in ARNs or stack traces.
		fieldRepl(HintAWSSecret, reAWSSecret, false),
		// 5. AWS session token — long base64 next to a "session token"
		// context word.
		fieldRepl(HintAWSSessionToken, reAWSSession, false),
	}

	// 6. Per-session form-field secrets (ADR-0007). One pattern pair
	// per registered field — JSON and text format. Run BEFORE the
	// generic secret-field rule so the more specific "form_secret"
	// hint wins.
	for _, name := range opts.SecretFields {
		if name == "" {
			continue
		}
		quoted := regexp.QuoteMeta(name)
		jsonRE := regexp.MustCompile(
			`((?i)"` + quoted + `"\s*:\s*)` +
				`(?:"(?:[^"\\]|\\.)*"|true|false|null|-?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?)`,
		)
		textRE := regexp.MustCompile(
			`((?i)\b` + quoted + `=)` +
				`("(?:[^"\\]|\\.)*"|\S+)`,
		)
		pats = append(pats,
			fieldRepl(HintFormSecret, jsonRE, true),
			fieldRepl(HintFormSecret, textRE, false),
		)
	}

	// 7. Generic password|token|secret|key|credential field names.
	// JSON form preserves the key for log readability and scrubs the
	// value; text form does the same for `key=value` payloads.
	pats = append(pats,
		fieldRepl(HintSecretField, reSecretFieldJSON, true),
		fieldRepl(HintSecretField, reSecretFieldText, false),
	)

	// 8. AWS account IDs (12-digit). Always-redacted by default; users
	// in single-account or homelab setups can disable.
	if opts.RedactAccountIDs {
		pats = append(pats, staticRepl(HintAccount, reAccountID, []byte(`<account>`)))
	}

	// 9. RFC1918 private addresses. Off by default; useful when the
	// VPC CIDRs themselves are sensitive (some org security models).
	if opts.RedactPrivateIPs {
		pats = append(pats, staticRepl(HintPrivateIP, rePrivateIP, []byte(`<private-ip>`)))
	}

	// 10. User-configured internal-host RE2 patterns. Off until the
	// user supplies at least one pattern. Invalid patterns are
	// silently skipped — the redactor must never fail open on the
	// outbound path.
	for _, p := range opts.InternalHostPatterns {
		if p == "" {
			continue
		}
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		pats = append(pats, staticRepl(HintInternalHost, re, []byte(`<internal-host>`)))
	}

	return pats
}
