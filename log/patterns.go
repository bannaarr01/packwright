package log

import (
	"bytes"
	"regexp"
)

// pattern bundles a regex with a closure that applies it to the input. The
// closure form (instead of a fixed regex + replacement template) lets a
// pattern make per-match decisions — in particular, rules 6 and 7 below
// must skip values that have already been redacted by an earlier, more
// specific rule, which RE2 cannot express on its own because it lacks
// negative lookahead.
type pattern struct {
	apply func(in []byte) []byte
}

// staticRepl builds a pattern that replaces every regex match in input
// with the given replacement template. The template may reference capture
// groups via Go's $-syntax (see Regexp.Expand).
func staticRepl(re *regexp.Regexp, repl []byte) pattern {
	return pattern{
		apply: func(in []byte) []byte {
			return re.ReplaceAll(in, repl)
		},
	}
}

// fieldRepl builds a pattern for "key+separator+value" rules where group 1
// captures the prefix to preserve and the remainder of the match is the
// value to redact. The value is left alone when it already looks
// redacted ("<redacted:…>" or `"<redacted:…>"`), so a later, broader rule
// does not clobber a more specific hint that an earlier rule produced.
func fieldRepl(re *regexp.Regexp, hint string, quoted bool) pattern {
	var redacted []byte
	if quoted {
		redacted = []byte(`"<redacted:` + hint + `>"`)
	} else {
		redacted = []byte(`<redacted:` + hint + `>`)
	}
	return pattern{
		apply: func(in []byte) []byte {
			return re.ReplaceAllFunc(in, func(match []byte) []byte {
				sub := re.FindSubmatchIndex(match)
				if len(sub) < 4 {
					return match
				}
				prefixEnd := sub[3] // end of group 1, start of value
				value := match[prefixEnd:]
				trimmed := bytes.TrimLeft(value, " \t")
				if bytes.HasPrefix(trimmed, []byte(`<redacted:`)) ||
					bytes.HasPrefix(trimmed, []byte(`"<redacted:`)) {
					return match
				}
				out := make([]byte, 0, prefixEnd+len(redacted))
				out = append(out, match[:prefixEnd]...)
				out = append(out, redacted...)
				return out
			})
		},
	}
}

// defaultPatterns returns the ADR-0018 pattern set in a fresh slice. The
// set is rebuilt on each call so callers may mutate the result without
// affecting other Redactor instances.
//
// Patterns are applied in declaration order. The most specific (AWS access
// keys, JWTs, Bearer headers, AWS secret-key / session-token contexts)
// come first so their classification hints survive the later, broader
// sensitive-name field rules.
func defaultPatterns() []pattern {
	return []pattern{
		// 1. AWS access key IDs (long-term AKIA, short-term ASIA).
		// The shape is fixed: 4-letter prefix + 16 uppercase alphanumeric.
		staticRepl(
			regexp.MustCompile(`A[KS]IA[0-9A-Z]{16}`),
			[]byte(`<redacted:aws_access_key>`),
		),
		// 2. JWT — three base64url segments separated by dots, where the
		// header and payload start with "eyJ" (the base64 of `{"`).
		staticRepl(
			regexp.MustCompile(`eyJ[A-Za-z0-9_\-]+\.eyJ[A-Za-z0-9_\-]+\.[A-Za-z0-9_\-]+`),
			[]byte(`<redacted:jwt>`),
		),
		// 3. Bearer-token headers. The literal "Bearer" word is preserved
		// (as $1) so a reader still sees the auth scheme; only the token
		// is replaced.
		staticRepl(
			regexp.MustCompile(`(?i)(Bearer)[ \t]+[A-Za-z0-9_\-\.=/+]+`),
			[]byte(`$1 <redacted:bearer>`),
		),
		// 4. AWS secret access key — 40 base64-ish characters next to a
		// "secret key" context word. We anchor by context to avoid
		// redacting any arbitrary 40-char base64 string; the raw shape
		// alone is too common in template hashes, ARNs, and stack traces.
		fieldRepl(
			regexp.MustCompile(`((?i)(?:aws[_\-]?)?secret[_\-]?(?:access[_\-]?)?key["':=\s]+)[A-Za-z0-9/+]{40}`),
			"aws_secret_key", false,
		),
		// 5. AWS session token — long base64 with `=` padding, anchored
		// to a session-token context word.
		fieldRepl(
			regexp.MustCompile(`((?i)session[_\-]?token["':=\s]+)[A-Za-z0-9/+]{100,}={1,2}`),
			"session_token", false,
		),
		// 6. JSON keys whose name contains password|token|secret|key|
		// credential (case-insensitive). Matches `"keyname":<value>` and
		// replaces only the value, preserving the key for log readability.
		// Values may be JSON strings, true/false/null, or numbers. Skips
		// values already wrapped in a <redacted:…> marker so an earlier,
		// more specific rule's hint is preserved.
		fieldRepl(
			regexp.MustCompile(
				`("[A-Za-z0-9_\-]*(?i:password|token|secret|key|credential)[A-Za-z0-9_\-]*"\s*:\s*)`+
					`(?:"(?:[^"\\]|\\.)*"|true|false|null|-?\d+(?:\.\d+)?(?:[eE][-+]?\d+)?)`,
			),
			"field", true,
		),
		// 7. slog text-format keys (`key=value`) where the key name
		// contains one of the sensitive words. Matches both quoted (`"…"`)
		// and bare (run of non-whitespace) values. Skips already-redacted
		// values for the same reason as rule 6.
		fieldRepl(
			regexp.MustCompile(
				`(\b[A-Za-z0-9_\-]*(?i:password|token|secret|key|credential)[A-Za-z0-9_\-]*=)`+
					`("(?:[^"\\]|\\.)*"|\S+)`,
			),
			"field", false,
		),
	}
}
