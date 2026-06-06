package gui

import (
	"fmt"
	"os"

	"github.com/bannaarr01/packwright/internal/theme"
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

// placeholderSlashCommands is the seed set used until the pack registry is
// wired into the front-ends. It matches the TUI's tui.placeholderItems
// verbatim so the two front-ends behave identically in MVP-1.
var placeholderSlashCommands = []SlashCommand{
	{Slash: "/example/hello", Title: "Example: hello"},
	{Slash: "/example/world", Title: "Example: world"},
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

// ListSlashCommands returns the palette's data set. In MVP-1 this is two
// hardcoded items matching the TUI; once the pack registry is wired into
// the front-ends this method will read from it instead.
func (a *App) ListSlashCommands() []SlashCommand {
	out := make([]SlashCommand, len(placeholderSlashCommands))
	copy(out, placeholderSlashCommands)
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
