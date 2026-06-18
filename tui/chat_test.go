package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/ai/consent"
	"github.com/bannaarr01/packwright/tui/screens"
)

// keyRunes builds a rune keypress message for a single character.
func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// TestApp_AISlashPushesChatScreen_Disabled verifies that selecting
// /ai in the palette pushes the chat screen onto the registry. With
// AI disabled the screen shows the setup hint (no live session is
// built), but it must still be reachable.
func TestApp_AISlashPushesChatScreen_Disabled(t *testing.T) {
	a := newApp(nil, nil)
	a.cfg = &config.Config{} // AI absent => disabled
	a.width, a.height = 80, 24

	m, cmd := a.Update(paletteSelectedMsg{Slash: "/ai", Title: "AI assistant"})
	na := m.(app)
	if _, ok := na.registry.Top().(*chatScreen); !ok {
		t.Fatalf("top screen = %T, want *chatScreen", na.registry.Top())
	}
	if cmd != nil {
		t.Fatal("expected no init command for the disabled chat panel")
	}
	// The hint scrolls to the bottom on render; "ai setup" stays in
	// view regardless of viewport size.
	if view := na.View(); !strings.Contains(view, "ai setup") {
		t.Fatalf("disabled panel should show the setup hint; view:\n%s", view)
	}
}

// TestApp_NonAISlashLeavesRegistryAtHome verifies that an unrouted
// palette selection (e.g. /new-pack) just closes the overlay — the
// registry doesn't grow.
func TestApp_NonAISlashLeavesRegistryAtHome(t *testing.T) {
	a := newApp(nil, nil)
	m, _ := a.Update(paletteSelectedMsg{Slash: "/new-pack", Title: "New pack"})
	na := m.(app)
	if d := na.registry.Depth(); d != 1 {
		t.Fatalf("registry depth = %d, want 1 (no screen pushed)", d)
	}
}

// TestApp_PopMessagePopsRegistry verifies the canonical Pop path used
// by every screen's Esc handler — the chat screen's wrapper translates
// leaveChatMsg into screens.PopMsg, so the assertion that a Pop
// shrinks the stack covers the chat-leave path too.
func TestApp_PopMessagePopsRegistry(t *testing.T) {
	a := newApp(nil, nil)
	a.cfg = &config.Config{}
	a.width, a.height = 80, 24
	m, _ := a.Update(paletteSelectedMsg{Slash: "/ai", Title: "AI assistant"})
	na := m.(app)
	if na.registry.Depth() != 2 {
		t.Fatalf("setup: depth = %d, want 2", na.registry.Depth())
	}
	m, _ = na.Update(screens.PopMsg{})
	na = m.(app)
	if na.registry.Depth() != 1 {
		t.Fatalf("after PopMsg, depth = %d, want 1", na.registry.Depth())
	}
}

// TestApp_ConsentRequestDeniedWhenNotInChat verifies the fail-closed
// posture: a consent request that arrives while no chat screen is on
// top is denied so the engine's blocked tool goroutine never leaks.
func TestApp_ConsentRequestDeniedWhenNotInChat(t *testing.T) {
	a := newApp(nil, nil)
	reply := make(chan consent.Decision, 1)
	a.Update(consentRequestMsg{req: consent.Request{Tool: "cfn/update-stack"}, reply: reply})
	select {
	case d := <-reply:
		if d != consent.Deny {
			t.Fatalf("decision = %v, want Deny", d)
		}
	default:
		t.Fatal("no consent decision was sent")
	}
}

// TestChatModel_ConsentKeysMapToDecisions verifies the consent modal's
// key-to-decision mapping. This exercises the chat sub-model directly;
// it doesn't go through the screens wrapper.
func TestChatModel_ConsentKeysMapToDecisions(t *testing.T) {
	cases := []struct {
		key  tea.KeyMsg
		want consent.Decision
	}{
		{keyRunes("y"), consent.ApproveOnce},
		{keyRunes("s"), consent.ApproveSession},
		{keyRunes("n"), consent.Deny},
		{tea.KeyMsg{Type: tea.KeyEsc}, consent.Deny},
	}
	for _, tc := range cases {
		m := newChatModel(DefaultKeyMap(), nil, 80, 24, &config.Config{}, "", true)
		reply := make(chan consent.Decision, 1)
		m.pending = &pendingConsent{req: consent.Request{Tool: "cfn/update-stack"}, reply: reply}

		m2, _ := m.Update(tc.key)
		select {
		case d := <-reply:
			if d != tc.want {
				t.Fatalf("key %q: decision = %v, want %v", tc.key.String(), d, tc.want)
			}
		default:
			t.Fatalf("key %q: no decision sent", tc.key.String())
		}
		if m2.pending != nil {
			t.Fatalf("key %q: modal not dismissed after decision", tc.key.String())
		}
	}
}

// TestChatModel_DisabledShowsSetupHint verifies the disabled-panel
// content path is untouched by the shell redesign.
func TestChatModel_DisabledShowsSetupHint(t *testing.T) {
	m := newChatModel(DefaultKeyMap(), nil, 80, 24, &config.Config{}, "", false)
	if m.initCmd() != nil {
		t.Fatal("disabled panel must not issue an init command")
	}
	if view := m.View(); !strings.Contains(view, "ai setup") {
		t.Fatalf("disabled view should mention `ai setup`; got:\n%s", view)
	}
}
