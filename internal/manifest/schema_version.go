package manifest

import (
	"fmt"
	"strconv"
	"strings"
)

// SchemaVersionPrefix is the fixed prefix every schema_version string
// carries per ADR-0028. The token following it is the integer schema major
// (e.g. "v1", "v2"). Exported so the migrate-manifests command and any
// future authoring tools can build / parse schema_version values without
// re-encoding the literal.
const SchemaVersionPrefix = "packwright.manifest."

// CurrentSchemaMajor is the integer schema major this build of Packwright
// can load. Bump alongside a new SchemaVersionVN constant when shipping a
// breaking schema change; ADR-0028 describes the migration policy and the
// matching migrate-manifests command flow.
const CurrentSchemaMajor = 1

// supportedSchemaMajors is the set of schema majors checkSchemaVersion
// accepts. Add an integer here once the manifest package is forward-
// compatible with that major; the migrate-manifests command rewrites older
// majors to CurrentSchemaMajor in place.
var supportedSchemaMajors = map[int]struct{}{
	1: {},
}

// schemaVersionCheck is the hook Load invokes (one line in loader.go) to
// enforce the schema_version field. The default points at the package's
// checkSchemaVersion; tests may swap it via t.Cleanup to widen or narrow
// the supported set without touching loader.go. The MVP-1 PR-05 loader
// reserved the call site for this PR — see ADR-0028.
var schemaVersionCheck = checkSchemaVersion

// ParseSchemaMajor extracts the integer major from a schema_version string.
//
//	ParseSchemaMajor("packwright.manifest.v1") → (1, nil)
//	ParseSchemaMajor("packwright.manifest.v2") → (2, nil)
//	ParseSchemaMajor("nonsense")               → (0, error)
//
// The migrate-manifests command uses this to classify on-disk manifests
// before deciding which migration step to run.
func ParseSchemaMajor(s string) (int, error) {
	if !strings.HasPrefix(s, SchemaVersionPrefix) {
		return 0, fmt.Errorf("must start with %q (got %q)", SchemaVersionPrefix, s)
	}
	tail := s[len(SchemaVersionPrefix):]
	if len(tail) < 2 || (tail[0] != 'v' && tail[0] != 'V') {
		return 0, fmt.Errorf("missing version token after prefix (got %q)", s)
	}
	n, err := strconv.Atoi(tail[1:])
	if err != nil {
		return 0, fmt.Errorf("parse major from %q: %w", tail, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("negative major in %q", tail)
	}
	return n, nil
}

// FormatSchemaVersion returns the canonical schema_version string for an
// integer major: 1 → "packwright.manifest.v1". Used by migrate-manifests
// when rewriting old manifests to the current schema.
func FormatSchemaVersion(major int) string {
	return fmt.Sprintf("%sv%d", SchemaVersionPrefix, major)
}

// checkSchemaVersion verifies m.SchemaVersion names a schema major this
// build can load. It returns nil for the empty string so Validate can
// produce its own (better-located) "is required" error for the missing-
// field case — callers that want a stricter check on the empty input
// should pre-screen for it.
//
// A non-empty but unsupported version is reported as a *ValidationError
// with Path "schema_version" so the loader's existing error-shape contract
// (asserted in loader_test.go) keeps holding.
func checkSchemaVersion(m *Manifest) error {
	if m == nil {
		return invalid("", "manifest is nil")
	}
	if m.SchemaVersion == "" {
		return nil
	}
	major, err := ParseSchemaMajor(m.SchemaVersion)
	if err != nil {
		return invalid("schema_version", err.Error())
	}
	if _, ok := supportedSchemaMajors[major]; !ok {
		return invalid("schema_version",
			fmt.Sprintf("unsupported schema major v%d (this build understands v%d)",
				major, CurrentSchemaMajor))
	}
	return nil
}
