package tui

import (
	"log/slog"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// mode identifies which sub-screen is currently active.
type mode int

const (
	modeLauncher mode = iota
	modePalette
)

// paletteLoader is the source of palette rows the root model consults at
// startup and whenever a refreshPaletteMsg arrives. Launch supplies a real
// loader that calls pack.LoadPalette; tests pass nil or a stub so the root
// model stays independent of the discovery side effects.
type paletteLoader func() []list.Item

// app is the root tea.Model. It owns the keymap, help row, and per-screen
// sub-models, and routes incoming messages to the active screen.
type app struct {
	keys     KeyMap
	help     help.Model
	logger   *slog.Logger
	mode     mode
	launcher launcher
	palette  palette
	width    int
	height   int
	loadPal  paletteLoader
}

// newApp constructs the root model. The logger receives palette-selection
// events; passing nil disables those log lines but is otherwise harmless.
// loader is consulted on startup (via the initial Init command) and on
// every refreshPaletteMsg; pass nil to start with an empty palette (the
// posture used by unit tests that exercise only key handling).
func newApp(logger *slog.Logger, loader paletteLoader) app {
	keys := DefaultKeyMap()
	return app{
		keys:    keys,
		help:    help.New(),
		logger:  logger,
		mode:    modeLauncher,
		palette: newPalette(keys),
		loadPal: loader,
	}
}

// Init satisfies tea.Model. When a loader is configured, the root issues a
// single refreshPaletteMsg so the first frame renders against the real
// registry instead of an empty palette.
func (a app) Init() tea.Cmd {
	if a.loadPal == nil {
		return nil
	}
	return func() tea.Msg { return refreshPaletteMsg{} }
}

// Update implements tea.Model. The dispatch policy is:
//  1. tea.WindowSizeMsg → propagate to every sub-model.
//  2. closePaletteMsg / paletteSelectedMsg → handle here, then return to launcher.
//  3. tea.KeyMsg + "ctrl+c" → quit unconditionally so the user always has an
//     escape hatch even when typing in the palette filter.
//  4. In palette mode, every other key goes to the palette so the filter
//     input receives them verbatim (notably 'q' and '/').
//  5. In launcher mode, global keys (Quit, OpenPalette, ToggleHelp) are
//     handled here before falling through to the launcher.
func (a app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = m.Width, m.Height
		a.help.Width = m.Width
		a.launcher.SetSize(m.Width, m.Height)
		a.palette.SetSize(m.Width, max(1, m.Height-1))
		return a, nil

	case closePaletteMsg:
		a.mode = modeLauncher
		return a, nil

	case paletteSelectedMsg:
		if a.logger != nil {
			a.logger.Info("palette selection",
				slog.String("slash", m.Slash),
				slog.String("title", m.Title))
		}
		a.mode = modeLauncher
		return a, nil

	case refreshPaletteMsg:
		if a.loadPal != nil {
			a.palette.SetItems(a.loadPal())
		}
		return a, nil

	case tea.KeyMsg:
		if m.String() == "ctrl+c" {
			return a, tea.Quit
		}
		if a.mode == modePalette {
			var cmd tea.Cmd
			a.palette, cmd = a.palette.Update(msg)
			return a, cmd
		}
		switch {
		case key.Matches(m, a.keys.Quit):
			return a, tea.Quit
		case key.Matches(m, a.keys.OpenPalette):
			a.mode = modePalette
			return a, nil
		case key.Matches(m, a.keys.ToggleHelp):
			a.help.ShowAll = !a.help.ShowAll
			return a, nil
		}
	}

	if a.mode == modePalette {
		var cmd tea.Cmd
		a.palette, cmd = a.palette.Update(msg)
		return a, cmd
	}
	return a, nil
}

// View renders the active screen with the help row underneath.
func (a app) View() string {
	var body string
	if a.mode == modePalette {
		body = a.palette.View()
	} else {
		body = a.launcher.View()
	}
	return lipgloss.JoinVertical(lipgloss.Left, body, a.help.View(a.keys))
}
