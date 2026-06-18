package tui

import (
	"context"
	"log/slog"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bannaarr01/packwright/awsx"
	"github.com/bannaarr01/packwright/tui/screens"
)

// tuiVerifier implements the ProfileSwitcher's Verifier dependency by
// constructing a fresh awsx.Client for the pick and running
// awsx.Verify. It is the TUI counterpart of the GUI's
// profileSwitcherDeps wiring.
type tuiVerifier struct {
	home   string
	logger *slog.Logger
}

// newTUIVerifier returns a Verifier that builds awsx clients rooted at
// home. The logger receives the underlying verify outcome.
func newTUIVerifier(home string, logger *slog.Logger) *tuiVerifier {
	return &tuiVerifier{home: home, logger: logger}
}

// Verify constructs an awsx client for the profile/region pair and runs
// STS GetCallerIdentity through awsx.Verify, returning the resulting
// Identity (or the structured error awsx surfaces).
func (v *tuiVerifier) Verify(ctx context.Context, profile, region string) (*awsx.Identity, error) {
	client, err := awsx.New(ctx, profile, region, v.home, v.logger)
	if err != nil {
		return nil, err
	}
	return awsx.Verify(ctx, client)
}

// This file adapts the existing concrete screen types (launcher, chat,
// audit, profile) to the screens.Screen interface used by the shell's
// registry. Wrapping rather than modifying the original Update methods
// preserves their existing per-screen tests and lets each model keep its
// idiomatic (T, tea.Cmd) signature.
//
// The wrappers also translate the legacy "leave" messages (leaveChatMsg,
// leaveAuditMsg, closePaletteMsg from ProfileSwitcher) into the registry's
// canonical PopMsg so the root model has a single navigation channel.

// launcherScreen wraps the launcher placeholder in the Screen interface.
type launcherScreen struct {
	inner launcher
}

func newLauncherScreen() *launcherScreen { return &launcherScreen{} }

// Init returns nil — the launcher has no startup work.
func (s *launcherScreen) Init() tea.Cmd { return nil }

// Update is a no-op: the launcher consumes no keys today. The screen
// stays on the stack until the root replaces or pushes another screen.
func (s *launcherScreen) Update(_ tea.Msg) (screens.Screen, tea.Cmd) { return s, nil }

// View delegates to the underlying launcher renderer.
func (s *launcherScreen) View() string { return s.inner.View() }

// SetSize forwards the content-pane size to the launcher.
func (s *launcherScreen) SetSize(w, h int) { s.inner.SetSize(w, h) }

// KeyMap returns the launcher's screen-local bindings — currently none;
// the global bindings on the footer are sufficient.
func (s *launcherScreen) KeyMap() []key.Binding { return nil }

// Title is the human-readable label rendered above the content pane.
func (s *launcherScreen) Title() string { return "Launcher" }

// chatScreen wraps the AI chat panel.
type chatScreen struct {
	inner chatModel
}

func newChatScreen(m chatModel) *chatScreen { return &chatScreen{inner: m} }

// Init runs the chat panel's session-building command (or nil when AI
// is disabled).
func (s *chatScreen) Init() tea.Cmd { return s.inner.initCmd() }

// Update forwards to chatModel.Update and translates the legacy
// leaveChatMsg cmd into a registry PopMsg so the root model only has to
// observe one navigation message.
func (s *chatScreen) Update(msg tea.Msg) (screens.Screen, tea.Cmd) {
	next, cmd := s.inner.Update(msg)
	s.inner = next
	return s, translateLeave(cmd)
}

// View delegates to chatModel.View, which owns its own layout.
func (s *chatScreen) View() string { return s.inner.View() }

// SetSize forwards to chatModel.setSize (pointer-receiver method).
func (s *chatScreen) SetSize(w, h int) { s.inner.setSize(w, h) }

// KeyMap returns no screen-local bindings — the chat panel renders its
// own footer (input or consent prompt) so adding to the shell footer
// would just duplicate hints.
func (s *chatScreen) KeyMap() []key.Binding { return nil }

// Title is the human-readable label rendered above the content pane.
func (s *chatScreen) Title() string { return "AI assistant" }

// auditScreen wraps the read-only AWS inventory panel.
type auditScreen struct {
	inner auditModel
}

func newAuditScreen(m auditModel) *auditScreen { return &auditScreen{inner: m} }

// Init kicks off the scan.
func (s *auditScreen) Init() tea.Cmd { return s.inner.initCmd() }

// Update forwards to auditModel.Update and translates leaveAuditMsg
// into a PopMsg the registry can act on.
func (s *auditScreen) Update(msg tea.Msg) (screens.Screen, tea.Cmd) {
	next, cmd := s.inner.Update(msg)
	s.inner = next
	return s, translateLeave(cmd)
}

// View delegates to auditModel.View, which owns its own layout.
func (s *auditScreen) View() string { return s.inner.View() }

// SetSize fires a WindowSizeMsg into auditModel — the audit panel uses
// the bubbletea convention of sizing through tea.WindowSizeMsg, so we
// route through Update rather than touching internal fields.
func (s *auditScreen) SetSize(w, h int) {
	s.inner, _ = s.inner.Update(tea.WindowSizeMsg{Width: w, Height: h})
}

// KeyMap returns no screen-local bindings — the audit panel renders its
// own footer hint row, same reasoning as chat.
func (s *auditScreen) KeyMap() []key.Binding { return nil }

// Title is the human-readable label rendered above the content pane.
func (s *auditScreen) Title() string { return "Audit" }

// profileScreen wraps the ProfileSwitcher (PR-07's /profile screen).
type profileScreen struct {
	inner ProfileSwitcher
}

func newProfileScreen(ps ProfileSwitcher) *profileScreen { return &profileScreen{inner: ps} }

// Init is a no-op — the switcher is fully populated at construction.
func (s *profileScreen) Init() tea.Cmd { return nil }

// Update forwards to ProfileSwitcher.Update and translates its
// closePaletteMsg ("Esc with no filter") into a PopMsg.
func (s *profileScreen) Update(msg tea.Msg) (screens.Screen, tea.Cmd) {
	next, cmd := s.inner.Update(msg)
	s.inner = next
	return s, translateLeave(cmd)
}

// View delegates to ProfileSwitcher.View.
func (s *profileScreen) View() string { return s.inner.View() }

// SetSize forwards the content-pane size.
func (s *profileScreen) SetSize(w, h int) { s.inner.SetSize(w, h) }

// KeyMap returns the switcher's screen-local bindings.
func (s *profileScreen) KeyMap() []key.Binding {
	// The switcher inherits the global keymap bindings; only the
	// filter-aware Enter is unique enough to surface separately.
	return nil
}

// Title is the human-readable label rendered above the content pane.
func (s *profileScreen) Title() string { return "Switch AWS profile" }

// translateLeave wraps a screen-emitted tea.Cmd so that any legacy
// "leave" message (leaveChatMsg, leaveAuditMsg, closePaletteMsg) is
// replaced with a registry PopMsg. Other messages pass through
// unchanged so the root model still sees, e.g., consent requests.
func translateLeave(c tea.Cmd) tea.Cmd {
	if c == nil {
		return nil
	}
	return func() tea.Msg {
		m := c()
		switch m.(type) {
		case leaveChatMsg, leaveAuditMsg, closePaletteMsg:
			return screens.PopMsg{}
		}
		return m
	}
}
