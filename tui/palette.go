package tui

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// paletteItem is one selectable entry in the palette. It satisfies
// list.Item and list.DefaultItem so the stock delegate can render it.
type paletteItem struct {
	slash string
	title string
}

// Title returns the slash command as the primary line for the list delegate.
func (p paletteItem) Title() string { return p.slash }

// Description returns the human-readable title as the secondary line.
func (p paletteItem) Description() string { return p.title }

// FilterValue returns the string the list's fuzzy filter scores against.
func (p paletteItem) FilterValue() string { return p.slash + " " + p.title }

// palette is the Ctrl+P fuzzy command palette. It wraps a bubbles/list.Model
// with filtering enabled (the list's default filter is fuzzy via sahilm/fuzzy)
// and reports user actions back to the root model via the message types in
// messages.go.
type palette struct {
	list list.Model
	keys KeyMap
}

// newPalette constructs the palette empty. Production callers immediately
// follow with SetItems carrying the registry-sourced rows; tests use
// newPaletteWithItems to inject deterministic data.
func newPalette(keys KeyMap) palette {
	return newPaletteWithItems(keys, nil)
}

// SetItems replaces the palette's contents. The Update loop calls it when a
// refreshPaletteMsg arrives so hot-reload edits propagate without rebuilding
// the surrounding list state (filter cursor, window size).
func (p *palette) SetItems(items []list.Item) { p.list.SetItems(items) }

// newPaletteWithItems constructs a palette pre-seeded with arbitrary items.
// Tests use it to inject deterministic data; production code goes through
// newPalette.
func newPaletteWithItems(keys KeyMap, items []list.Item) palette {
	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Command palette"
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	// The list's default keymap binds 'q' and Ctrl+C to Quit/ForceQuit. The
	// root model owns quit semantics — without this the user could exit by
	// pressing 'q' while the palette is open.
	l.KeyMap.Quit = key.Binding{}
	l.KeyMap.ForceQuit = key.Binding{}
	return palette{list: l, keys: keys}
}

// SetSize forwards a window-resize to the underlying list. The root model
// calls it from its tea.WindowSizeMsg handler.
func (p *palette) SetSize(w, h int) { p.list.SetSize(w, h) }

// Update routes a message to the palette. Esc returns a closePaletteMsg only
// when no filter is active — when the user is typing in or has applied a
// filter, the list's own handler clears the filter first. Enter emits a
// paletteSelectedMsg unless the user is mid-typing in the filter input, in
// which case the list commits the filter.
func (p palette) Update(msg tea.Msg) (palette, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch {
		case key.Matches(km, p.keys.ClosePalette):
			if p.list.FilterState() == list.Unfiltered {
				return p, func() tea.Msg { return closePaletteMsg{} }
			}
		case key.Matches(km, p.keys.Select):
			if p.list.FilterState() != list.Filtering {
				if it, ok := p.list.SelectedItem().(paletteItem); ok {
					sel := paletteSelectedMsg{Slash: it.slash, Title: it.title}
					return p, func() tea.Msg { return sel }
				}
			}
		}
	}
	var cmd tea.Cmd
	p.list, cmd = p.list.Update(msg)
	return p, cmd
}

// paletteStyle wraps the list view with consistent padding. Defined as a
// package-level var so it is built once at package init.
var paletteStyle = lipgloss.NewStyle().Padding(1, 2)

// View renders the palette.
func (p palette) View() string { return paletteStyle.Render(p.list.View()) }
