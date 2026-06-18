package screens

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bannaarr01/packwright/internal/update"
)

// ChangesetScreen previews the resource-by-resource diff that an
// upcoming deploy would apply. ADR-0048 fixes the Diff shape; this
// screen renders it. There is no Apply control here — PR-06 owns the
// approval flow.
type ChangesetScreen struct {
	title  string
	diff   update.Diff
	width  int
	height int
	keys   changesetKeyMap
}

// changesetKeyMap holds the screen-local bindings.
type changesetKeyMap struct {
	Back key.Binding
}

// NewChangeset returns a screen previewing d for the named stack. The
// title is shown above the content pane; it is the caller's choice so
// the screen can render either a stack name or a project/env/stack
// triple.
func NewChangeset(title string, d update.Diff) *ChangesetScreen {
	return &ChangesetScreen{
		title: title,
		diff:  d,
		keys: changesetKeyMap{
			Back: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		},
	}
}

// Init returns nil — the diff is supplied synchronously.
func (s *ChangesetScreen) Init() tea.Cmd { return nil }

// SetSize records the content-pane dimensions for the next render.
func (s *ChangesetScreen) SetSize(w, h int) { s.width, s.height = w, h }

// KeyMap returns the screen-local bindings.
func (s *ChangesetScreen) KeyMap() []key.Binding {
	return []key.Binding{s.keys.Back}
}

// Title is the human-readable label shown above the content pane.
func (s *ChangesetScreen) Title() string {
	return "Changeset · " + s.title
}

// Update routes one bubbletea message. Esc emits PopMsg.
func (s *ChangesetScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		if key.Matches(km, s.keys.Back) {
			return s, func() tea.Msg { return PopMsg{} }
		}
	}
	return s, nil
}

// View renders the diff as four buckets (Adds, Modifies, Replaces,
// Deletes) followed by a parameter-deltas section. Parses the
// NoChanges sentinel as a one-line message so the user is not
// confronted with an empty screen.
func (s *ChangesetScreen) View() string {
	var b strings.Builder
	b.WriteString(changesetTitle.Render(s.Title()))
	b.WriteString("\n")
	if s.diff.NoChanges {
		b.WriteString("\n" + changesetDim.Render("No changes — the stack is already at the target state."))
		return changesetPad.Render(b.String())
	}
	a, m, r, d := s.diff.Counts()
	b.WriteString(changesetDim.Render(fmt.Sprintf(
		"  %d add · %d modify · %d replace · %d remove\n\n", a, m, r, d)))
	b.WriteString(renderBucket("Add", s.diff.Adds, changesetAdd))
	b.WriteString(renderBucket("Modify", s.diff.Modifies, changesetMod))
	b.WriteString(renderBucket("Replace", s.diff.Replaces, changesetRep))
	b.WriteString(renderBucket("Remove", s.diff.Deletes, changesetRem))
	if len(s.diff.ParameterDeltas) > 0 {
		b.WriteString(changesetTitle.Render("Parameters"))
		b.WriteString("\n")
		for _, p := range s.diff.ParameterDeltas {
			marker := "  "
			if p.CausedReplacement {
				marker = changesetRep.Render(" ! ")
			}
			b.WriteString(fmt.Sprintf("%s%-24s ", marker, p.Key))
			b.WriteString(changesetDim.Render(p.Old))
			b.WriteString(" → ")
			b.WriteString(p.New)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return changesetPad.Render(b.String())
}

// renderBucket renders one of the four resource buckets with a heading
// and one row per ResourceDelta. Empty buckets are skipped so the
// screen stays compact.
func renderBucket(label string, deltas []update.ResourceDelta, accent lipgloss.Style) string {
	if len(deltas) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(changesetTitle.Render(label))
	b.WriteString(changesetDim.Render(fmt.Sprintf("  (%d)\n", len(deltas))))
	for _, d := range deltas {
		b.WriteString(accent.Render(actionGlyph(d.Action)))
		b.WriteString("  ")
		b.WriteString(changesetResource.Render(d.LogicalID))
		if d.ResourceType != "" {
			b.WriteString(changesetDim.Render("  " + d.ResourceType))
		}
		if d.IAM {
			b.WriteString(" " + changesetWarn.Render("[IAM]"))
		}
		b.WriteString("\n")
		if d.PhysicalID != "" {
			b.WriteString(changesetDim.Render("     physical: " + d.PhysicalID + "\n"))
		}
		if len(d.PropertyCauses) > 0 {
			b.WriteString(changesetDim.Render("     causes:   " + strings.Join(d.PropertyCauses, ", ") + "\n"))
		}
	}
	b.WriteString("\n")
	return b.String()
}

func actionGlyph(a update.DiffAction) string {
	switch a {
	case update.ActionAdd:
		return "+"
	case update.ActionModify:
		return "~"
	case update.ActionReplace:
		return "!"
	case update.ActionRemove:
		return "-"
	case update.ActionImport:
		return "↘"
	}
	return "·"
}

var (
	changesetPad      = lipgloss.NewStyle().Padding(1, 2)
	changesetTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	changesetDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	changesetResource = lipgloss.NewStyle().Bold(true)
	changesetAdd      = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	changesetMod      = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	changesetRep      = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	changesetRem      = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	changesetWarn     = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
)
