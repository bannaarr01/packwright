package manifest

import (
	"fmt"
	"strings"
)

// ValidationError is returned by Validate when a manifest is well-formed YAML
// but violates a cross-field rule. Path locates the offending field using a
// dotted, index-suffixed notation (e.g. "form[2].depends_on[0]"); Reason is a
// human-readable explanation. Tests assert on this typed error rather than
// matching error strings.
type ValidationError struct {
	Path   string
	Reason string
}

// ErrDraftNotPromoted is the typed error Validate returns when the manifest
// passes every structural rule but carries `_draft: true` (ADR-0047). The
// action engine surfaces this through its existing error pipeline so the UI
// can render "Promote this draft before deploying" instead of a generic
// validation failure. Load tolerates the error specifically so a freshly
// scaffolded or copied draft still appears in the watcher / sidebar.
//
// Callers detect it with errors.As; the Slash field carries the manifest's
// slash so the surface text reads naturally.
type ErrDraftNotPromoted struct {
	Slash string
}

// Error renders the draft-not-promoted error. The phrasing names the slash
// so error cards in the TUI / GUI carry enough context to act on.
func (e *ErrDraftNotPromoted) Error() string {
	if e == nil || e.Slash == "" {
		return "manifest: draft has not been promoted — run /promote-template before deploying"
	}
	return fmt.Sprintf("manifest: %s: draft has not been promoted — run /promote-template %s before deploying", e.Slash, e.Slash)
}

// Error formats the validation error as "manifest: <path>: <reason>". The
// "manifest:" prefix mirrors the Load wrapper so concatenated error chains
// still read naturally.
func (e *ValidationError) Error() string {
	if e.Path == "" {
		return "manifest: " + e.Reason
	}
	return fmt.Sprintf("manifest: %s: %s", e.Path, e.Reason)
}

// invalid is a small constructor that keeps Validate call sites tidy.
func invalid(path, reason string) error {
	return &ValidationError{Path: path, Reason: reason}
}

// Validate runs every cross-field rule against m and returns the first
// violation. The checks fall into three groups: top-level required fields,
// kind/section coupling, and per-Field structural rules. Loader callers do
// not normally call Validate directly — Load runs it automatically — but it
// is exported so tests and authoring tools can validate a Manifest assembled
// in memory.
func Validate(m *Manifest) error {
	if m == nil {
		return invalid("", "manifest is nil")
	}

	if m.SchemaVersion == "" {
		return invalid("schema_version", "is required")
	}
	if m.SchemaVersion != SchemaVersionV1 {
		return invalid("schema_version",
			fmt.Sprintf("unsupported version %q (expected %q)", m.SchemaVersion, SchemaVersionV1))
	}

	switch m.Kind {
	case KindResource, KindShell, KindMonitor, KindComposite:
		// recognised
	case "":
		return invalid("kind", "is required")
	default:
		return invalid("kind", fmt.Sprintf("unknown kind %q", m.Kind))
	}

	if m.Slash == "" {
		return invalid("slash", "is required")
	}
	if !strings.HasPrefix(m.Slash, "/") {
		return invalid("slash", fmt.Sprintf("must start with %q (got %q)", "/", m.Slash))
	}
	if strings.ContainsAny(m.Slash, " \t\n") {
		return invalid("slash", "must not contain whitespace")
	}
	if m.Title == "" {
		return invalid("title", "is required")
	}

	if err := validateKindSections(m); err != nil {
		return err
	}
	if err := validateFields(m.Form); err != nil {
		return err
	}
	if err := validateScaling(m.Scaling, m.Form); err != nil {
		return err
	}

	// Draft check runs last so the error reports a slash that already
	// passed structural validation; if a draft is structurally broken,
	// users see the broken-field error first (the more actionable one)
	// and the draft-not-promoted error second on the next attempt.
	if IsDraft(m) {
		return &ErrDraftNotPromoted{Slash: m.Slash}
	}
	return nil
}

