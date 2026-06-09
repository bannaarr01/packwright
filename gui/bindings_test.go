package gui

import (
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/internal/theme"
	"github.com/bannaarr01/packwright/pack"
)

// newTestApp builds an App with a discarding logger so tests do not noise up
// the test output.
func newTestApp() *App {
	return newApp(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1})))
}

func TestProfileReadsEnvOrFallsBack(t *testing.T) {
	t.Setenv("AWS_PROFILE", "")
	app := newTestApp()
	if got := app.Profile(); got != "default" {
		t.Errorf("Profile() with empty AWS_PROFILE = %q, want %q", got, "default")
	}

	t.Setenv("AWS_PROFILE", "ops")
	if got := app.Profile(); got != "ops" {
		t.Errorf("Profile() with AWS_PROFILE=ops = %q, want %q", got, "ops")
	}
}

func TestRegionReadsEitherEnvOrFallsBack(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")
	app := newTestApp()
	if got := app.Region(); got != "-" {
		t.Errorf("Region() with both env unset = %q, want %q", got, "-")
	}

	t.Setenv("AWS_DEFAULT_REGION", "eu-west-1")
	if got := app.Region(); got != "eu-west-1" {
		t.Errorf("Region() with only AWS_DEFAULT_REGION = %q, want %q", got, "eu-west-1")
	}

	// AWS_REGION wins when both are set.
	t.Setenv("AWS_REGION", "us-east-2")
	if got := app.Region(); got != "us-east-2" {
		t.Errorf("Region() with AWS_REGION set = %q, want %q", got, "us-east-2")
	}
}

func TestAccountIsPlaceholderUntilWired(t *testing.T) {
	if got := newTestApp().Account(); got != "-" {
		t.Errorf("Account() = %q, want %q (MVP-1 placeholder)", got, "-")
	}
}

func TestListSlashCommandsReadsFromLoadPalette(t *testing.T) {
	// Stub the package-level seam so the test is hermetic — no real config
	// home, no filesystem discovery.
	orig := loadPalette
	t.Cleanup(func() { loadPalette = orig })
	loadPalette = func() ([]pack.PaletteEntry, error) {
		return []pack.PaletteEntry{
			{Slash: "/restart-api", Title: "Restart API", Source: "user", Scope: pack.ScopeUser},
			{Slash: "/alb", Title: "ALB (acme)", Source: "acme", Scope: pack.ScopePack},
		}, nil
	}

	got := newTestApp().ListSlashCommands()
	want := []SlashCommand{
		{Slash: "/restart-api", Title: "Restart API"},
		{Slash: "/alb", Title: "ALB (acme)"},
	}
	if len(got) != len(want) {
		t.Fatalf("ListSlashCommands() len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("ListSlashCommands()[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestListSlashCommandsToleratesPartialLoad(t *testing.T) {
	// LoadPalette returns non-nil rows alongside a non-nil error when only
	// some packs failed; the GUI must still render the healthy rows rather
	// than failing the RPC.
	orig := loadPalette
	t.Cleanup(func() { loadPalette = orig })
	loadPalette = func() ([]pack.PaletteEntry, error) {
		return []pack.PaletteEntry{
			{Slash: "/new-command", Title: "New command", Source: "builtin", Scope: pack.ScopeUser},
		}, errors.New("one pack failed to parse")
	}

	got := newTestApp().ListSlashCommands()
	if len(got) != 1 || got[0].Slash != "/new-command" {
		t.Fatalf("ListSlashCommands() = %+v, want one /new-command row", got)
	}
}

func TestThemeResolvesAndValidates(t *testing.T) {
	t.Setenv("PACKWRIGHT_THEME", "dark")
	t.Setenv("COLORFGBG", "")
	app := newTestApp()
	got, err := app.Theme()
	if err != nil {
		t.Fatalf("Theme() error = %v, want nil", err)
	}
	if got.Mode != string(theme.ModeDark) {
		t.Errorf("Theme().Mode with PACKWRIGHT_THEME=dark = %q, want %q", got.Mode, theme.ModeDark)
	}
	// Sanity check that the embedded tokens were loaded — bg is a six-digit
	// hex string per tokens/schema.json.
	if !strings.HasPrefix(got.Tokens.BG, "#") || len(got.Tokens.BG) != 7 {
		t.Errorf("Theme().Tokens.BG = %q, want a #RRGGBB string", got.Tokens.BG)
	}
}

func TestThemeRespectsLightOverride(t *testing.T) {
	t.Setenv("PACKWRIGHT_THEME", "light")
	t.Setenv("COLORFGBG", "")
	got, err := newTestApp().Theme()
	if err != nil {
		t.Fatalf("Theme() error = %v, want nil", err)
	}
	if got.Mode != string(theme.ModeLight) {
		t.Errorf("Theme().Mode with PACKWRIGHT_THEME=light = %q, want %q", got.Mode, theme.ModeLight)
	}
}
