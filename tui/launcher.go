package tui

import "github.com/charmbracelet/lipgloss"

// launcher is the home screen shown when the TUI starts and after the palette
// is dismissed. The MVP-1 view is intentionally minimal — a welcome message
// and a pointer at the palette keybinding. Later PRs add recent-stacks and
// quick-action rows here.
type launcher struct {
	width, height int
}

// SetSize records the latest terminal size for future rendering decisions.
func (l *launcher) SetSize(w, h int) { l.width, l.height = w, h }

var launcherStyle = lipgloss.NewStyle().Padding(1, 2)

// View renders the launcher screen.
func (l launcher) View() string {
	return launcherStyle.Render("Packwright\n\nPress ctrl+p to open the palette.")
}