// validateScaling enforces the single rule the scaling block carries on its
// own behalf (ADR-0049): every scaling[].param must resolve to a form[].id.
// The kind/min/max/step/values fields are not checked here — they overlay
// the form's metadata for the /scale UI only, and the scaling package
// validates them at BuildParams time. The form-field set is the source of
// truth for what parameters can be touched at all.
func validateScaling(specs []ScalingSpec, fields []Field) error {
	if len(specs) == 0 {
		return nil
	}
	formIDs := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		formIDs[f.ID] = struct{}{}
	}
	for i, s := range specs {
		path := fmt.Sprintf("scaling[%d].param", i)
		if s.Param == "" {
			return invalid(path, "is required")
		}
		if _, ok := formIDs[s.Param]; !ok {
			return invalid(path,
				fmt.Sprintf("references unknown form field %q", s.Param))
		}
	}
	return nil
}

// validateKindSections enforces which top-level sections each kind may carry.
// resource manifests must declare both template and deploy; the other kinds
// must not carry them (their own sections will land in PR-13 / MVP-2/3).
func validateKindSections(m *Manifest) error {
	if m.Kind == KindResource {
		if m.Template == nil {
			return invalid("template", "is required for kind: resource")
		}
		if m.Template.Kind == "" {
			return invalid("template.kind", "is required")
		}
		if m.Template.Path == "" {
			return invalid("template.path", "is required")
		}
		if m.Deploy == nil {
			return invalid("deploy", "is required for kind: resource")
		}
		if m.Deploy.Driver == "" {
			return invalid("deploy.driver", "is required")
		}
		return nil
	}

	if m.Template != nil {
		return invalid("template", fmt.Sprintf("not allowed for kind: %s", m.Kind))
	}
	if m.Deploy != nil {
		return invalid("deploy", fmt.Sprintf("not allowed for kind: %s", m.Kind))
	}
	return nil
}

// validateFields runs structural checks on the form: known type, non-empty
// unique IDs, enum has values, depends_on references resolve, min/max are
// consistent. Field-rule semantics (regex syntax, distinct-az lookups, etc.)
// belong to the runtime validator registry and are intentionally out of scope
// here.
//
// Field IDs are gathered into allIDs in a first pass so depends_on can refer
// to a field declared later in the form (authors often order by display
// order, not by dependency order). Duplicate IDs are reported with the path
// of the *second* occurrence so the offending entry is obvious in the error.
func validateFields(fields []Field) error {
	allIDs := make(map[string]int, len(fields))
	for i, f := range fields {
		if f.ID == "" {
			return invalid(fmt.Sprintf("form[%d].id", i), "is required")
		}
		if prev, dup := allIDs[f.ID]; dup {
			return invalid(fmt.Sprintf("form[%d].id", i),
				fmt.Sprintf("duplicate id %q (also at form[%d])", f.ID, prev))
		}
		allIDs[f.ID] = i
	}

	for i, f := range fields {
		path := fmt.Sprintf("form[%d]", i)

		if f.Type == "" {
			return invalid(path+".type", "is required")
		}
		if _, ok := knownFieldTypes[f.Type]; !ok {
			return invalid(path+".type", fmt.Sprintf("unknown field type %q", f.Type))
		}

		if f.Type == FieldTypeEnum && len(f.Values) == 0 {
			return invalid(path+".values", "is required for type: enum")
		}

		if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
			return invalid(path+".min",
				fmt.Sprintf("min (%d) must not exceed max (%d)", *f.Min, *f.Max))
		}

		for j, dep := range f.DependsOn {
			depPath := fmt.Sprintf("%s.depends_on[%d]", path, j)
			if dep == "" {
				return invalid(depPath, "is empty")
			}
			if dep == f.ID {
				return invalid(depPath, fmt.Sprintf("field %q cannot depend on itself", dep))
			}
			if _, ok := allIDs[dep]; !ok {
				return invalid(depPath, fmt.Sprintf("references unknown field %q", dep))
			}
		}

		for j, v := range f.Validate {
			if v.Rule == "" {
				return invalid(fmt.Sprintf("%s.validate[%d].rule", path, j), "is required")
			}
		}
	}
	return nil
}
