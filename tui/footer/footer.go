// Package footer renders the two-line strip at the bottom of the TUI:
//
//   - the top line shows the active screen's local key bindings,
//     pulled from Screen.KeyMap() on every render;
//   - the bottom line shows the persistent global bindings the shell
//     reserves regardless of which screen is active.
//
// The footer owns layout and styling only — both lists of bindings are
// passed in by the root model on every render.
package footer

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
)

// Footer is the root-model-owned widget that renders the bottom strip.
// One Footer is reused across renders.
type Footer struct {
	local  lipgloss.Style
	global lipgloss.Style
	sep    lipgloss.Style
}

// New returns a Footer ready to render. The "global" line is dimmer than
// the "local" line so the active screen's bindings draw the eye first.
func New() Footer {
	return Footer{
		local:  lipgloss.NewStyle().Foreground(lipgloss.Color("250")),
		global: lipgloss.NewStyle().Foreground(lipgloss.Color("241")),
		sep:    lipgloss.NewStyle().Foreground(lipgloss.Color("237")),
	}
}

// Height returns the number of rows View() emits. The root model
// subtracts it from the available space when sizing the content pane.
func (f Footer) Height() int { return 2 }

// View renders the two-line footer at the given width. The bindings are
// rendered as "key — label" pairs joined by " · ". An empty local list
// renders as a blank top line so the global line stays anchored to the
// terminal floor.
func (f Footer) View(local, global []key.Binding, width int) string {
	top := f.local.Render(joinBindings(local, " · "))
	bot := f.global.Render(joinBindings(global, " · "))
	if width <= 0 {
		return top + "\n" + bot
	}
	return f.padTo(top, width) + "\n" + f.padTo(bot, width)
}

// padTo right-pads s with spaces so its rendered width matches width.
// Strings already wider than width are returned unchanged — the caller
// decides whether to truncate.
func (f Footer) padTo(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// joinBindings renders one line of "key — label" pairs separated by sep.
// Disabled bindings are skipped so a screen can hide a binding mid-flow
// (e.g. the form screen disabling Enter while a field is empty).
func joinBindings(bs []key.Binding, sep string) string {
	if len(bs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(bs))
	for _, b := range bs {
		if !b.Enabled() {
			continue
		}
		h := b.Help()
		if h.Key == "" && h.Desc == "" {
			continue
		}
		parts = append(parts, h.Key+" "+h.Desc)
	}
	return strings.Join(parts, sep)
}
