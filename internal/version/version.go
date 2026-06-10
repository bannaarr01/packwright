// Package version exposes the running Packwright app version. The running
// build embeds its version through a package-level variable (overridden via
// -ldflags at release time); tests and runtime call sites read it via Get
// rather than touching the variable directly so the caller graph is easy to
// audit. See ADR-0028 for the three-stream versioning policy this package
// participates in.
package version

import (
	"strings"

	"golang.org/x/mod/semver"
)

// Version is the running Packwright app version. It defaults to "dev" for
// local builds and is overridden at release time via the linker, e.g.:
//
//	go build -ldflags "-X github.com/bannaarr01/packwright/internal/version.Version=v0.4.2" .
//
// Tests should not mutate Version directly; use Set, which returns a restore
// function callers can register with t.Cleanup so the override never leaks
// across tests.
var Version = "dev"

// Dev is the sentinel value Version holds for non-release local builds. It
// is exported so callers can compare without re-hard-coding the literal in
// every caller (e.g. the requires check bypasses constraint matching when
// the running build is Dev).
const Dev = "dev"

// Get returns Version. Prefer this over reading Version directly: the
// indirection makes future swaps (env override, telemetry tagging) a
// one-place change.
func Get() string { return Version }

// Set overrides Version and returns a function that restores the previous
// value. Tests pair Set with t.Cleanup; non-test callers should not use it.
func Set(v string) (restore func()) {
	prev := Version
	Version = v
	return func() { Version = prev }
}

// Normalize returns v with a leading "v" prefix so it matches the form
// golang.org/x/mod/semver expects. Whitespace is trimmed; an empty input is
// returned unchanged so callers can distinguish "no version" from "v".
func Normalize(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if v[0] == 'v' || v[0] == 'V' {
		return "v" + v[1:]
	}
	return "v" + v
}

// IsRelease reports whether v looks like a real semver release version. The
// sentinel Dev returns false, as do empty strings and malformed inputs.
// Callers that gate behaviour on "is this a shipped build" (e.g. telemetry,
// upgrade prompts, requires-constraint enforcement) read this.
func IsRelease(v string) bool {
	if v == "" || v == Dev {
		return false
	}
	return semver.IsValid(Normalize(v))
}
