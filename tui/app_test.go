package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestAppCtrlPOpensPalette verifies Ctrl+P transitions the root model from
// launcher to palette mode.
func TestAppCtrlPOpensPalette(t *testing.T) {
	a := newApp(nil)
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
	a := newApp(nil)
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
	a := newApp(nil)
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
	a := newApp(nil)
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
	a := newApp(nil)
	model, _ := a.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	a = model.(app)
	model, _ = a.Update(closePaletteMsg{})
	a = model.(app)
	if a.mode != modeLauncher {
		t.Errorf("after close, mode = %v, want modeLauncher", a.mode)
	}
}

// TestAppQuitDoesNotFireWhenTypingInPalette verifies that 'q' typed inside
// the palette goes to the list (filter input) rather than quitting the app.
func TestAppQuitDoesNotFireWhenTypingInPalette(t *testing.T) {
	a := newApp(nil)
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
