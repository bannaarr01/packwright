// Package tui implements the Packwright terminal user interface built on the
// Charm stack (Bubble Tea, Bubbles, Lipgloss). The TUI exposes a Ctrl+P fuzzy
// command palette over the (future) pack registry, a persistent project tree
// sidebar, and a screen registry that drives the content pane.
//
// The package exports a single entry point, [Launch], which is wired into the
// CLI through cmd.TUILauncher from a sibling subcommand file.
package tui

import "github.com/charmbracelet/bubbles/key"

// KeyMap holds every global keybinding the shell observes. It also satisfies
// the help.KeyMap interface so the bottom-of-screen help row can be rendered
// with bubbles/help when needed.
//
// Screen-local bindings live on each screen's KeyMap() method; the footer
// renders those above the global row.
type KeyMap struct {
	Quit         key.Binding
	OpenPalette  key.Binding
	ClosePalette key.Binding
	ToggleHelp   key.Binding
	Up           key.Binding
	Down         key.Binding
	Select       key.Binding
	// FocusCycle moves focus between the sidebar and the content pane.
	// Tab is the standard binding; Shift+Tab is also accepted so users
	// who only know that pair can cycle either direction.
	FocusCycle key.Binding
}

// DefaultKeyMap returns the bindings the Packwright shell ships with.
// Existing bindings (Ctrl+P, Esc, ?, q, Ctrl+C, Enter, ↑/↓) are
// preserved; Tab is added for sidebar/content focus cycling.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Quit:         key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		OpenPalette:  key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("ctrl+p", "palette")),
		ClosePalette: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
		ToggleHelp:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Up:           key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:         key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Select:       key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		FocusCycle:   key.NewBinding(key.WithKeys("tab", "shift+tab"), key.WithHelp("tab", "focus")),
	}
}

// ShortHelp implements help.KeyMap. It returns the bindings shown in the
// compact (single-line) help row.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.OpenPalette, k.FocusCycle, k.ToggleHelp, k.Quit}
}

// FullHelp implements help.KeyMap. It returns the bindings shown when the
// user toggles full help with `?`.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Select},
		{k.OpenPalette, k.ClosePalette, k.FocusCycle},
		{k.ToggleHelp, k.Quit},
	}
}

// GlobalBindings returns the bindings the footer renders on its bottom
// (persistent) line. These are always active regardless of focus.
func (k KeyMap) GlobalBindings() []key.Binding {
	return []key.Binding{k.OpenPalette, k.FocusCycle, k.ClosePalette, k.Quit}
}
