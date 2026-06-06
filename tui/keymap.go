// Package tui implements the Packwright terminal user interface built on the
// Charm stack (Bubble Tea, Bubbles, Lipgloss). The TUI exposes a Ctrl+P fuzzy
// command palette over the (future) pack registry plus a launcher home screen.
//
// The package exports a single entry point, [Launch], which is wired into the
// CLI through cmd.TUILauncher from a sibling subcommand file.
package tui

import "github.com/charmbracelet/bubbles/key"

// KeyMap holds every keybinding the TUI observes. It also satisfies the
// help.KeyMap interface so the bottom-of-screen help row can be rendered with
// bubbles/help.
type KeyMap struct {
	Quit         key.Binding
	OpenPalette  key.Binding
	ClosePalette key.Binding
	ToggleHelp   key.Binding
	Up           key.Binding
	Down         key.Binding
	Select       key.Binding
}

// DefaultKeyMap returns the bindings used by the Packwright TUI:
// q/Ctrl+C to quit, Ctrl+P to open the palette, Esc to close it,
// ? to toggle full-screen help.
func DefaultKeyMap() KeyMap {
	return KeyMap{
		Quit:         key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		OpenPalette:  key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("ctrl+p", "palette")),
		ClosePalette: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
		ToggleHelp:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Up:           key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:         key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Select:       key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
	}
}

// ShortHelp implements help.KeyMap. It returns the bindings shown in the
// compact (single-line) help row.
func (k KeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.OpenPalette, k.ToggleHelp, k.Quit}
}

// FullHelp implements help.KeyMap. It returns the bindings shown when the
// user toggles full help with `?`.
func (k KeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Select},
		{k.OpenPalette, k.ClosePalette},
		{k.ToggleHelp, k.Quit},
	}
}
