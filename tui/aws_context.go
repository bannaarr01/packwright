package tui

import "github.com/charmbracelet/bubbles/list"

// Built-in AWS-context slash labels. Like the workspace rows, these commands
// are handled directly by the root model (handlePaletteSelection) rather than
// backed by a dispatch manifest, so pack.LoadPalette never surfaces them.
const (
	slashProfile = "/profile"
	slashRegion  = "/region"
)

// awsContextPaletteItems returns the built-in AWS-context rows (/profile and
// /region) the palette offers alongside the manifest-backed and workspace rows.
// The palette only routes items it lists (see palette.go), and neither
// pack.LoadPalette nor workspacePaletteItems surfaces these, so buildPaletteLoader
// seeds them here on every refresh.
func awsContextPaletteItems() []list.Item {
	return []list.Item{
		paletteItem{slash: slashProfile, title: "Switch AWS profile"},
		paletteItem{slash: slashRegion, title: "Switch AWS region"},
	}
}
