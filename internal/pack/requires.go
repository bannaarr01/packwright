package pack

import (
	"fmt"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/bannaarr01/packwright/internal/version"
)

// ModulePackwright is the requires-map key for the app version constraint
// declared in a pack.yaml's `requires:` section. ADR-0028 enumerates this
// alongside ModuleManifestSchema; both keys are exported as named constants
// so pack authors get compile-time feedback if they change.
const (
	// ModulePackwright keys the running-app SemVer constraint
	// (e.g. ">=0.4.0 <0.6.0").
	ModulePackwright = "packwright"

	// ModuleManifestSchema keys the manifest-schema version token list
	// (e.g. "v1" or "v1, v2"). Comma-separated entries express "or".
	ModuleManifestSchema = "packwright.manifest"
)

// defaultCurrentManifestMajor mirrors manifest.CurrentSchemaMajor so this
// package can do the manifest-schema check without importing
// internal/manifest. Bump in lockstep with manifest.CurrentSchemaMajor — the
// requires_test.go suite asserts the two remain aligned so a drift in one
// place fails CI here.
const defaultCurrentManifestMajor = 1

// RequiresError describes a `requires:` mismatch surfaced at pack load. The
// fields mirror the ADR-0028 error template so the front-ends can render the
// message without reformatting; the typed shape lets callers branch on the
// failing module (app version vs. manifest schema).
type RequiresError struct {
	// PackName is the offending pack's name (the `name:` from pack.yaml).
	// Empty when the caller does not yet know it — the formatted message
	// degrades gracefully but the typed information is still useful.
	PackName string

	// Module is the key inside `requires:` whose constraint failed, one of
	// ModulePackwright or ModuleManifestSchema.
	Module string

	// Constraint is the constraint string verbatim from pack.yaml.
	Constraint string

	// Have is what the runtime offers — the running app version for
	// ModulePackwright, the canonical "vN" token for ModuleManifestSchema.
	Have string
}

// Error formats the RequiresError as the message documented in ADR-0028. The
// "either …" remediation block is left to the renderer that has access to
// the surrounding install context (pack URL, app upgrade link).
func (e *RequiresError) Error() string {
	pack := e.PackName
	if pack == "" {
		pack = "<unknown>"
	}
	return fmt.Sprintf("pack %q requires %s %s; you have %s",
		pack, e.Module, e.Constraint, e.Have)
}

// CheckOptions tunes Check's behaviour. The zero value enforces every
// supported stream (packwright + packwright.manifest); the migrate-manifests
// command sets IgnoreManifest so it can open a pack that declares an old
// schema constraint and rewrite it in place.
type CheckOptions struct {
	// RunningAppVersion overrides version.Get(). Empty means "use the
	// process-wide value" — almost every caller leaves this empty.
	RunningAppVersion string

	// CurrentManifestMajor overrides defaultCurrentManifestMajor. Zero
	// means "use the default". Tests and the migrate command set this
	// explicitly so behaviour is deterministic across version bumps.
	CurrentManifestMajor int

	// IgnoreManifest skips the packwright.manifest constraint check while
	// still enforcing packwright. Set by migrate-manifests so an old-
	// schema pack can be opened just long enough to rewrite it.
	IgnoreManifest bool
}

// Check returns nil if requires is compatible with the running app and the
// manifest schema this build can load; otherwise it returns a *RequiresError
// pinpointing the first failing module.
//
// A nil or empty requires map is treated as "no constraints" — packs without
// a `requires:` section load unconditionally, mirroring the pre-PR-02 loader
// behaviour so existing fixtures keep working.
//
// packName is included in the formatted error so the ADR-0028 template
// renders correctly. Pass empty when the caller has not yet decoded the
// pack name.
func Check(packName string, requires map[string]string, opts CheckOptions) error {
	if len(requires) == 0 {
		return nil
	}

	running := opts.RunningAppVersion
	if running == "" {
		running = version.Get()
	}

	if c, ok := requires[ModulePackwright]; ok {
		if err := checkAppVersion(packName, c, running); err != nil {
			return err
		}
	}

	if !opts.IgnoreManifest {
		if c, ok := requires[ModuleManifestSchema]; ok {
			major := opts.CurrentManifestMajor
			if major == 0 {
				major = defaultCurrentManifestMajor
			}
			if err := checkManifestSchema(packName, c, major); err != nil {
				return err
			}
		}
	}

	return nil
}

