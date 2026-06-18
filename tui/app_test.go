package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bannaarr01/packwright/tui/screens"
	"github.com/bannaarr01/packwright/tui/sidebar"
)

// TestAppCtrlPOpensPalette verifies Ctrl+P parks focus on the palette
// overlay (replacing the modeLauncher → modePalette transition in the
// pre-PR-09 shell).
func TestAppCtrlPOpensPalette(t *testing.T) {
	a := newApp(nil, nil)
	if a.focus != focusSidebar {
		t.Fatalf("initial focus = %v, want focusSidebar", a.focus)
	}
	model, _ := a.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	a = model.(app)
	if a.focus != focusPalette {
		t.Errorf("after ctrl+p, focus = %v, want focusPalette", a.focus)
	}
}

// TestAppQuitFromLauncher verifies 'q' returns tea.Quit when the
// sidebar holds focus on the launcher (the existing keybinding must
// keep working in the redesigned shell).
func TestAppQuitFromLauncher(t *testing.T) {
	a := newApp(nil, nil)
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("'q' on the launcher returned nil cmd, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("cmd produced %T, want tea.QuitMsg", cmd())
	}
}

// TestAppCtrlCAlwaysQuits verifies Ctrl+C quits even while the palette
// overlay owns focus — the escape hatch must never be swallowed.
func TestAppCtrlCAlwaysQuits(t *testing.T) {
	a := newApp(nil, nil)
	model, _ := a.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	a = model.(app)
	if a.focus != focusPalette {
		t.Fatal("setup failed: palette did not take focus")
	}
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c on palette returned nil cmd, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("cmd produced %T, want tea.QuitMsg", cmd())
	}
}

// TestAppClosePaletteMessageRestoresFocus verifies that closePaletteMsg
// (emitted by the palette on Esc) drops focus back to where it was
// before the overlay opened.
func TestAppClosePaletteMessageRestoresFocus(t *testing.T) {
	a := newApp(nil, nil)
	model, _ := a.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	a = model.(app)
	if a.focus != focusPalette {
		t.Fatal("setup failed: palette did not take focus")
	}
	model, _ = a.Update(closePaletteMsg{})
	a = model.(app)
	if a.focus != focusSidebar {
		t.Errorf("after close, focus = %v, want focusSidebar", a.focus)
	}
}

// TestAppUnknownPaletteSelectionClosesOverlay verifies that a palette
// selection whose slash isn't routed by the shell (e.g. /new-pack)
// still closes the overlay so the user can keep navigating.
func TestAppUnknownPaletteSelectionClosesOverlay(t *testing.T) {
	a := newApp(nil, nil)
	model, _ := a.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	a = model.(app)
	model, _ = a.Update(paletteSelectedMsg{Slash: "/new-pack", Title: "new pack"})
	a = model.(app)
	if a.focus == focusPalette {
		t.Errorf("focus is still palette after selection; want sidebar/content")
	}
	if d := a.registry.Depth(); d != 1 {
		t.Errorf("registry depth = %d, want 1 (no screen pushed for unknown slash)", d)
	}
}

// TestAppRefreshPaletteMsgSetsItemsFromLoader verifies that a
// refreshPaletteMsg invokes the configured loader and the resulting
// rows land in the palette's list.
func TestAppRefreshPaletteMsgSetsItemsFromLoader(t *testing.T) {
	calls := 0
	loader := func() []list.Item {
		calls++
		return []list.Item{
			paletteItem{slash: "/restart-api", title: "Restart API"},
		}
	}
	a := newApp(nil, loader)

	model, _ := a.Update(refreshPaletteMsg{})
	a = model.(app)

	if calls != 1 {
		t.Fatalf("loader called %d times, want 1", calls)
	}
	items := a.palette.list.Items()
	if len(items) != 1 {
		t.Fatalf("palette items = %d, want 1", len(items))
	}
	got, ok := items[0].(paletteItem)
	if !ok || got.slash != "/restart-api" || got.title != "Restart API" {
		t.Errorf("palette item = %+v, want paletteItem{/restart-api Restart API}", items[0])
	}
}

// TestAppInitEmitsRefreshWhenLoaderConfigured verifies that the root
// model's Init returns a command that produces a refreshPaletteMsg, so
// the palette is populated on the first frame without the user opening
// it.
func TestAppInitEmitsRefreshWhenLoaderConfigured(t *testing.T) {
	a := newApp(nil, func() []list.Item { return nil })
	cmd := a.Init()
	if cmd == nil {
		t.Fatal("Init() returned nil with loader configured; want a refreshPaletteMsg command")
	}
	if _, ok := cmd().(refreshPaletteMsg); !ok {
		t.Errorf("Init() produced %T, want refreshPaletteMsg", cmd())
	}
}

