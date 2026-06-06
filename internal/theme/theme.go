// Package theme is the single source of truth for Packwright's visual palette.
//
// It exposes three things:
//
//   - A small Mode enum (dark, light, auto) shared by the TUI and the GUI.
//   - Tokens, a semantic palette loaded from one JSON file per mode. The TUI
//     consumes these via the Lipgloss style accessors in this package; the GUI
//     imports the same JSON files directly from its Vite build.
//   - Resolve, a pure function that maps explicit caller inputs (env var,
//     config value, raw COLORFGBG) to a concrete dark or light mode.
//
// Theming policy is described in ADR-0011. This package owns the palette;
// callers own the I/O that produces Resolve's inputs.
package theme

import "fmt"

// Mode is the user-facing theme selector. Configuration files and the
// $PACKWRIGHT_THEME environment variable round-trip through this type.
//
// Resolve always returns a concrete mode (ModeDark or ModeLight); ModeAuto
// only appears as an input to Resolve.
type Mode string

// The three theme modes. Their string values are the canonical spellings used
// in config files, the environment variable, and the /theme slash command.
const (
	ModeDark  Mode = "dark"
	ModeLight Mode = "light"
	ModeAuto  Mode = "auto"
)

// ParseMode parses a Mode from its string form, accepting the canonical
// lowercase spellings only. An empty string is reported as not-a-mode via
// ok=false so callers can distinguish "unset" from "invalid".
func ParseMode(s string) (m Mode, ok bool) {
	switch Mode(s) {
	case ModeDark, ModeLight, ModeAuto:
		return Mode(s), true
	}
	return "", false
}

// String returns the canonical spelling of the mode.
func (m Mode) String() string { return string(m) }

// IsConcrete reports whether m is ModeDark or ModeLight (i.e. not ModeAuto and
// not the zero value). Callers that need a palette must operate on a concrete
// mode.
func (m Mode) IsConcrete() bool { return m == ModeDark || m == ModeLight }

// errUnknownMode is returned by API surfaces that demand a concrete mode when
// given ModeAuto or the zero value.
func errUnknownMode(m Mode) error {
	return fmt.Errorf("theme: %q is not a concrete mode (want %q or %q)", string(m), ModeDark, ModeLight)
}
