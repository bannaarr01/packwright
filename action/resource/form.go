package resource

import (
	"github.com/bannaarr01/packwright/manifest"
)

// FieldState is the per-field state tracked while a user fills out a form.
// FormState is intentionally a passive data structure: front-ends mutate it
// directly via Set, and validators read from it via Snapshot. It contains no
// Bubble Tea / Wails imports so the same state can drive either front-end.
type FieldState struct {
	Value   any
	Touched bool
	Error   string
}

// FormState is the in-progress state of one action's form. The slice of
// fields mirrors the manifest's declared order so iteration produces a stable
// rendering for both the TUI and GUI.
type FormState struct {
	fields []manifest.Field
	state  map[string]*FieldState
}

// NewFormState constructs a FormState seeded with the manifest's field
// definitions. All fields start untouched with no value; defaults are not
// auto-applied because they may themselves be Go-template strings that
// reference other fields the user has not yet filled in.
func NewFormState(m *manifest.Manifest) *FormState {
	fs := &FormState{state: make(map[string]*FieldState)}
	if m == nil {
		return fs
	}
	fs.fields = append(fs.fields, m.Form...)
	for _, f := range m.Form {
		fs.state[f.ID] = &FieldState{}
	}
	return fs
}

// Fields returns the manifest's fields in declaration order. The slice is a
// view into the FormState — do not mutate.
func (f *FormState) Fields() []manifest.Field {
	return f.fields
}

// Set updates the value for fieldID and marks the field touched. Unknown
// field IDs are silently ignored so a stale front-end can't crash the engine.
func (f *FormState) Set(fieldID string, value any) {
	s, ok := f.state[fieldID]
	if !ok {
		return
	}
	s.Value = value
	s.Touched = true
	s.Error = ""
}

// SetError attaches an error message to a field. Callers (the validator) set
// it after running validation; the front-end reads it via Get.
func (f *FormState) SetError(fieldID, msg string) {
	if s, ok := f.state[fieldID]; ok {
		s.Error = msg
	}
}

// Get returns the current FieldState for fieldID. The zero value is returned
// for unknown IDs, which lets the front-end probe optimistically.
func (f *FormState) Get(fieldID string) FieldState {
	if s, ok := f.state[fieldID]; ok {
		return *s
	}
	return FieldState{}
}

// Available reports whether fieldID is ready to be presented to the user:
// every field listed in its depends_on must have a non-empty value. Used by
// the TUI to grey out subordinate pickers (e.g. SubnetIds until VpcId is
// chosen).
func (f *FormState) Available(fieldID string) bool {
	field, ok := f.field(fieldID)
	if !ok {
		return false
	}
	for _, dep := range field.DependsOn {
		if !hasValue(f.state[dep]) {
			return false
		}
	}
	return true
}

// AvailableIDs returns the IDs of all fields whose dependencies are satisfied,
// in declaration order.
func (f *FormState) AvailableIDs() []string {
	out := make([]string, 0, len(f.fields))
	for _, fld := range f.fields {
		if f.Available(fld.ID) {
			out = append(out, fld.ID)
		}
	}
	return out
}

// Inputs returns a copy of the current value map, suitable for passing to
// Execute. Fields that were never set are omitted so the renderer can tell
// "not provided" from "explicitly empty".
func (f *FormState) Inputs() Inputs {
	out := make(Inputs, len(f.state))
	for id, s := range f.state {
		if !s.Touched {
			continue
		}
		out[id] = s.Value
	}
	return out
}

func (f *FormState) field(id string) (manifest.Field, bool) {
	for _, fld := range f.fields {
		if fld.ID == id {
			return fld, true
		}
	}
	return manifest.Field{}, false
}

// hasValue reports whether s holds a non-empty value. Empty strings, nil
// slices, and untouched fields all count as "not yet set" so depends_on
// gating reads naturally.
func hasValue(s *FieldState) bool {
	if s == nil || !s.Touched || s.Value == nil {
		return false
	}
	switch v := s.Value.(type) {
	case string:
		return v != ""
	case []string:
		return len(v) > 0
	case []any:
		return len(v) > 0
	default:
		return true
	}
}
