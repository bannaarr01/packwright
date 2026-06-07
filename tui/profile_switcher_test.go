package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bannaarr01/packwright/awsx"
)

// fakeVerifier is a deterministic Verifier the tests drive instead of awsx.
type fakeVerifier struct {
	identity *awsx.Identity
	err      error
	gotName  string
	gotRegn  string
}

func (f *fakeVerifier) Verify(_ context.Context, profile, region string) (*awsx.Identity, error) {
	f.gotName = profile
	f.gotRegn = region
	return f.identity, f.err
}

// newTestSwitcher returns a switcher seeded with three profiles, sized large
// enough for the list to render at least one row, with the second profile
// marked active.
func newTestSwitcher(v Verifier) ProfileSwitcher {
	s := NewProfileSwitcher(DefaultKeyMap(),
		[]awsx.Profile{
			{Name: "alpha", Region: "us-east-1"},
			{Name: "beta", Region: "eu-west-1"},
			{Name: "gamma"},
		},
		"beta", v, nil,
	)
	s.SetSize(80, 24)
	return s
}

func TestProfileSwitcherActiveItemHasMarker(t *testing.T) {
	s := newTestSwitcher(nil)
	rendered := s.View()
	if !strings.Contains(rendered, "→ beta") {
		t.Fatalf("View() did not show active marker for beta:\n%s", rendered)
	}
}

func TestProfileSwitcherEnterTriggersVerify(t *testing.T) {
	fv := &fakeVerifier{
		identity: &awsx.Identity{Account: "111122223333", Arn: "arn:aws:iam::111122223333:user/jdoe"},
	}
	s := newTestSwitcher(fv)
	// The list starts with alpha selected (index 0). Press Enter.
	_, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter returned nil cmd; expected verify command")
	}
	msg := cmd()
	got, ok := msg.(ProfileSwitcherMsg)
	if !ok {
		t.Fatalf("cmd produced %T, want ProfileSwitcherMsg", msg)
	}
	if got.Profile != "alpha" || got.Region != "us-east-1" {
		t.Errorf("emitted msg = %+v, want profile=alpha region=us-east-1", got)
	}
	if got.Err != nil {
		t.Errorf("Err = %v, want nil", got.Err)
	}
	if got.Identity == nil || got.Identity.Account != "111122223333" {
		t.Errorf("Identity = %+v, want Account=111122223333", got.Identity)
	}
	if fv.gotName != "alpha" {
		t.Errorf("Verifier saw profile=%q, want alpha", fv.gotName)
	}
}

func TestProfileSwitcherEnterSurfacesVerifyError(t *testing.T) {
	wantErr := errors.New("expired sso")
	fv := &fakeVerifier{err: wantErr}
	s := newTestSwitcher(fv)
	_, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter returned nil cmd")
	}
	got := cmd().(ProfileSwitcherMsg)
	if !errors.Is(got.Err, wantErr) {
		t.Errorf("Err = %v, want %v", got.Err, wantErr)
	}
	if got.Identity != nil {
		t.Errorf("Identity = %+v, want nil when verify fails", got.Identity)
	}
}

func TestProfileSwitcherEscEmitsClose(t *testing.T) {
	s := newTestSwitcher(nil)
	_, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("Esc returned nil cmd")
	}
	if _, ok := cmd().(closePaletteMsg); !ok {
		t.Errorf("Esc produced %T, want closePaletteMsg", cmd())
	}
}

func TestProfileSwitcherNilVerifierStillEmitsMsg(t *testing.T) {
	s := newTestSwitcher(nil)
	_, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter with nil verifier returned nil cmd")
	}
	got := cmd().(ProfileSwitcherMsg)
	if got.Profile != "alpha" {
		t.Errorf("Profile = %q, want alpha", got.Profile)
	}
	if got.Err != nil || got.Identity != nil {
		t.Errorf("Identity/Err should both be nil with nil verifier; got %+v", got)
	}
}

func TestProfileSwitcherEmptyRegionShowsPlaceholder(t *testing.T) {
	s := newTestSwitcher(nil)
	if !strings.Contains(s.View(), "(no region set)") {
		t.Errorf("expected placeholder for gamma profile; got:\n%s", s.View())
	}
}
