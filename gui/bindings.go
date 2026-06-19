// Wails events emitted by this package:
//
//   - packwright:palette-changed   — manifests under pack/command/monitor
//     roots changed; the palette and the "By pack" sidebar grouping refetch.
//   - packwright:workspace-changed — anything under <home>/projects/ changed
//     (a new project.yaml, env.yaml, or stack record JSON). The "Projects"
//     sidebar grouping refetches ListProjects + ListStacks.
//
// Both are pure "something changed, take another look" signals with no
// payload — the frontend already owns the data-fetching path.
package gui

import (
	"fmt"
	"os"
	"time"

	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/record"
	"github.com/bannaarr01/packwright/internal/theme"
	"github.com/bannaarr01/packwright/internal/workspace"
	"github.com/bannaarr01/packwright/pack"
)

// SlashCommand is one entry returned by ListSlashCommands. The shape mirrors
// the TUI's paletteItem so future pack-registry routing can swap both
// front-ends to the real source in one change. Source / Scope / Pinned are
// the same fields pack.PaletteEntry carries; the sidebar groups rows by
// them, so dropping any of these would force the frontend to re-derive
// information the Go side already knows.
type SlashCommand struct {
	Slash  string `json:"slash"`
	Title  string `json:"title"`
	Source string `json:"source"`
	Scope  string `json:"scope"`
	Pinned bool   `json:"pinned"`
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

// Profile returns the AWS profile the user is using: the persisted config.yaml
// profile wins, then $AWS_PROFILE, then "default". See currentProfileName.
func (a *App) Profile() string { return currentProfileName() }

// Region returns the AWS region the user is in: the persisted config.yaml
// region wins, then $AWS_REGION, then $AWS_DEFAULT_REGION, then "-". See
// currentRegionName.
func (a *App) Region() string { return currentRegionName() }

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
		out = append(out, SlashCommand{
			Slash:  e.Slash,
			Title:  e.Title,
			Source: e.Source,
			Scope:  string(e.Scope),
			Pinned: e.Pinned,
		})
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

// Project is the sidebar-shape mirror of workspace.Project. The DTO is
// trimmed to the fields the Projects-grouping sidebar renders so the JSON
// payload stays small and the frontend is not coupled to internal/workspace
// shape changes. Profile / Region / Description are intentionally omitted —
// the active-project chip in the footer reads those via its own binding.
type Project struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	Envs []Env  `json:"envs"`
}

// Env is the sidebar-shape mirror of workspace.Env.
type Env struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// StackRow is the sidebar row shape for one stack record. Only the columns
// the sidebar renders are present: the broad status drives the badge, the
// slash command is the row label, and UpdatedAt is the muted timestamp
// shown after the name. Resources / Outputs / Parameters are deliberately
// omitted — opening a stack detail screen will use a separate binding.
type StackRow struct {
	Name      string `json:"name"`
	Slash     string `json:"slash"`
	Broad     string `json:"broad"`
	UpdatedAt string `json:"updated_at"`
}

// loadProjects is the package-level seam for ListProjects. Tests stub this
// to inject fake project trees without writing to disk. The second return
// value carries the partial-load warnings that workspace.LoadAll
// accumulates per project — preserved through the seam so ListProjects can
// log them, mirroring how loadPalette / ListSlashCommands surface palette
// load failures.
var loadProjects = func() ([]workspace.Project, []error, error) {
	home, err := config.Home()
	if err != nil {
		return nil, nil, err
	}
	projects, warnings := workspace.LoadAll(home)
	return projects, warnings, nil
}

// loadStacks is the package-level seam for ListStacks. Tests stub this to
// return fixture StackRecord slices. The default implementation reads from
// <home>/projects/<project>/<env>/stacks/.
var loadStacks = func(project, env string) ([]*record.StackRecord, error) {
	home, err := config.Home()
	if err != nil {
		return nil, err
	}
	return record.NewStore(home).List(project, env)
}

// ListProjects returns the workspace tree (ADR-0045) shaped for the
// sidebar's Projects grouping. The frontend re-invokes it whenever the
// packwright:workspace-changed event fires, so creating a project surfaces
// in the sidebar within one event cycle. A read failure (missing
// projects/, malformed project.yaml) is logged but returns an empty slice
// — the sidebar shows the empty-state copy rather than failing the RPC.
func (a *App) ListProjects() []Project {
	projects, warnings, err := loadProjects()
	if err != nil {
		a.logger.Warn("gui: workspace: load projects", "err", err)
		return []Project{}
	}
	for _, w := range warnings {
		a.logger.Warn("gui: workspace: partial load", "err", w)
	}
	out := make([]Project, 0, len(projects))
	for _, p := range projects {
		envs := make([]Env, 0, len(p.Envs))
		for _, e := range p.Envs {
			envs = append(envs, Env{Slug: e.Slug, Name: e.Name})
		}
		out = append(out, Project{Slug: p.Slug, Name: p.Name, Envs: envs})
	}
	a.logger.Info("gui: workspace: list projects", "projects", len(out))
	return out
}

// ListStacks returns the stack records persisted under (project, env) per
// ADR-0046, mapped to the StackRow shape the sidebar renders. Project and
// env must both be non-empty; for independent stacks the frontend uses
// ListStacks("", "") which routes to the independent tree inside
// record.Store. A read failure returns an empty slice (logged) so a single
// malformed record never blanks the sidebar.
func (a *App) ListStacks(project, env string) []StackRow {
	recs, err := loadStacks(project, env)
	if err != nil {
		a.logger.Warn("gui: workspace: load stacks",
			"project", project,
			"env", env,
			"err", err)
		return []StackRow{}
	}
	out := make([]StackRow, 0, len(recs))
	for _, r := range recs {
		out = append(out, StackRow{
			Name:      r.StackName,
			Slash:     r.Manifest.Slash,
			Broad:     string(r.Status.Broad),
			UpdatedAt: formatStackTime(r.LastUpdatedAt, r.DeployedAt),
		})
	}
	return out
}

// formatStackTime returns an RFC3339 string for the most relevant
// timestamp on a record, or the empty string when neither is set —
// drafts mostly. The empty string is the contract the StackRow component
// branches on to skip the muted suffix.
func formatStackTime(lastUpdated, deployed time.Time) string {
	if !lastUpdated.IsZero() {
		return lastUpdated.UTC().Format(time.RFC3339)
	}
	if !deployed.IsZero() {
		return deployed.UTC().Format(time.RFC3339)
	}
	return ""
}
