package errors

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"text/template"
)

// Entry is one catalogue entry decoded from a YAML file under catalogue/. It
// is the matcher's runtime form: the raw_regex is pre-compiled and the
// templated fields (title, cause, suggested, console_url) are pre-parsed so
// Match can iterate the catalogue cheaply.
type Entry struct {
	// ID is the catalogue entry's stable identifier, the file's basename
	// without the .yaml extension. Surfaced on AppError.MatchedID.
	ID string

	// Priority sorts entries within the catalogue; higher priority wins
	// when more than one entry matches. Defaults to 0; ties break on ID
	// (ascending) for determinism.
	Priority int

	// Match holds the conditions an inbound raw error must satisfy.
	Match MatchSpec

	// Title is the entry's headline template.
	Title *template.Template

	// Cause is the entry's probable-root-cause template.
	Cause *template.Template

	// Suggested is the list of next-step templates.
	Suggested []*template.Template

	// ConsoleURL is the entry's AWS Console deep-link template.
	ConsoleURL *template.Template

	// Retryable mirrors AppError.Retryable; copied through verbatim.
	Retryable bool

	// rawSpec is the entry as loaded from disk; kept so error messages
	// can refer back to the original YAML rather than the compiled form.
	rawSpec entrySpec
}

// MatchSpec is the runtime form of a catalogue entry's match block. An
// entry matches when every populated condition is satisfied; an entry with
// no conditions never matches (rejected at load time).
type MatchSpec struct {
	// AWSService, when non-empty, must equal the inbound context's
	// AWSService (case-insensitive). Empty means "do not check".
	AWSService string

	// AWSCode, when non-empty, must equal the inbound context's AWSCode
	// (case-insensitive). Empty means "do not check".
	AWSCode string

	// RawRegex, when non-nil, must match the raw error text. Named
	// capture groups in the expression are extracted into the template
	// context. Empty means "do not check".
	RawRegex *regexp.Regexp
}

// Context is the per-call data the matcher uses both to filter entries and
// to render their templates. Callers populate as much as they have: the auto
// fetch path always populates StackName, Resource, AWSService, AWSCode, and
// Region; an ad-hoc Match call may only pass Inputs.
//
// Inputs is the manifest's last-submitted form data, keyed by field ID. The
// matcher merges it with regex named-groups so a catalogue template can
// reference either a form field ("{{ .TargetGroupName }}" when TargetGroupName
// was a form field) or a value extracted from the raw error message.
type Context struct {
	AWSService string
	AWSCode    string
	StackName  string
	Resource   string
	Region     string
	Inputs     map[string]any
}

// Match walks the catalogue in priority order and returns the AppError for
// the first matching entry. When nothing matches, it returns an AppError
// populated only with Raw + the AWS metadata from ctx — equivalent to what
// the user would have seen from the raw `aws` CLI.
//
// A nil rawErr is treated as the empty error string; callers should not rely
// on Match returning nil — it always returns a populated *AppError so the
// renderer never has to special-case the absent value.
func Match(rawErr error, ctx Context) *AppError {
	raw := ""
	if rawErr != nil {
		raw = rawErr.Error()
	}
	return MatchString(raw, ctx)
}

// MatchString is the string-typed variant of Match for call sites that
// already have the raw error text (e.g. a CloudFormation stack event's
// Reason field, which is a string, not an error).
func MatchString(raw string, ctx Context) *AppError {
	cat := loadedCatalogue()
	for _, e := range cat {
		groups, ok := e.evalMatch(raw, ctx)
		if !ok {
			continue
		}
		return e.render(raw, ctx, groups)
	}
	return fallback(raw, ctx)
}

// evalMatch reports whether e matches (raw, ctx) and returns the regex
// named capture groups it extracted (empty map when no raw_regex is
// configured). It does not execute user code — the "eval" in the name is
// short for "evaluate match conditions"; the body is just string and regex
// comparisons. The returned map is owned by the caller.
func (e *Entry) evalMatch(raw string, ctx Context) (map[string]string, bool) {
	if e.Match.AWSService != "" && !strings.EqualFold(e.Match.AWSService, ctx.AWSService) {
		return nil, false
	}
	if e.Match.AWSCode != "" && !strings.EqualFold(e.Match.AWSCode, ctx.AWSCode) {
		return nil, false
	}
	groups := map[string]string{}
	if e.Match.RawRegex != nil {
		m := e.Match.RawRegex.FindStringSubmatch(raw)
		if m == nil {
			return nil, false
		}
		for i, name := range e.Match.RawRegex.SubexpNames() {
			if name == "" || i >= len(m) {
				continue
			}
			groups[name] = m[i]
		}
	}
	return groups, true
}

// render evaluates the entry's templates against the merged (regex groups +
// ctx.Inputs + ctx fields) data map and returns the resulting AppError.
// Template-execution failures degrade gracefully: the field falls back to
// its raw template body so a typo in the catalogue cannot mask the original
// AWS error.
func (e *Entry) render(raw string, ctx Context, groups map[string]string) *AppError {
	data := mergeData(ctx, groups)

	out := &AppError{
		AWSService: ctx.AWSService,
		AWSCode:    ctx.AWSCode,
		StackName:  ctx.StackName,
		Resource:   ctx.Resource,
		Raw:        raw,
		Retryable:  e.Retryable,
		MatchedID:  e.ID,
		Title:      execTemplate(e.Title, data),
		Cause:      execTemplate(e.Cause, data),
		ConsoleURL: execTemplate(e.ConsoleURL, data),
	}
	if len(e.Suggested) > 0 {
		out.Suggested = make([]string, 0, len(e.Suggested))
		for _, t := range e.Suggested {
			out.Suggested = append(out.Suggested, execTemplate(t, data))
		}
	}
	return out
}

