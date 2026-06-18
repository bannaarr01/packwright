package screens

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bannaarr01/packwright/internal/record"
)

// RecordScreen shows the persisted StackRecord for one stack: broad +
// CFN status, region/profile/account, outputs, and resource list.
// Pushed onto the registry when the sidebar emits an OpenStackMsg.
type RecordScreen struct {
	project string
	env     string
	stack   string
	rec     *record.StackRecord
	err     error
	width   int
	height  int
	keys    recordKeyMap
}

// recordKeyMap holds the screen-local bindings rendered on the footer.
type recordKeyMap struct {
	Back key.Binding
}

// NewRecord constructs a screen for the named stack. The record is
// looked up synchronously through store; a nil store or missing record
// still produces a screen (it shows "no record yet"). Store-level
// errors are surfaced inline.
func NewRecord(project, env, stack string, store *record.Store) *RecordScreen {
	s := &RecordScreen{
		project: project,
		env:     env,
		stack:   stack,
		keys: recordKeyMap{
			Back: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		},
	}
	if store != nil {
		if r, err := store.Read(project, env, stack); err == nil {
			s.rec = r
		} else {
			s.err = err
		}
	}
	return s
}

// Init returns nil — the record is read synchronously at construction.
func (s *RecordScreen) Init() tea.Cmd { return nil }

// SetSize records the content-pane dimensions for the next View.
func (s *RecordScreen) SetSize(w, h int) { s.width, s.height = w, h }

// KeyMap returns the screen-local bindings the footer renders on its
// top line.
func (s *RecordScreen) KeyMap() []key.Binding {
	return []key.Binding{s.keys.Back}
}

// Title is the human-readable label shown above the content pane.
func (s *RecordScreen) Title() string {
	return fmt.Sprintf("Record · %s/%s/%s", s.project, s.env, s.stack)
}

// Update routes one bubbletea message. Esc emits PopMsg.
func (s *RecordScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		if key.Matches(km, s.keys.Back) {
			return s, func() tea.Msg { return PopMsg{} }
		}
	}
	return s, nil
}

// View renders the record body. Layout is intentionally flat — PR-02
// owns the canonical record visualization; this is a shell-only
// rendering of the same data.
func (s *RecordScreen) View() string {
	var b strings.Builder
	b.WriteString(recordTitle.Render(s.Title()))
	b.WriteString("\n\n")
	if s.err != nil {
		b.WriteString(recordErr.Render("error: " + s.err.Error()))
		return recordPad.Render(b.String())
	}
	if s.rec == nil {
		b.WriteString(recordDim.Render("No record yet. Deploy the stack to populate this view."))
		return recordPad.Render(b.String())
	}
	b.WriteString(field("status", string(s.rec.Status.Broad)+" · "+s.rec.Status.CFN))
	if s.rec.Status.Discrepancy != "" {
		b.WriteString(field("note", s.rec.Status.Discrepancy))
	}
	b.WriteString(field("region", s.rec.Region))
	if s.rec.Profile != "" {
		b.WriteString(field("profile", s.rec.Profile))
	}
	if s.rec.Account != "" {
		b.WriteString(field("account", s.rec.Account))
	}
	if !s.rec.LastUpdatedAt.IsZero() {
		b.WriteString(field("updated", s.rec.LastUpdatedAt.Format("2006-01-02 15:04 MST")))
	} else if !s.rec.DeployedAt.IsZero() {
		b.WriteString(field("deployed", s.rec.DeployedAt.Format("2006-01-02 15:04 MST")))
	}
	if len(s.rec.Outputs) > 0 {
		b.WriteString("\n")
		b.WriteString(recordTitle.Render("outputs"))
		b.WriteString("\n")
		outs := append([]record.Output(nil), s.rec.Outputs...)
		sort.Slice(outs, func(i, j int) bool { return outs[i].Key < outs[j].Key })
		for _, o := range outs {
			b.WriteString(field(o.Key, o.Value))
		}
	}
	if n := len(s.rec.Resources); n > 0 {
		b.WriteString("\n")
		b.WriteString(recordTitle.Render(fmt.Sprintf("resources (%d)", n)))
		b.WriteString("\n")
		for _, r := range s.rec.Resources {
			b.WriteString(recordDim.Render(fmt.Sprintf("  %-26s ", r.LogicalID)))
			b.WriteString(fmt.Sprintf("%-32s ", r.Type))
			b.WriteString(recordDim.Render(r.Status))
			b.WriteString("\n")
		}
	}
	return recordPad.Render(b.String())
}

func field(name, val string) string {
	return recordDim.Render(fmt.Sprintf("  %-12s ", name)) + val + "\n"
}

var (
	recordPad   = lipgloss.NewStyle().Padding(1, 2)
	recordTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	recordDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	recordErr   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)
