package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// TestAppCtrlPOpensPalette verifies Ctrl+P transitions the root model from
// launcher to palette mode.
func TestAppCtrlPOpensPalette(t *testing.T) {
	a := newApp(nil, nil)
	if a.mode != modeLauncher {
		t.Fatalf("initial mode = %v, want modeLauncher", a.mode)
	}
	model, _ := a.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	a = model.(app)
	if a.mode != modePalette {
		t.Errorf("after ctrl+p, mode = %v, want modePalette", a.mode)
	}
}

// TestAppQuitFromLauncher verifies 'q' in launcher mode returns tea.Quit.
func TestAppQuitFromLauncher(t *testing.T) {
	a := newApp(nil, nil)
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("'q' in launcher returned nil cmd, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("cmd produced %T, want tea.QuitMsg", cmd())
	}
}

// TestAppCtrlCAlwaysQuits verifies Ctrl+C quits even while the palette is
// open — the escape hatch must never be swallowed by a sub-model.
func TestAppCtrlCAlwaysQuits(t *testing.T) {
	a := newApp(nil, nil)
	model, _ := a.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	a = model.(app)
	if a.mode != modePalette {
		t.Fatal("setup failed: palette did not open")
	}
	_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c in palette returned nil cmd, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("cmd produced %T, want tea.QuitMsg", cmd())
	}
}

// TestAppPaletteSelectionReturnsToLauncher verifies that consuming a
// paletteSelectedMsg switches the mode back to launcher.
func TestAppPaletteSelectionReturnsToLauncher(t *testing.T) {
	a := newApp(nil, nil)
	model, _ := a.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	a = model.(app)
	model, _ = a.Update(paletteSelectedMsg{Slash: "/x", Title: "x"})
	a = model.(app)
	if a.mode != modeLauncher {
		t.Errorf("after selection, mode = %v, want modeLauncher", a.mode)
	}
}

// TestAppClosePaletteMessageReturnsToLauncher verifies that closePaletteMsg
// (emitted by the palette on Esc) returns to the launcher.
func TestAppClosePaletteMessageReturnsToLauncher(t *testing.T) {
	a := newApp(nil, nil)
	model, _ := a.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	a = model.(app)
	model, _ = a.Update(closePaletteMsg{})
	a = model.(app)
	if a.mode != modeLauncher {
		t.Errorf("after close, mode = %v, want modeLauncher", a.mode)
	}
}

// TestAppRefreshPaletteMsgSetsItemsFromLoader verifies that a
// refreshPaletteMsg invokes the configured loader and the resulting rows
// land in the palette's list. This is the message the manifest-watcher
// goroutine sends on every debounced change.
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
// model's Init returns a command that produces a refreshPaletteMsg, so the
// palette is populated on the first frame without the user opening it.
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

// TestAppInitIsNilWithoutLoader verifies that the test-only nil-loader
// posture still works — Init returns nil so unit tests don't accidentally
// dispatch a refresh into the loader path.
func TestAppInitIsNilWithoutLoader(t *testing.T) {
	if cmd := newApp(nil, nil).Init(); cmd != nil {
		t.Errorf("Init() with nil loader = %v, want nil", cmd)
	}
}

// TestAppQuitDoesNotFireWhenTypingInPalette verifies that 'q' typed inside
// the palette goes to the list (filter input) rather than quitting the app.
func TestAppQuitDoesNotFireWhenTypingInPalette(t *testing.T) {
	a := newApp(nil, nil)
	model, _ := a.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	a = model.(app)
	model, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	a = model.(app)
	if cmd != nil {
		if _, ok := cmd().(tea.QuitMsg); ok {
			t.Fatal("'q' in palette triggered tea.Quit; expected it to be consumed by the list")
		}
	}
	if a.mode != modePalette {
		t.Errorf("mode = %v, want modePalette (still open)", a.mode)
	}
}
