package screens

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bannaarr01/packwright/internal/manifest"
	"github.com/bannaarr01/packwright/internal/manifest/hints"
)

// FormSubmitMsg is emitted when the user presses Enter (and the form is
// not actively editing in a multi-line field). The root model decides
// what to do with the values — there is no canonical handler here.
type FormSubmitMsg struct {
	// Title echoes the form's title so the root model can route the
	// submission without consulting any other state.
	Title string
	// Values is field-id → entered text. Fields the user left blank are
	// present with an empty string.
	Values map[string]string
}

// FormScreen is the generic field-entry screen used for the (future)
// stack-creation wizard. It renders the manifest's []Field, one
// textinput per field, with the PR-04 hint blurb under each.
type FormScreen struct {
	title  string
	fields []manifest.Field
	inputs []textinput.Model
	cur    int
	width  int
	height int
	keys   formKeyMap
}

// formKeyMap holds the screen-local bindings.
type formKeyMap struct {
	Next   key.Binding
	Prev   key.Binding
	Submit key.Binding
	Back   key.Binding
}

// NewForm builds a form with the given title and fields. The first
// field is focused automatically.
func NewForm(title string, fields []manifest.Field) *FormScreen {
	inputs := make([]textinput.Model, len(fields))
	for i, f := range fields {
		ti := textinput.New()
		ti.Placeholder = f.Placeholder
		ti.Prompt = "› "
		ti.CharLimit = 200
		inputs[i] = ti
	}
	if len(inputs) > 0 {
		inputs[0].Focus()
	}
	return &FormScreen{
		title:  title,
		fields: fields,
		inputs: inputs,
		keys: formKeyMap{
			Next:   key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
			Prev:   key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev field")),
			Submit: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "submit")),
			Back:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		},
	}
}

// Init returns nil — the inputs are seeded synchronously in NewForm.
func (s *FormScreen) Init() tea.Cmd { return nil }

// SetSize records the available size and reflows the inputs.
func (s *FormScreen) SetSize(w, h int) {
	s.width, s.height = w, h
	innerWidth := max(20, w-6)
	for i := range s.inputs {
		s.inputs[i].Width = innerWidth
	}
}

// KeyMap returns the screen-local bindings rendered on the footer.
func (s *FormScreen) KeyMap() []key.Binding {
	return []key.Binding{s.keys.Next, s.keys.Prev, s.keys.Submit, s.keys.Back}
}

// Title is the human-readable label rendered above the content pane.
func (s *FormScreen) Title() string { return s.title }

// Update routes one bubbletea message. Tab / Shift+Tab walks fields,
// Enter submits, Esc pops the screen.
func (s *FormScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch {
		case key.Matches(km, s.keys.Back):
			return s, func() tea.Msg { return PopMsg{} }
		case key.Matches(km, s.keys.Next):
			s.advance(1)
			return s, nil
		case key.Matches(km, s.keys.Prev):
			s.advance(-1)
			return s, nil
		case key.Matches(km, s.keys.Submit):
			return s, s.submit()
		}
	}
	if s.cur < len(s.inputs) {
		var cmd tea.Cmd
		s.inputs[s.cur], cmd = s.inputs[s.cur].Update(msg)
		return s, cmd
	}
	return s, nil
}

// advance moves the focused input by delta (+1 / -1) and refocuses the
// new one. Out-of-range moves wrap.
func (s *FormScreen) advance(delta int) {
	if len(s.inputs) == 0 {
		return
	}
	if s.cur < len(s.inputs) {
		s.inputs[s.cur].Blur()
	}
	s.cur = (s.cur + delta + len(s.inputs)) % len(s.inputs)
	s.inputs[s.cur].Focus()
}

// submit emits FormSubmitMsg keyed by field ID.
func (s *FormScreen) submit() tea.Cmd {
	values := make(map[string]string, len(s.fields))
	for i, f := range s.fields {
		values[f.ID] = s.inputs[i].Value()
	}
	title := s.title
	return func() tea.Msg { return FormSubmitMsg{Title: title, Values: values} }
}

// View renders the form: title, then each field as a label row + input +
// optional hint blurb resolved through manifest/hints.
func (s *FormScreen) View() string {
	var b strings.Builder
	b.WriteString(formTitle.Render(s.title))
	b.WriteString("\n\n")
	for i, f := range s.fields {
		label := f.Label
		if label == "" {
			label = f.ID
		}
		b.WriteString(formField.Render(label))
		if f.Required {
			b.WriteString(formReq.Render(" *"))
		}
		if i == s.cur {
			b.WriteString(formDim.Render("  (editing)"))
		}
		b.WriteString("\n")
		b.WriteString(s.inputs[i].View())
		b.WriteString("\n")
		if hint := hints.Resolve(f); hint != "" {
			b.WriteString(formDim.Render("  " + hint))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return formPad.Render(b.String())
}

var (
	formPad   = lipgloss.NewStyle().Padding(1, 2)
	formTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	formField = lipgloss.NewStyle().Bold(true)
	formReq   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	formDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)
