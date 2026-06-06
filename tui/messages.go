package tui

// closePaletteMsg requests the root model to dismiss the palette and return
// to the launcher. It is emitted by the palette when the user presses Esc
// while no filter is active.
type closePaletteMsg struct{}

// paletteSelectedMsg is emitted by the palette when the user picks an item.
// The root model logs the selection and returns to the launcher. Once the
// pack registry lands, the slash will be routed to the appropriate handler.
type paletteSelectedMsg struct {
	Slash string
	Title string
}
