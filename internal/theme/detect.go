package theme

import (
	"strconv"
	"strings"
)

// Inputs are the raw values Resolve uses to pick a theme. They are passed in
// explicitly — Resolve does no I/O of its own — so the function is trivial to
// test and so this package does not depend on internal/config.
//
// Empty strings mean "not set"; Config's zero value ("") is also treated as
// not set.
type Inputs struct {
	// Env is the raw value of $PACKWRIGHT_THEME (or "" if unset). It wins over
	// Config when it parses to a valid Mode.
	Env string

	// Config is the theme value read from the user's config file. ModeDark or
	// ModeLight pin the result; ModeAuto (or the zero value) defers to the
	// COLORFGBG heuristic.
	Config Mode

	// COLORFGBG is the raw value of the terminal's COLORFGBG environment
	// variable (or "" if unset). The format is "fg;bg" or "fg;sep;bg". Only
	// the bg field is consulted.
	COLORFGBG string
}

// Resolve maps Inputs to a concrete dark or light mode. It is pure and never
// returns ModeAuto.
//
// Precedence (highest first), per ADR-0011:
//  1. $PACKWRIGHT_THEME if it parses to dark or light.
//  2. Config if it is dark or light.
//  3. The COLORFGBG heuristic when Config is auto (or unset) or when
//     $PACKWRIGHT_THEME=auto.
//  4. Dark — the Claude Code default.
//
// An Env value of "auto" is treated as a request to run the heuristic; an
// unrecognised Env value is ignored and we fall through to Config.
func Resolve(in Inputs) Mode {
	if m, ok := ParseMode(in.Env); ok {
		if m.IsConcrete() {
			return m
		}
		// Env explicitly asked for auto: skip Config and run the heuristic.
		return detectFromCOLORFGBG(in.COLORFGBG)
	}
	if in.Config.IsConcrete() {
		return in.Config
	}
	return detectFromCOLORFGBG(in.COLORFGBG)
}

// detectFromCOLORFGBG inspects the bg field of a COLORFGBG-style value and
// returns ModeLight when the terminal background is light, ModeDark otherwise
// (including the unset / unparseable cases — see ADR-0011 for the rationale:
// dark is the Claude Code default).
//
// COLORFGBG is "fg;bg" or "fg;sep;bg" (rxvt's tri-field form, where the middle
// field is the cursor colour). The bg field can be an ANSI index (0–15) or
// the literal string "default":
//
//   - numeric bg >= 9 → light (bright background)
//   - bg == "default" → light (most terminals that report "default" are
//     light-background; users on dark terminals usually set COLORFGBG
//     explicitly or override via $PACKWRIGHT_THEME)
//   - anything else, including missing or malformed → dark
func detectFromCOLORFGBG(raw string) Mode {
	if raw == "" {
		return ModeDark
	}
	parts := strings.Split(raw, ";")
	if len(parts) < 2 {
		return ModeDark
	}
	bg := strings.TrimSpace(parts[len(parts)-1])
	if bg == "" {
		return ModeDark
	}
	if strings.EqualFold(bg, "default") {
		return ModeLight
	}
	n, err := strconv.Atoi(bg)
	if err != nil {
		return ModeDark
	}
	if n >= 9 {
		return ModeLight
	}
	return ModeDark
}
