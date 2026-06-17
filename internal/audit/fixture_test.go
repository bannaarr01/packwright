package audit_test

import (
	"path/filepath"
	"testing"

	"github.com/bannaarr01/packwright/pack"
)

// TestAuditPaletteFixtureSurfacesSlash is a smoke test for the
// DoD bullet "packwright tui with PACKWRIGHT_HOME pointing at a test
// fixture shows /audit in the palette". The fixture lives at
// testdata/home/commands/audit.yaml; pack.LoadPalette is the same code
// the TUI runs at startup, so a passing assertion here means a real
// TUI run with PACKWRIGHT_HOME=<fixture-home> will surface the row.
func TestAuditPaletteFixtureSurfacesSlash(t *testing.T) {
	home := filepath.Join("testdata", "home")
	entries, err := pack.LoadPalette(home, nil)
	if err != nil {
		t.Fatalf("LoadPalette(%q): %v", home, err)
	}
	for _, e := range entries {
		if e.Slash == "/audit" {
			return
		}
	}
	t.Fatalf("/audit not found in palette entries: %+v", entries)
}
