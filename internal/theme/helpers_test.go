package theme

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Tests pin Lipgloss to truecolor so palette hex values appear verbatim in the
// emitted escape sequences regardless of what the test host's TERM happens to
// be (CI runners often report a 256-colour profile that would otherwise
// down-sample the hex into the nearest palette index).
//
// termenv is already a transitive dependency of lipgloss and is the only way
// to address lipgloss's color-profile API: lipgloss.SetColorProfile takes a
// termenv.Profile and there is no profile-typed wrapper on the lipgloss side.
// The import here is therefore unavoidable for the escape-sequence smoke test
// required by the PR-07 acceptance criteria, and it is confined to _test.go
// files so termenv is never linked into the shipping binary by this package.

func lipglossColorProfile() termenv.Profile { return lipgloss.ColorProfile() }

func setLipglossTruecolor() { lipgloss.SetColorProfile(termenv.TrueColor) }

func restoreLipglossColorProfile(p termenv.Profile) { lipgloss.SetColorProfile(p) }

// truecolorFG returns the SGR fragment Lipgloss emits to set the foreground to
// the given six-digit hex colour, e.g. "#7EE787" → "38;2;126;231;135".
func truecolorFG(hex string) string {
	r, g, b := hexToRGB(hex)
	return fmt.Sprintf("38;2;%d;%d;%d", r, g, b)
}

func hexToRGB(hex string) (r, g, b int) {
	hex = strings.TrimPrefix(hex, "#")
	if _, err := fmt.Sscanf(hex, "%02x%02x%02x", &r, &g, &b); err != nil {
		panic(fmt.Errorf("hexToRGB(%q): %w", hex, err))
	}
	return
}
