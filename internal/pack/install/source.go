package install

import (
	"path/filepath"
	"strings"
)

// source is the parsed form of the user-supplied install argument. The
// install commands take a single positional argument that may be either a
// git URL (optionally with a `#<ref>` pin) or a local filesystem path;
// parseSource resolves which.
type source struct {
	// isLocal reports whether the argument referred to a path on the
	// local filesystem (one of: absolute, starts with `./` or `../`,
	// or — only when no URL marker is present — bare relative path that
	// resolves to an existing directory).
	isLocal bool

	// path is the absolute, cleaned filesystem path when isLocal is true.
	path string

	// url is the bare git URL (with any `#ref` suffix stripped) when
	// isLocal is false.
	url string

	// ref is the optional refspec extracted from a `<url>#<ref>` form.
	// Empty when the user did not pin a ref.
	ref string

	// raw is the verbatim argument the user supplied; preserved for
	// diagnostics so error messages echo what the user actually typed.
	raw string
}

// parseSource classifies arg as either a git URL with an optional
// `#<ref>` pin, or a local filesystem path. Path-shaped inputs (those
// starting with `/`, `./`, or `../`, or pointing at an existing local
// directory) take precedence — git URLs containing a `#` would
// otherwise be ambiguous, but the local-path check rules them out
// before the `#` split.
//
// The function does not stat the network. Local paths are resolved to
// an absolute, cleaned form so subsequent code can rely on
// `filepath.Clean(s.path) == s.path`.
func parseSource(arg string) (source, error) {
	src := source{raw: arg}
	if arg == "" {
		return src, &parseError{arg: arg, reason: "empty source"}
	}

	// A path-shaped argument is unambiguously local; do not try to
	// interpret embedded `#` characters as ref pins for these — a
	// directory may legitimately contain a literal `#`.
	if isExplicitLocalPath(arg) {
		abs, err := filepath.Abs(arg)
		if err != nil {
			return src, &parseError{arg: arg, reason: "resolve local path: " + err.Error()}
		}
		src.isLocal = true
		src.path = filepath.Clean(abs)
		return src, nil
	}

	// URL-shaped: split off the optional `#<ref>` pin.
	url, ref := splitRef(arg)
	if url == "" {
		return src, &parseError{arg: arg, reason: "missing URL"}
	}
	src.url = url
	src.ref = ref

	// Reject anything that doesn't look at all URL-shaped — this catches
	// bare names like "my-pack" that the user probably meant as a path
	// to a sibling directory but forgot the `./` prefix.
	if !looksLikeRemote(url) {
		return src, &parseError{
			arg:    arg,
			reason: "does not look like a git URL; prefix local paths with './'",
		}
	}
	// Argv-smuggling defence: a URL or ref that begins with `-` would,
	// if it ever reached git's argv, be parsed as a flag — the
	// `--upload-pack=<cmd>` variant is a documented RCE channel for
	// `git clone`. We reject these here as well as terminating option
	// parsing on the git side via `--end-of-options`, so a bug in
	// either layer alone is not exploitable.
	if strings.HasPrefix(url, "-") {
		return src, &parseError{arg: arg, reason: "URL must not start with '-'"}
	}
	if strings.HasPrefix(ref, "-") {
		return src, &parseError{arg: arg, reason: "ref must not start with '-'"}
	}
	return src, nil
}

// isExplicitLocalPath reports whether arg has the form of a filesystem
// path. We accept absolute paths and explicit-relative paths (`./`,
// `../`); a bare name like `acme/foo` is ambiguous (could be a path or
// a shorthand for a remote) and is left to looksLikeRemote.
func isExplicitLocalPath(arg string) bool {
	if filepath.IsAbs(arg) {
		return true
	}
	if strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../") {
		return true
	}
	// Windows drive-letter paths.
	if len(arg) >= 2 && arg[1] == ':' {
		c := arg[0]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return true
		}
	}
	return false
}

// splitRef separates a `<url>#<ref>` argument into its components. A
// missing `#` returns (arg, ""). Only the first `#` is treated as a
// separator so a ref containing `#` (rare) survives untouched.
func splitRef(arg string) (url, ref string) {
	if i := strings.Index(arg, "#"); i >= 0 {
		return arg[:i], arg[i+1:]
	}
	return arg, ""
}

// looksLikeRemote reports whether s has the shape of a git remote we
// can hand to `git clone`. Three forms are recognised:
//   - `<scheme>://...` (https, http, git, ssh, file)
//   - `git@host:path` (scp-style SSH)
//   - `user@host:path` more generally
//
// We deliberately do not validate the URL beyond shape — git itself is
// the source of truth for what it will accept.
func looksLikeRemote(s string) bool {
	if strings.Contains(s, "://") {
		return true
	}
	// scp-like: user@host:path. Detect a colon that isn't a Windows
	// drive separator. Windows drives are already caught upstream.
	if i := strings.Index(s, "@"); i > 0 {
		if j := strings.Index(s[i:], ":"); j > 0 {
			return true
		}
	}
	return false
}

// parseError is the typed error returned from parseSource. Keeping it
// distinct from a fmt.Errorf wrap lets future callers (e.g. the slash
// dispatcher in cli.go) tailor the error message without parsing
// strings.
type parseError struct {
	arg    string
	reason string
}

func (e *parseError) Error() string {
	return "install: source " + quote(e.arg) + ": " + e.reason
}

// quote wraps s in double quotes for human-readable error output. We
// avoid %q's escape rules here because URLs and paths look cleaner
// without backslash escapes.
func quote(s string) string { return `"` + s + `"` }
