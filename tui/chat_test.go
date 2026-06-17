package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/ai/consent"
)

// keyRunes builds a rune keypress message for a single character.
func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestApp_AISlashOpensChatPanel_Disabled(t *testing.T) {
	a := newApp(nil, nil)
	a.cfg = &config.Config{} // AI absent => disabled
	a.width, a.height = 80, 24

	m, cmd := a.Update(paletteSelectedMsg{Slash: "/ai", Title: "AI assistant"})
	na := m.(app)
	if na.mode != modeChat {
		t.Fatalf("mode = %v, want modeChat", na.mode)
	}
	// Disabled => no session-building command is issued.
	if cmd != nil {
		t.Fatal("expected no init command for the disabled panel")
	}
	if view := na.View(); !strings.Contains(view, "disabled") {
		t.Fatalf("disabled panel should show the setup hint; view:\n%s", view)
	}
}

func TestApp_NonAISlashReturnsToLauncher(t *testing.T) {
	a := newApp(nil, nil)
	a.mode = modePalette
	m, _ := a.Update(paletteSelectedMsg{Slash: "/new-pack", Title: "New pack"})
	if na := m.(app); na.mode != modeLauncher {
		t.Fatalf("mode = %v, want modeLauncher", na.mode)
	}
}

func TestApp_LeaveChatReturnsToLauncher(t *testing.T) {
	a := newApp(nil, nil)
	a.mode = modeChat
	m, _ := a.Update(leaveChatMsg{})
	if na := m.(app); na.mode != modeLauncher {
		t.Fatalf("mode = %v, want modeLauncher", na.mode)
	}
}

func TestApp_ConsentRequestDeniedWhenNotInChat(t *testing.T) {
	// A consent request that arrives while the panel is closed must be denied
	// so the engine's blocked tool goroutine never leaks (fail-closed).
	a := newApp(nil, nil)
	a.mode = modeLauncher
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

func TestChatModel_DisabledShowsSetupHint(t *testing.T) {
	m := newChatModel(DefaultKeyMap(), nil, 80, 24, &config.Config{}, "", false)
	if m.initCmd() != nil {
		t.Fatal("disabled panel must not issue an init command")
	}
	if view := m.View(); !strings.Contains(view, "ai setup") {
		t.Fatalf("disabled view should mention `ai setup`; got:\n%s", view)
	}
}
