package gui

import (
	"testing"

	"github.com/bannaarr01/packwright/internal/ai/consent"
)

// TestDecodeConsentFailClosed proves the frontend → engine decision mapping is
// fail-closed: only the two explicit approve strings approve; everything else
// (including an empty or garbage value) denies.
func TestDecodeConsentFailClosed(t *testing.T) {
	cases := map[string]consent.Decision{
		"approve_once":    consent.ApproveOnce,
		"approve_session": consent.ApproveSession,
		"deny":            consent.Deny,
		"":                consent.Deny,
		"yes":             consent.Deny,
		"APPROVE_ONCE":    consent.Deny, // case-sensitive on purpose
	}
	for in, want := range cases {
		if got := decodeConsent(in); got != want {
			t.Errorf("decodeConsent(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestAIEnabledDefaultFalse proves AI defaults off when no config enables it —
// the GUI must not present a live assistant on a fresh install.
func TestAIEnabledDefaultFalse(t *testing.T) {
	t.Setenv("PACKWRIGHT_HOME", t.TempDir())
	if newApp(nil).AIEnabled() {
		t.Error("AIEnabled()=true on a fresh home; want false (AI is opt-in)")
	}
}

// TestStartAISessionDisabled proves StartAISession fails closed with a setup
// hint when AI is not enabled, rather than opening a dead session.
func TestStartAISessionDisabled(t *testing.T) {
	t.Setenv("PACKWRIGHT_HOME", t.TempDir())
	got := newApp(nil).StartAISession()
	if got.OK {
		t.Fatal("StartAISession OK=true with AI disabled; want failure")
	}
	if got.Error == "" {
		t.Error("StartAISession Error empty; want a setup hint")
	}
}

// TestRespondAIConsentNoPending guards the defensive path: answering a consent
// prompt when none is pending must be a no-op, not a panic or a blocked send.
func TestRespondAIConsentNoPending(t *testing.T) {
	a := newApp(nil)
	a.RespondAIConsent("approve_once") // must not panic or block
}

// TestCloseAISessionIdempotent proves tearing down with no live session is safe
// (App.shutdown calls it unconditionally).
func TestCloseAISessionIdempotent(t *testing.T) {
	a := newApp(nil)
	a.CloseAISession()
	a.CloseAISession()
}

// TestSendAIMessageNoSession proves sending with no session does not panic; with
// no wails runtime the error event is simply dropped.
func TestSendAIMessageNoSession(t *testing.T) {
	a := newApp(nil)
	a.SendAIMessage("hello") // no session, no wailsCtx → no-op, no panic
}
