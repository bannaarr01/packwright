package tui

import (
	"github.com/bannaarr01/packwright/internal/ai/chat"
	"github.com/bannaarr01/packwright/internal/ai/consent"
)

// leaveChatMsg asks the root model to close the AI chat panel and return to
// the launcher. The chat sub-model emits it when the user presses Esc.
type leaveChatMsg struct{}

// chatReadyMsg carries the result of constructing the AI session off the UI
// goroutine (see buildSessionCmd). A non-nil err means the session could not
// start (no key, disabled, provider build failure); the panel renders it.
type chatReadyMsg struct {
	session *chat.Session
	err     error
}

// aiStreamMsg delivers one event from an in-flight turn's channel. ok is false
// when the channel has closed, which finalizes the turn. ch is carried so the
// Update loop can re-arm the reader for the next event.
type aiStreamMsg struct {
	ev chat.Event
	ch <-chan chat.Event
	ok bool
}

// consentRequestMsg bridges the engine's synchronous consent.ShowModal call
// into the bubbletea event loop: the tool goroutine is blocked on reply while
// the user answers the modal. It is delivered via (*tea.Program).Send from the
// ShowModal override installed in Launch.
type consentRequestMsg struct {
	req   consent.Request
	reply chan consent.Decision
}

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

// refreshPaletteMsg requests the root model to re-load the palette contents
// from disk. The Launch path's watcher goroutine sends this through
// (*tea.Program).Send when a manifest file under one of the watched roots
// changes, so the user sees their edits reflected without restarting.
type refreshPaletteMsg struct{}
