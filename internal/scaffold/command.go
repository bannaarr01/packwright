package scaffold

import (
	"bytes"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/bannaarr01/packwright/internal/manifest/hints"
	"github.com/bannaarr01/packwright/manifest"
)

// embeddedTemplates bundles every .gotmpl file under templates/ into the
// binary. The parsed Template set is built once at init from this filesystem
// so Generate has no per-call I/O.
//
//go:embed templates/*.gotmpl
var embeddedTemplates embed.FS

// templates is the parsed text/template set. _field.gotmpl defines the shared
// "field" sub-template; the four kind files each render a complete manifest
// and may invoke "field" by name.
var templates = template.Must(
	template.New("scaffold").Funcs(templateFuncs).ParseFS(embeddedTemplates, "templates/*.gotmpl"),
)

// templateFuncs is the helper set every scaffold template can call.
//
//   - q renders any value as a YAML-safe scalar, delegating to yaml.v3 so a
//     string containing colons, hashes, or YAML reserved words is quoted
//     correctly.
//   - renderField turns a single FieldSpec into the YAML block lines that
//     belong under a manifest's `form:` list. Rendering happens in Go so
//     the kind templates stay free of text/template's whitespace pitfalls
//     when chaining sub-templates.
//   - envSorted iterates a string-keyed map deterministically; without this
//     helper, map ranges would produce non-reproducible YAML.
var templateFuncs = template.FuncMap{
	"q":           yamlScalar,
	"renderField": renderField,
	"envSorted":   sortedStringMap,
}

// Generate renders the canonical YAML for the manifest described by spec.
// The returned bytes pass the manifest-schema validator (internal/manifest)
// when written to disk and reloaded; callers can also Decode them in-memory.
//
// Generate validates the spec first, then dispatches to the kind's embedded
// template. Validation catches the structural mistakes the wizard front-end
// might let through (missing slash, wrong template/deploy nesting, unknown
// field type) so the caller never has to wrap the result in another schema
// check.
func Generate(spec Spec) ([]byte, error) {
	if err := validateSpec(spec); err != nil {
		return nil, err
	}
	tmpl := templates.Lookup(string(spec.Kind) + ".gotmpl")
	if tmpl == nil {
		return nil, fmt.Errorf("scaffold: no template for kind %q", spec.Kind)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, spec); err != nil {
		return nil, fmt.Errorf("scaffold: render %s: %w", spec.Kind, err)
	}
	return buf.Bytes(), nil
}

// validateSpec rejects specs whose shape contradicts the kind. It enforces
// the same kind/section coupling the manifest validator does, but earlier in
// the pipeline so the error names FieldSpec/TemplateSpec rather than YAML
// paths. Field-level checks (unknown type, duplicate IDs) reuse the
// manifest-validator semantics by deferring to a synthesized Manifest.
func validateSpec(spec Spec) error {
	if spec.Slash == "" {
		return fmt.Errorf("scaffold: spec.Slash is required")
	}
	if !strings.HasPrefix(spec.Slash, "/") {
		return fmt.Errorf("scaffold: spec.Slash must start with %q (got %q)", "/", spec.Slash)
	}
	if strings.ContainsAny(spec.Slash, " \t\n") {
		return fmt.Errorf("scaffold: spec.Slash must not contain whitespace")
	}
	if spec.Title == "" {
		return fmt.Errorf("scaffold: spec.Title is required")
	}

	switch spec.Kind {
	case manifest.KindResource:
		if spec.Template == nil {
			return fmt.Errorf("scaffold: spec.Template is required for kind: resource")
		}
		if spec.Template.Kind == "" {
			return fmt.Errorf("scaffold: spec.Template.Kind is required")
		}
		if spec.Template.Path == "" {
			return fmt.Errorf("scaffold: spec.Template.Path is required")
		}
		if spec.Deploy == nil {
			return fmt.Errorf("scaffold: spec.Deploy is required for kind: resource")
		}
		if spec.Deploy.Driver == "" {
			return fmt.Errorf("scaffold: spec.Deploy.Driver is required")
		}
	case manifest.KindShell, manifest.KindMonitor, manifest.KindComposite:
		if spec.Template != nil {
			return fmt.Errorf("scaffold: spec.Template is not allowed for kind: %s", spec.Kind)
		}
		if spec.Deploy != nil {
			return fmt.Errorf("scaffold: spec.Deploy is not allowed for kind: %s", spec.Kind)
		}
	case "":
		return fmt.Errorf("scaffold: spec.Kind is required")
	default:
		return fmt.Errorf("scaffold: spec.Kind %q is not a recognised manifest kind", spec.Kind)
	}

	return validateFields(spec.Form)
}

