package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

// TestPaletteEnterEmitsSelection verifies Enter on the first item produces a
// paletteSelectedMsg carrying that item's slash + title.
func TestPaletteEnterEmitsSelection(t *testing.T) {
	items := []list.Item{
		paletteItem{slash: "/a/one", title: "one"},
		paletteItem{slash: "/b/two", title: "two"},
	}
	p := newPaletteWithItems(DefaultKeyMap(), items)
	p.SetSize(80, 20)

	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Update(KeyEnter) returned nil cmd, want a paletteSelectedMsg")
	}
	sel, ok := cmd().(paletteSelectedMsg)
	if !ok {
		t.Fatalf("got %T, want paletteSelectedMsg", cmd())
	}
	if sel.Slash != "/a/one" || sel.Title != "one" {
		t.Errorf("selection = %+v, want {Slash:/a/one Title:one}", sel)
	}
}

// TestPaletteEscEmitsCloseWhenUnfiltered verifies Esc with no active filter
// produces a closePaletteMsg.
func TestPaletteEscEmitsCloseWhenUnfiltered(t *testing.T) {
	p := newPaletteWithItems(DefaultKeyMap(), nil)
	p.SetSize(80, 20)
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("Update(KeyEsc) returned nil cmd, want a closePaletteMsg")
	}
	if _, ok := cmd().(closePaletteMsg); !ok {
		t.Fatalf("got %T, want closePaletteMsg", cmd())
	}
}

// TestPaletteFiltersByQuery verifies that pressing the list's filter key ('/')
// activates the filter, after which typing characters narrows the items.
func TestPaletteFiltersByQuery(t *testing.T) {
	items := []list.Item{
		paletteItem{slash: "/alpha", title: "alpha"},
		paletteItem{slash: "/beta", title: "beta"},
		paletteItem{slash: "/gamma", title: "gamma"},
	}
	p := newPaletteWithItems(DefaultKeyMap(), items)
	p.SetSize(80, 20)

	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if p.list.FilterState() != list.Filtering {
		t.Fatalf("FilterState = %v after '/', want Filtering", p.list.FilterState())
	}
	for _, r := range "alp" {
		p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if p.list.FilterValue() != "alp" {
		t.Errorf("FilterValue = %q, want %q", p.list.FilterValue(), "alp")
	}
}
