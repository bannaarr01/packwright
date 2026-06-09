package gui

import (
	"fmt"
	"os"

	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/theme"
	"github.com/bannaarr01/packwright/pack"
)

// SlashCommand is one entry returned by ListSlashCommands. The shape mirrors
// the TUI's paletteItem so future pack-registry routing can swap both
// front-ends to the real source in one change.
type SlashCommand struct {
	Slash string `json:"slash"`
	Title string `json:"title"`
}

// ThemePayload is the Theme binding's return shape. Tokens carries the same
// validated palette the TUI consumes via internal/theme; Mode is the
// resolved concrete mode ("dark" or "light") so the frontend can decide
// whether to apply Tailwind's `class="dark"` on <html>.
type ThemePayload struct {
	Mode   string       `json:"mode"`
	Tokens theme.Tokens `json:"tokens"`
}

// loadPalette is a package-level seam so tests can stub palette discovery
// without touching the real filesystem. Production code keeps the default
// (resolve config.Home → pack.LoadPalette); tests assign their own closure
// in the test setup.
var loadPalette = func() ([]pack.PaletteEntry, error) {
	home, err := config.Home()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load()
	if err != nil {
		// A malformed config.yaml must not prevent the palette from rendering
		// the discoverable rows; downgrade to an empty config so LoadPalette
		// still sees the home directory layout.
		cfg = &config.Config{}
	}
	return pack.LoadPalette(home, cfg.PinnedDefaults)
}

// Profile returns the AWS profile the user appears to be using. For MVP-1
// this reads $AWS_PROFILE with a "default" fallback; a future PR will wire
// it to the config package.
func (a *App) Profile() string {
	if v := os.Getenv("AWS_PROFILE"); v != "" {
		return v
	}
	return "default"
}

// Region returns the AWS region the user appears to be in. For MVP-1 this
// reads $AWS_REGION (then $AWS_DEFAULT_REGION) with a "-" fallback so the
// header always has something to render.
func (a *App) Region() string {
	if v := os.Getenv("AWS_REGION"); v != "" {
		return v
	}
	if v := os.Getenv("AWS_DEFAULT_REGION"); v != "" {
		return v
	}
	return "-"
}

// Account returns the AWS account id the user appears to be using. For MVP-1
// this is a placeholder; resolving it requires an STS call which lives
// behind the awsx layer and is intentionally not exercised on every window
// open. Returns "-" until wired.
func (a *App) Account() string { return "-" }

// ListSlashCommands returns the palette's data set sourced from the pack
// registry (pack.LoadPalette). The frontend re-invokes it on every palette
// open, so a manifest edit propagates without an explicit reload — the same
// behaviour the TUI achieves via its manifest watcher. A partial load
// (e.g. one malformed pack among many) returns the rows that did parse;
// the error is logged here and not surfaced to the frontend so the palette
// degrades gracefully.
func (a *App) ListSlashCommands() []SlashCommand {
	entries, err := loadPalette()
	if err != nil {
		a.logger.Warn("gui: palette: partial load", "err", err)
	}
	out := make([]SlashCommand, 0, len(entries))
	for _, e := range entries {
		out = append(out, SlashCommand{Slash: e.Slash, Title: e.Title})
	}
	a.logger.Info("gui: palette: list", "rows", len(out))
	return out
}

// Theme returns the current palette plus its resolved concrete mode. The
// frontend uses Mode to set `class="dark"` on <html> and Tokens to drive
// Tailwind variables. Errors from the embedded theme loader are surfaced
// to the frontend as a Wails RPC error.
//
// Resolution follows the same precedence as the TUI (see internal/theme):
// $PACKWRIGHT_THEME wins, then config (not yet wired here), then the
// COLORFGBG heuristic, then dark as the default.
func (a *App) Theme() (ThemePayload, error) {
	mode := theme.Resolve(theme.Inputs{
		Env:       os.Getenv("PACKWRIGHT_THEME"),
		COLORFGBG: os.Getenv("COLORFGBG"),
	})
	tokens, err := theme.Load(mode)
	if err != nil {
		return ThemePayload{}, fmt.Errorf("gui: loading theme tokens: %w", err)
	}
	return ThemePayload{Mode: mode.String(), Tokens: tokens}, nil
}

// SelectSlashCommand is called by the palette when the user picks an item.
// MVP-1 just logs the selection (matching the TUI's paletteSelectedMsg
// behaviour) so the round trip is demoable end-to-end before pack-registry
// routing lands.
func (a *App) SelectSlashCommand(sc SlashCommand) {
	a.logger.Info("palette selection",
		"slash", sc.Slash,
		"title", sc.Title)
}
