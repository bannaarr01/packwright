package gui

import (
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/internal/record"
	"github.com/bannaarr01/packwright/internal/theme"
	"github.com/bannaarr01/packwright/internal/workspace"
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
		{Slash: "/restart-api", Title: "Restart API", Source: "user", Scope: string(pack.ScopeUser)},
		{Slash: "/alb", Title: "ALB (acme)", Source: "acme", Scope: string(pack.ScopePack)},
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

func TestListProjectsMapsWorkspaceToDTO(t *testing.T) {
	orig := loadProjects
	t.Cleanup(func() { loadProjects = orig })
	loadProjects = func() ([]workspace.Project, []error, error) {
		return []workspace.Project{
			{
				Slug: "acme",
				Name: "Acme",
				Envs: []workspace.Env{
					{Slug: "dev", Name: "Development"},
					{Slug: "prd", Name: "Production"},
				},
			},
			{
				Slug: "beta",
				Name: "Beta Co",
				Envs: nil, // no envs yet
			},
		}, nil, nil
	}

	got := newTestApp().ListProjects()
	want := []Project{
		{
			Slug: "acme",
			Name: "Acme",
			Envs: []Env{
				{Slug: "dev", Name: "Development"},
				{Slug: "prd", Name: "Production"},
			},
		},
		{Slug: "beta", Name: "Beta Co", Envs: []Env{}},
	}
	if len(got) != len(want) {
		t.Fatalf("ListProjects() len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Slug != w.Slug || got[i].Name != w.Name {
			t.Errorf("ListProjects()[%d] head = %+v, want %+v", i, got[i], w)
		}
		if len(got[i].Envs) != len(w.Envs) {
			t.Errorf("ListProjects()[%d] envs len = %d, want %d", i, len(got[i].Envs), len(w.Envs))
			continue
		}
		for j, ew := range w.Envs {
			if got[i].Envs[j] != ew {
				t.Errorf("ListProjects()[%d].Envs[%d] = %+v, want %+v", i, j, got[i].Envs[j], ew)
			}
		}
	}
}

func TestListProjectsReturnsEmptySliceOnError(t *testing.T) {
	// Frontend treats ListProjects() as "never null"; the contract is an
	// empty slice on failure so the empty-state UI renders rather than the
	// RPC throwing.
	orig := loadProjects
	t.Cleanup(func() { loadProjects = orig })
	loadProjects = func() ([]workspace.Project, []error, error) {
		return nil, nil, errors.New("disk on fire")
	}

	got := newTestApp().ListProjects()
	if got == nil {
		t.Fatalf("ListProjects() = nil, want empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("ListProjects() = %+v, want empty slice", got)
	}
}

func TestListProjectsReturnsHealthyProjectsAlongsideWarnings(t *testing.T) {
	// Mirrors ListSlashCommands's partial-load contract: when LoadAll
	// surfaces non-empty warnings, the healthy projects must still be
	// returned so a single malformed env.yaml does not blank the sidebar.
	orig := loadProjects
	t.Cleanup(func() { loadProjects = orig })
	loadProjects = func() ([]workspace.Project, []error, error) {
		return []workspace.Project{
				{Slug: "acme", Name: "Acme"},
			}, []error{
				errors.New("workspace: project \"broken\": malformed env.yaml"),
			}, nil
	}

	got := newTestApp().ListProjects()
	if len(got) != 1 || got[0].Slug != "acme" {
		t.Fatalf("ListProjects() = %+v, want one acme row", got)
	}
}

func TestListStacksMapsRecordToRow(t *testing.T) {
	deployed := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 6, 17, 11, 30, 0, 0, time.UTC)

	orig := loadStacks
	t.Cleanup(func() { loadStacks = orig })
	loadStacks = func(project, env string) ([]*record.StackRecord, error) {
		if project != "acme" || env != "dev" {
			t.Errorf("loadStacks called with (%q, %q), want (acme, dev)", project, env)
		}
		return []*record.StackRecord{
			{
				StackName:     "alb-dev-stack",
				Manifest:      record.ManifestRef{Slash: "/alb", Source: "packs/reference/manifests/alb.yaml"},
				Status:        record.Status{Broad: record.BroadDeployed},
				DeployedAt:    deployed,
				LastUpdatedAt: updated,
			},
			{
				StackName: "draft-stack",
				Manifest:  record.ManifestRef{Slash: "/draft"},
				Status:    record.Status{Broad: record.BroadDraft},
				// Both timestamps zero — formatStackTime returns "".
			},
		}, nil
	}

	got := newTestApp().ListStacks("acme", "dev")
	want := []StackRow{
		{
			Name:      "alb-dev-stack",
			Slash:     "/alb",
			Broad:     "deployed",
			UpdatedAt: updated.Format(time.RFC3339),
		},
		{
			Name:      "draft-stack",
			Slash:     "/draft",
			Broad:     "draft",
			UpdatedAt: "",
		},
	}
	if len(got) != len(want) {
		t.Fatalf("ListStacks() len = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("ListStacks()[%d] = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestListStacksRendersAllBroadStatuses(t *testing.T) {
	// The badge mapping in PR-10's StatusBadge.svelte is driven by the
	// broad-status string. Drop any value here and the frontend will fall
	// back to its default (deleted) badge — so the contract is "every
	// BroadStatus in record.go round-trips as its string value".
	cases := []record.BroadStatus{
		record.BroadDraft,
		record.BroadDeploying,
		record.BroadDeployed,
		record.BroadPartial,
		record.BroadFailed,
		record.BroadDrifted,
		record.BroadDeleted,
	}

	orig := loadStacks
	t.Cleanup(func() { loadStacks = orig })
	loadStacks = func(string, string) ([]*record.StackRecord, error) {
		recs := make([]*record.StackRecord, 0, len(cases))
		for i, b := range cases {
			recs = append(recs, &record.StackRecord{
				StackName: "s-" + string(b),
				Manifest:  record.ManifestRef{Slash: "/" + string(b)},
				Status:    record.Status{Broad: b},
				// Vary timestamps so we don't accidentally compare zero
				// across all rows.
				DeployedAt: time.Unix(int64(i*60), 0).UTC(),
			})
		}
		return recs, nil
	}

	got := newTestApp().ListStacks("acme", "dev")
	if len(got) != len(cases) {
		t.Fatalf("ListStacks() len = %d, want %d", len(got), len(cases))
	}
	for i, b := range cases {
		if got[i].Broad != string(b) {
			t.Errorf("row[%d].Broad = %q, want %q", i, got[i].Broad, string(b))
		}
	}
}

func TestListStacksReturnsEmptySliceOnError(t *testing.T) {
	orig := loadStacks
	t.Cleanup(func() { loadStacks = orig })
	loadStacks = func(string, string) ([]*record.StackRecord, error) {
		return nil, errors.New("disk on fire")
	}

	got := newTestApp().ListStacks("acme", "dev")
	if got == nil || len(got) != 0 {
		t.Fatalf("ListStacks() = %+v, want empty slice", got)
	}
}

func TestListStacksUsesIndependentTreeForEmptyProject(t *testing.T) {
	orig := loadStacks
	t.Cleanup(func() { loadStacks = orig })
	var sawProject, sawEnv string
	loadStacks = func(project, env string) ([]*record.StackRecord, error) {
		sawProject, sawEnv = project, env
		return nil, nil
	}
	newTestApp().ListStacks("", "")
	if sawProject != "" || sawEnv != "" {
		t.Errorf("loadStacks args = (%q, %q), want both empty (independent tree)", sawProject, sawEnv)
	}
}

func TestFormatStackTimePrefersLastUpdated(t *testing.T) {
	deployed := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)

	if got := formatStackTime(updated, deployed); got != updated.Format(time.RFC3339) {
		t.Errorf("formatStackTime(updated, deployed) = %q, want %q", got, updated.Format(time.RFC3339))
	}
	if got := formatStackTime(time.Time{}, deployed); got != deployed.Format(time.RFC3339) {
		t.Errorf("formatStackTime(zero, deployed) = %q, want %q", got, deployed.Format(time.RFC3339))
	}
	if got := formatStackTime(time.Time{}, time.Time{}); got != "" {
		t.Errorf("formatStackTime(zero, zero) = %q, want empty", got)
	}
}
