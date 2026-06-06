package gui

import (
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/internal/theme"
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

func TestListSlashCommandsMatchesTUIPlaceholders(t *testing.T) {
	got := newTestApp().ListSlashCommands()
	// The two seeded items must match tui/palette.go's placeholderItems
	// exactly — see the parity requirement in the PR-09 plan.
	want := []SlashCommand{
		{Slash: "/example/hello", Title: "Example: hello"},
		{Slash: "/example/world", Title: "Example: world"},
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

func TestListSlashCommandsReturnsCopy(t *testing.T) {
	app := newTestApp()
	a := app.ListSlashCommands()
	a[0].Slash = "/mutated"
	b := app.ListSlashCommands()
	if b[0].Slash == "/mutated" {
		t.Error("ListSlashCommands must return a defensive copy; package-level slice was mutated")
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
