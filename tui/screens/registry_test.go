package screens

import (
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// fakeScreen is a deterministic Screen used to exercise the registry's
// Push / Pop / Top / Replace semantics without dragging in real screens.
type fakeScreen struct {
	name string
	w, h int
	init tea.Cmd
}

func (s *fakeScreen) Init() tea.Cmd                      { return s.init }
func (s *fakeScreen) Update(_ tea.Msg) (Screen, tea.Cmd) { return s, nil }
func (s *fakeScreen) View() string                       { return s.name }
func (s *fakeScreen) SetSize(w, h int)                   { s.w, s.h = w, h }
func (s *fakeScreen) KeyMap() []key.Binding              { return nil }
func (s *fakeScreen) Title() string                      { return s.name }

func TestRegistryPushPopBalanceAndDepth(t *testing.T) {
	r := New(&fakeScreen{name: "home"})
	if r.Depth() != 1 {
		t.Fatalf("initial depth = %d, want 1", r.Depth())
	}
	r.Push(&fakeScreen{name: "a"})
	r.Push(&fakeScreen{name: "b"})
	if r.Depth() != 3 {
		t.Fatalf("after 2 pushes, depth = %d, want 3", r.Depth())
	}
	if r.Top().Title() != "b" {
		t.Errorf("top.Title() = %q, want %q", r.Top().Title(), "b")
	}
	if !r.Pop() {
		t.Error("Pop() = false, want true (above home)")
	}
	if r.Top().Title() != "a" {
		t.Errorf("after pop, top.Title() = %q, want %q", r.Top().Title(), "a")
	}
	r.Pop()
	if r.Pop() {
		t.Error("Pop() at home returned true; the home screen must be unpoppable")
	}
	if r.Top().Title() != "home" {
		t.Errorf("after redundant pop, top.Title() = %q, want %q", r.Top().Title(), "home")
	}
}

func TestRegistryPushRunsInitCommand(t *testing.T) {
	r := New(&fakeScreen{name: "home"})
	called := false
	cmd := r.Push(&fakeScreen{name: "a", init: func() tea.Msg { called = true; return nil }})
	if cmd == nil {
		t.Fatal("Push() returned nil cmd; want the screen's Init command")
	}
	cmd()
	if !called {
		t.Error("Init command did not fire")
	}
}

func TestRegistrySetSizePropagatesToAllScreens(t *testing.T) {
	home := &fakeScreen{name: "home"}
	mid := &fakeScreen{name: "mid"}
	top := &fakeScreen{name: "top"}
	r := New(home)
	r.Push(mid)
	r.Push(top)
	r.SetSize(40, 12)
	for _, s := range []*fakeScreen{home, mid, top} {
		if s.w != 40 || s.h != 12 {
			t.Errorf("%s: size = (%d, %d), want (40, 12)", s.name, s.w, s.h)
		}
	}
}

func TestRegistryReplaceSwapsTopWithoutChangingDepth(t *testing.T) {
	r := New(&fakeScreen{name: "home"})
	r.Push(&fakeScreen{name: "a"})
	if r.Depth() != 2 {
		t.Fatalf("setup: depth = %d, want 2", r.Depth())
	}
	r.Replace(&fakeScreen{name: "b"})
	if r.Depth() != 2 {
		t.Errorf("Replace changed depth: got %d, want 2", r.Depth())
	}
	if r.Top().Title() != "b" {
		t.Errorf("top.Title() = %q, want %q", r.Top().Title(), "b")
	}
}