// fallback returns the unknown-error AppError: Raw is always populated, AWS
// metadata is copied through verbatim, and the rest is empty. The renderer
// uses this shape to draw the "we did not find a pattern for this" card.
func fallback(raw string, ctx Context) *AppError {
	return &AppError{
		AWSService: ctx.AWSService,
		AWSCode:    ctx.AWSCode,
		StackName:  ctx.StackName,
		Resource:   ctx.Resource,
		Raw:        raw,
	}
}

// mergeData builds the template data map. Regex named groups win over
// ctx.Inputs win over the well-known ctx fields, so a catalogue template
// can always shadow earlier sources with a more specific value (the
// freshly-extracted groups are the most specific).
func mergeData(ctx Context, groups map[string]string) map[string]any {
	data := map[string]any{}
	data["AWSService"] = ctx.AWSService
	data["AWSCode"] = ctx.AWSCode
	data["StackName"] = ctx.StackName
	data["Resource"] = ctx.Resource
	data["Region"] = ctx.Region
	for k, v := range ctx.Inputs {
		data[k] = v
	}
	for k, v := range groups {
		data[k] = v
	}
	return data
}

// execTemplate renders t against data and returns the result. A nil
// template renders as "" so optional fields can be left out of an entry
// without a special case in render. On execution failure (typically a
// missing field reference) the function returns the template's raw body
// — callers see a literal "{{ .Foo }}" instead of an empty string, which
// makes catalogue bugs visible without crashing.
func execTemplate(t *template.Template, data map[string]any) string {
	if t == nil {
		return ""
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return t.Root.String()
	}
	return buf.String()
}

// entrySpec is the on-disk shape of a catalogue YAML file. It is the
// loader's intermediate type — compileEntry turns it into a runtime Entry.
// source is set by the loader (not from YAML) so error messages can refer
// back to the originating filename.
type entrySpec struct {
	ID        string        `yaml:"id"`
	Priority  int           `yaml:"priority,omitempty"`
	Match     matchSpecSpec `yaml:"match"`
	Title     string        `yaml:"title"`
	Cause     string        `yaml:"cause"`
	Suggested []string      `yaml:"suggested,omitempty"`
	Console   string        `yaml:"console_url,omitempty"`
	Retryable bool          `yaml:"retryable,omitempty"`

	source string `yaml:"-"`
}

// matchSpecSpec is the YAML shape of the match block. RawRegex is a plain
// string at this layer; Compile compiles it into a *regexp.Regexp.
type matchSpecSpec struct {
	AWSService string `yaml:"aws_service,omitempty"`
	AWSCode    string `yaml:"aws_code,omitempty"`
	RawRegex   string `yaml:"raw_regex,omitempty"`
}

// compileEntry compiles a decoded entrySpec into a runtime Entry. It is
// exported through the loader, not directly; callers should not assemble
// Entry values by hand because Match relies on the compiled templates and
// regex being non-nil/valid.
func compileEntry(spec entrySpec) (*Entry, error) {
	if spec.ID == "" {
		return nil, fmt.Errorf("errors: %s: id is required", spec.source)
	}
	if spec.Title == "" {
		return nil, fmt.Errorf("errors: %s: title is required", spec.source)
	}
	if spec.Match.AWSService == "" && spec.Match.AWSCode == "" && spec.Match.RawRegex == "" {
		return nil, fmt.Errorf("errors: %s: match block must set at least one of aws_service, aws_code, raw_regex", spec.source)
	}

	e := &Entry{
		ID:        spec.ID,
		Priority:  spec.Priority,
		Retryable: spec.Retryable,
		Match: MatchSpec{
			AWSService: spec.Match.AWSService,
			AWSCode:    spec.Match.AWSCode,
		},
		rawSpec: spec,
	}

	if spec.Match.RawRegex != "" {
		re, err := regexp.Compile(spec.Match.RawRegex)
		if err != nil {
			return nil, fmt.Errorf("errors: %s: raw_regex: %w", spec.source, err)
		}
		e.Match.RawRegex = re
	}

	t, err := parseTemplate(spec.ID+":title", spec.Title)
	if err != nil {
		return nil, fmt.Errorf("errors: %s: title: %w", spec.source, err)
	}
	e.Title = t

	if spec.Cause != "" {
		t, err := parseTemplate(spec.ID+":cause", spec.Cause)
		if err != nil {
			return nil, fmt.Errorf("errors: %s: cause: %w", spec.source, err)
		}
		e.Cause = t
	}

	if spec.Console != "" {
		t, err := parseTemplate(spec.ID+":console_url", spec.Console)
		if err != nil {
			return nil, fmt.Errorf("errors: %s: console_url: %w", spec.source, err)
		}
		e.ConsoleURL = t
	}

	for i, s := range spec.Suggested {
		t, err := parseTemplate(fmt.Sprintf("%s:suggested[%d]", spec.ID, i), s)
		if err != nil {
			return nil, fmt.Errorf("errors: %s: suggested[%d]: %w", spec.source, i, err)
		}
		e.Suggested = append(e.Suggested, t)
	}

	return e, nil
}

// parseTemplate parses body with the package's shared option set. Missing
// keys render as "<no value>" so a catalogue template that references an
// optional field does not blow up the entire AppError.
func parseTemplate(name, body string) (*template.Template, error) {
	return template.New(name).Option("missingkey=default").Parse(body)
}

// sortEntries sorts the slice in place by descending Priority, ascending ID
// — the order Match walks. Exposed so tests can assert on a stable
// catalogue order without re-implementing the comparator.
func sortEntries(entries []*Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Priority != entries[j].Priority {
			return entries[i].Priority > entries[j].Priority
		}
		return entries[i].ID < entries[j].ID
	})
}