// validateFields runs the per-field structural checks the manifest validator
// also enforces. Duplicate detection works on a single pass so the error
// points at the second occurrence — the entry the user just added.
func validateFields(fields []FieldSpec) error {
	seen := make(map[string]int, len(fields))
	for i, f := range fields {
		if f.ID == "" {
			return fmt.Errorf("scaffold: spec.Form[%d].ID is required", i)
		}
		if prev, dup := seen[f.ID]; dup {
			return fmt.Errorf("scaffold: spec.Form[%d].ID %q duplicates spec.Form[%d]", i, f.ID, prev)
		}
		seen[f.ID] = i
		if f.Type == "" {
			return fmt.Errorf("scaffold: spec.Form[%d].Type is required", i)
		}
		if _, ok := knownFieldTypes[f.Type]; !ok {
			return fmt.Errorf("scaffold: spec.Form[%d].Type %q is not a recognised field type", i, f.Type)
		}
		if f.Type == manifest.TypeEnum && len(f.Values) == 0 {
			return fmt.Errorf("scaffold: spec.Form[%d].Values is required for type: enum", i)
		}
		if f.Min != nil && f.Max != nil && *f.Min > *f.Max {
			return fmt.Errorf("scaffold: spec.Form[%d].Min (%d) must not exceed Max (%d)", i, *f.Min, *f.Max)
		}
	}
	for i, f := range fields {
		for j, dep := range f.DependsOn {
			if dep == "" {
				return fmt.Errorf("scaffold: spec.Form[%d].DependsOn[%d] is empty", i, j)
			}
			if dep == f.ID {
				return fmt.Errorf("scaffold: spec.Form[%d].DependsOn[%d] %q is a self-dependency", i, j, dep)
			}
			if _, ok := seen[dep]; !ok {
				return fmt.Errorf("scaffold: spec.Form[%d].DependsOn[%d] references unknown field %q", i, j, dep)
			}
		}
	}
	return nil
}

// knownFieldTypes mirrors the manifest validator's allow-list. Keeping a
// private copy here avoids dragging an internal/* dependency into the spec
// validator (the top-level manifest shim does not export the set).
var knownFieldTypes = map[manifest.FieldType]struct{}{
	manifest.TypeString:       {},
	manifest.TypeInt:          {},
	manifest.TypeBool:         {},
	manifest.TypeEnum:         {},
	manifest.TypeMultistring:  {},
	manifest.TypeSecret:       {},
	manifest.TypeAWSVpcID:     {},
	manifest.TypeAWSSubnetIDs: {},
	manifest.TypeAWSSGIDs:     {},
	manifest.TypeAWSACMArn:    {},
}

// yamlScalar renders v as a single YAML-safe scalar. yaml.Marshal handles
// the quoting rules (escaping embedded quotes, picking single vs double
// vs plain style based on content); the trailing newline it adds is
// trimmed so callers can drop the result inline after `key:`.
func yamlScalar(v any) (string, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("scaffold: yaml.Marshal: %w", err)
	}
	return strings.TrimRight(string(b), "\n"), nil
}