// checkAppVersion compares the running app version against constraint using
// SemVer rules. Dev builds bypass the check: a developer hacking on the
// binary wants to load any pack, and the failure mode there is them seeing
// real load-time errors rather than getting blocked at the constraint gate.
func checkAppVersion(packName, constraint, running string) error {
	if !version.IsRelease(running) {
		return nil
	}
	normalized := version.Normalize(running)
	ok, err := matchSemverConstraint(constraint, normalized)
	if err != nil {
		return fmt.Errorf("pack %q: parse %s constraint %q: %w",
			packName, ModulePackwright, constraint, err)
	}
	if !ok {
		return &RequiresError{
			PackName:   packName,
			Module:     ModulePackwright,
			Constraint: constraint,
			Have:       running,
		}
	}
	return nil
}

// checkManifestSchema compares the manifest-schema constraint against the
// integer schema major this build can load. The constraint syntax is one or
// more comma-separated tokens of the form "vN"; a token matches when N
// equals currentMajor. Whitespace inside the list is tolerated.
func checkManifestSchema(packName, constraint string, currentMajor int) error {
	constraint = strings.TrimSpace(constraint)
	if constraint == "" {
		return nil
	}
	want := fmt.Sprintf("v%d", currentMajor)
	for _, tok := range strings.Split(constraint, ",") {
		if strings.TrimSpace(tok) == want {
			return nil
		}
	}
	return &RequiresError{
		PackName:   packName,
		Module:     ModuleManifestSchema,
		Constraint: constraint,
		Have:       want,
	}
}

// matchSemverConstraint reports whether running satisfies constraint. The
// constraint syntax is a space-separated list of comparators — e.g.
// ">=0.4.0 <0.6.0" — combined with AND semantics. An empty constraint
// matches anything. The supported comparator operators are described in
// matchSemverComparator's doc comment.
func matchSemverConstraint(constraint, running string) (bool, error) {
	fields := strings.Fields(constraint)
	if len(fields) == 0 {
		return true, nil
	}
	for _, f := range fields {
		ok, err := matchSemverComparator(f, running)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

// matchSemverComparator evaluates one comparator against running. running
// must already be in v-prefixed semver form (use version.Normalize). The
// recognised operators are:
//
//	""     equality (synonym of "=")
//	"="    equality
//	"=="   equality
//	"!="   inequality
//	">"    strictly greater
//	">="   greater-or-equal
//	"<"    strictly less
//	"<="   less-or-equal
//	"^"    same major, >= target (npm-style caret)
//	"~"    same major+minor, >= target (npm-style tilde)
//
// Unrecognised operators are an error; the manifest authors get a parse-
// time complaint instead of silent over-permissive matching.
func matchSemverComparator(comp, running string) (bool, error) {
	if comp == "" {
		return true, nil
	}
	op, target := splitComparator(comp)
	if target == "" {
		return false, fmt.Errorf("empty version in comparator %q", comp)
	}
	target = version.Normalize(target)
	if !semver.IsValid(target) {
		return false, fmt.Errorf("invalid version %q in comparator %q", target, comp)
	}
	cmp := semver.Compare(running, target)
	switch op {
	case "", "=", "==":
		return cmp == 0, nil
	case "!=":
		return cmp != 0, nil
	case ">":
		return cmp > 0, nil
	case ">=":
		return cmp >= 0, nil
	case "<":
		return cmp < 0, nil
	case "<=":
		return cmp <= 0, nil
	case "^":
		return cmp >= 0 && semver.Major(running) == semver.Major(target), nil
	case "~":
		return cmp >= 0 && semver.MajorMinor(running) == semver.MajorMinor(target), nil
	default:
		return false, fmt.Errorf("unknown comparator operator %q in %q", op, comp)
	}
}

// splitComparator splits a comparator into its operator prefix and version
// target. Two-character operators are matched first so ">=" is not confused
// with ">".
func splitComparator(comp string) (op, target string) {
	for _, two := range []string{">=", "<=", "!=", "=="} {
		if strings.HasPrefix(comp, two) {
			return two, comp[len(two):]
		}
	}
	switch comp[0] {
	case '>', '<', '=', '^', '~':
		return string(comp[0]), comp[1:]
	}
	return "", comp
}