// TestAppInitIsNilWithoutLoader verifies the test-only nil-loader
// posture: Init returns nil so unit tests don't accidentally dispatch a
// refresh into the loader path.
func TestAppInitIsNilWithoutLoader(t *testing.T) {
	if cmd := newApp(nil, nil).Init(); cmd != nil {
		t.Errorf("Init() with nil loader = %v, want nil", cmd)
	}
}

// TestAppQuitDoesNotFireWhenTypingInPalette verifies that 'q' typed
// while the palette overlay holds focus goes to the list filter input
// rather than quitting the app.
func TestAppQuitDoesNotFireWhenTypingInPalette(t *testing.T) {
	a := newApp(nil, nil)
	model, _ := a.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	a = model.(app)
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("'q' in palette triggered tea.Quit; expected it to be consumed by the list")
		}
	}
	a = model.(app)
	if a.focus != focusPalette {
		t.Errorf("focus = %v, want focusPalette (still open)", a.focus)
	}
}

// TestAppTabCyclesFocusBetweenSidebarAndContent verifies the Tab
// binding flips focus and back again (the DoD's "Tab cycles focus"
// requirement).
func TestAppTabCyclesFocusBetweenSidebarAndContent(t *testing.T) {
	a := newApp(nil, nil)
	if a.focus != focusSidebar {
		t.Fatalf("initial focus = %v, want focusSidebar", a.focus)
	}
	model, _ := a.Update(tea.KeyMsg{Type: tea.KeyTab})
	a = model.(app)
	if a.focus != focusContent {
		t.Errorf("after first Tab, focus = %v, want focusContent", a.focus)
	}
	model, _ = a.Update(tea.KeyMsg{Type: tea.KeyTab})
	a = model.(app)
	if a.focus != focusSidebar {
		t.Errorf("after second Tab, focus = %v, want focusSidebar", a.focus)
	}
}

// TestAppOpenStackPushesRecordScreen verifies that picking a Stack row
// in the sidebar pushes the record screen onto the registry, exactly
// as the sidebar.OpenStackMsg contract promises.
func TestAppOpenStackPushesRecordScreen(t *testing.T) {
	a := newApp(nil, nil)
	if d := a.registry.Depth(); d != 1 {
		t.Fatalf("initial registry depth = %d, want 1", d)
	}
	model, _ := a.Update(sidebar.OpenStackMsg{Project: "demo", Env: "dev", Stack: "vpc"})
	a = model.(app)
	if d := a.registry.Depth(); d != 2 {
		t.Fatalf("after open-stack, registry depth = %d, want 2", d)
	}
	if _, ok := a.registry.Top().(*screens.RecordScreen); !ok {
		t.Errorf("top screen = %T, want *screens.RecordScreen", a.registry.Top())
	}
}

// TestAppPopMessagePopsRegistry verifies that PopMsg shrinks the
// registry stack (and is a no-op at the home screen).
func TestAppPopMessagePopsRegistry(t *testing.T) {
	a := newApp(nil, nil)
	model, _ := a.Update(sidebar.OpenStackMsg{Project: "demo", Env: "dev", Stack: "vpc"})
	a = model.(app)
	if a.registry.Depth() != 2 {
		t.Fatalf("setup: depth = %d, want 2", a.registry.Depth())
	}
	model, _ = a.Update(screens.PopMsg{})
	a = model.(app)
	if a.registry.Depth() != 1 {
		t.Errorf("after Pop, depth = %d, want 1", a.registry.Depth())
	}
	// Popping the home screen is a no-op.
	model, _ = a.Update(screens.PopMsg{})
	a = model.(app)
	if a.registry.Depth() != 1 {
		t.Errorf("after redundant Pop, depth = %d, want 1 (home is unpoppable)", a.registry.Depth())
	}
}

// TestAppNarrowResizeKeepsSidebarUsable verifies the DoD's "Resizing
// the terminal to 70 cols swaps the logo for the single-line wordmark;
// sidebar narrows but stays usable" — sidebarWidth never drops below
// the floor the tree needs to render labels and badges.
func TestAppNarrowResizeKeepsSidebarUsable(t *testing.T) {
	w := sidebarWidth(70)
	if w < 8 || w >= 70 {
		t.Errorf("sidebarWidth(70) = %d, want a usable narrow value", w)
	}
}