// fieldDefault converts a FieldSpec's textual default into the typed YAML
// scalar the field's Type expects. The string passes through yamlScalar for
// quoting; ints and bools emit unquoted so the loader's strict types accept
// them. Unknown types fall back to a quoted string — a safe default that
// keeps unrecognised field types from blowing up the templater.
func fieldDefault(f FieldSpec) (string, error) {
	switch f.Type {
	case manifest.TypeInt:
		if _, err := strconv.Atoi(f.Default); err != nil {
			return "", fmt.Errorf("scaffold: field %q: default %q is not an int", f.ID, f.Default)
		}
		return f.Default, nil
	case manifest.TypeBool:
		switch f.Default {
		case "true", "false":
			return f.Default, nil
		default:
			return "", fmt.Errorf("scaffold: field %q: default %q is not a bool", f.ID, f.Default)
		}
	default:
		return yamlScalar(f.Default)
	}
}

// renderField turns one FieldSpec into the YAML lines that sit under a
// manifest's `form:` block. The output starts with two spaces and ends
// with a newline, so callers concatenate fields by simply ranging and
// emitting the result. Doing the rendering in Go (rather than a
// text/template sub-template) sidesteps the trim-marker pitfalls that
// caused fields to collapse onto a single line in early drafts.
//
// fieldDefault is invoked for the typed scalar so an int field's default
// emits unquoted (and is therefore decoded as an int by the loader).
func renderField(f FieldSpec) (string, error) {
	var b strings.Builder
	id, err := yamlScalar(f.ID)
	if err != nil {
		return "", err
	}
	label, err := yamlScalar(f.Label)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "  - id: %s\n", id)
	fmt.Fprintf(&b, "    label: %s\n", label)
	fmt.Fprintf(&b, "    type: %s\n", f.Type)
	// ADR-0051: every scaffolded field ships a commented placeholder line
	// pre-filled with the catalogue default, so the override mechanism is
	// discoverable. Commented out so the strict YAML loader treats it as
	// metadata, not a real key — uncommenting is the author's opt-in.
	if hint := hints.Lookup(string(f.Type)); hint != "" {
		quoted, err := yamlScalar(hint)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "    # placeholder: %s\n", quoted)
	}
	if f.Required {
		b.WriteString("    required: true\n")
	}
	if f.Default != "" {
		def, err := fieldDefault(f)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "    default: %s\n", def)
	}
	if len(f.Values) > 0 {
		b.WriteString("    values:\n")
		for _, v := range f.Values {
			val, err := yamlScalar(v)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "      - %s\n", val)
		}
	}
	if f.Min != nil {
		fmt.Fprintf(&b, "    min: %d\n", *f.Min)
	}
	if f.Max != nil {
		fmt.Fprintf(&b, "    max: %d\n", *f.Max)
	}
	if len(f.DependsOn) > 0 {
		b.WriteString("    depends_on:\n")
		for _, d := range f.DependsOn {
			fmt.Fprintf(&b, "      - %s\n", d)
		}
	}
	return b.String(), nil
}

// sortedStringMap returns the keys of m in lexical order so range over the
// returned slice yields a deterministic iteration order. The template
// invokes this on env maps; without it, generated YAML would differ run to
// run, breaking golden-file tests and version-control diffs.
func sortedStringMap(m map[string]string) []sortedEntry {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]sortedEntry, len(keys))
	for i, k := range keys {
		out[i] = sortedEntry{Key: k, Value: m[k]}
	}
	return out
}

// sortedEntry pairs a sorted key with its value. text/template's range over
// a slice exposes index/element; exposing a struct lets the template name
// the key and value with `$k, $v := envSorted ...`. The template iterates as
// `range $k, $v` — text/template's range over a slice of two-field structs
// supports this via field destructuring is *not* native, so the template
// instead uses `range envSorted .Deploy.Env` and references `.Key`/`.Value`.
type sortedEntry struct {
	Key   string
	Value string
}
