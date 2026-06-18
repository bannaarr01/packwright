// Package screens defines the contract every TUI screen satisfies plus the
// stack that the root model uses to navigate between them.
//
// A screen is anything that renders into the content pane (the area to the
// right of the persistent sidebar and below the header). The root model
// holds a *Registry; the screen at the top of the stack receives Update
// calls and renders View(). Push descends into a new screen; Pop returns
// to the previous one. The bottom of the stack (the launcher) cannot be
// popped — that's how Esc stops bubbling out of the app.
//
// The Screen interface is intentionally narrow: bubbletea's Update/View
// for the core loop, SetSize for the layout pass, KeyMap for the footer's
// top line, Title for the header strip, and Init for the one-shot command
// fired when a screen is first pushed.
package screens

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// Screen is one entry in the registry stack. Every screen the root model
// can navigate into satisfies this interface.
//
// Update returns Screen rather than the concrete type so screens can swap
// themselves out in-place (e.g. a wizard moving between its own pages)
// without the root model having to know about the swap.
type Screen interface {
	// Init runs once when the screen is pushed onto the registry. It
	// returns the initial command, or nil if the screen needs none.
	Init() tea.Cmd
	// Update processes one bubbletea message and returns the (possibly
	// replaced) screen plus any follow-up command.
	Update(msg tea.Msg) (Screen, tea.Cmd)
	// View renders the screen body. The root model has already sized
	// the screen via SetSize before View is called.
	View() string
	// SetSize informs the screen of the content-pane dimensions. The
	// root model calls it after every WindowSizeMsg and after sidebar
	// resize.
	SetSize(w, h int)
	// KeyMap returns the screen-local bindings the footer renders on
	// its top line. The persistent global keymap is rendered below.
	KeyMap() []key.Binding
	// Title is the human-readable label rendered above the content
	// pane, e.g. "Launcher" or "Record: demo/dev/api".
	Title() string
}

// PopMsg asks the registry to pop the current screen. Screens emit it
// from their Update when they want to dismiss themselves (typically on
// Esc).
type PopMsg struct{}

// PushMsg asks the registry to push a new screen on top of the current
// one. Screens emit it when they navigate into another screen — e.g. the
// sidebar emitting a record screen when the user picks a stack row.
type PushMsg struct {
	// Screen is the new top-of-stack screen.
	Screen Screen
}

// Registry is the screen stack. It is owned by the root model; screens
// themselves never see it directly — they navigate by returning PopMsg /
// PushMsg from their Update.
type Registry struct {
	stack []Screen
}

// New returns a registry whose bottom (un-poppable) screen is home.
func New(home Screen) *Registry {
	return &Registry{stack: []Screen{home}}
}

// Top returns the screen currently receiving Update calls and rendering
// the content pane. The registry is always non-empty by construction.
func (r *Registry) Top() Screen {
	return r.stack[len(r.stack)-1]
}

// Push adds s to the top of the stack and returns its Init command (or
// nil). Callers route the returned command through the bubbletea loop.
func (r *Registry) Push(s Screen) tea.Cmd {
	r.stack = append(r.stack, s)
	return s.Init()
}

// Pop removes the top screen and returns true. Popping the home screen
// is a no-op and returns false — Esc on the launcher should not crash
// the program.
func (r *Registry) Pop() bool {
	if len(r.stack) <= 1 {
		return false
	}
	r.stack = r.stack[:len(r.stack)-1]
	return true
}

// Replace swaps the top screen for s without changing depth. Used when a
// screen wants to forward to a different screen without leaving a
// breadcrumb in the stack.
func (r *Registry) Replace(s Screen) tea.Cmd {
	r.stack[len(r.stack)-1] = s
	return s.Init()
}

// Depth is the number of screens on the stack. The home screen counts as
// depth 1; pushing one screen makes the depth 2.
func (r *Registry) Depth() int {
	return len(r.stack)
}

// UpdateTop forwards msg to the top screen and stores any in-place
// replacement back on the stack. Returns the follow-up command, if any.
//
// PopMsg and PushMsg returned in the command are intentionally not
// short-circuited here: the root model unwraps them so the navigation is
// observable at the root.
func (r *Registry) UpdateTop(msg tea.Msg) tea.Cmd {
	s, cmd := r.Top().Update(msg)
	r.stack[len(r.stack)-1] = s
	return cmd
}

// SetSize forwards the content-pane size to every screen on the stack
// so screens that aren't currently visible (but might come back via Pop)
// are also laid out for the current terminal.
func (r *Registry) SetSize(w, h int) {
	for _, s := range r.stack {
		s.SetSize(w, h)
	}
}
