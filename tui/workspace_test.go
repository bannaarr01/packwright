package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bannaarr01/packwright/tui/screens"
)

// TestWorkspacePaletteItemsSeeded proves every workspace/template slash gets a
// built-in palette row — without them the commands would be unreachable from
// the palette (LoadPalette only surfaces manifest-backed slashes).
func TestWorkspacePaletteItemsSeeded(t *testing.T) {
	want := map[string]bool{
		slashNewProject:      false,
		slashNewEnv:          false,
		slashSwitchProject:   false,
		slashListProjects:    false,
		slashCopyTemplate:    false,
		slashPromoteTemplate: false,
	}
	for _, it := range workspacePaletteItems() {
		pi, ok := it.(paletteItem)
		if !ok {
			t.Fatalf("palette item %T is not a paletteItem", it)
		}
		if _, tracked := want[pi.slash]; tracked {
			want[pi.slash] = true
		}
	}
	for slash, seen := range want {
		if !seen {
			t.Errorf("workspacePaletteItems missing a row for %s", slash)
		}
	}
}

// TestWorkspaceSlashOpensForm verifies that picking an input-taking workspace
// slash pushes its form and stashes the matching pendingWorkspace, so the
// submission can be routed without consulting any other state.
func TestWorkspaceSlashOpensForm(t *testing.T) {
	a := newApp(nil, nil)
	a.home = t.TempDir()

	model, _ := a.Update(paletteSelectedMsg{Slash: slashNewProject, Title: "New project"})
	a = model.(app)

	if d := a.registry.Depth(); d != 2 {
		t.Fatalf("after picking %s, registry depth = %d, want 2 (form pushed)", slashNewProject, d)
	}
	if _, ok := a.registry.Top().(*screens.FormScreen); !ok {
		t.Errorf("top screen = %T, want *screens.FormScreen", a.registry.Top())
	}
	if a.pendingWorkspace == nil || a.pendingWorkspace.kind != wsNewProject {
		t.Errorf("pendingWorkspace = %+v, want kind wsNewProject", a.pendingWorkspace)
	}
}

// TestWorkspaceListProjectsShowsNotice verifies /list-projects skips the form
// step and renders a read-only notice immediately (it needs no input).
func TestWorkspaceListProjectsShowsNotice(t *testing.T) {
	t.Setenv("PACKWRIGHT_HOME", t.TempDir())

	a := newApp(nil, nil)
	model, _ := a.Update(paletteSelectedMsg{Slash: slashListProjects, Title: "List projects"})
	a = model.(app)

	if d := a.registry.Depth(); d != 2 {
		t.Fatalf("after /list-projects, registry depth = %d, want 2 (notice pushed)", d)
	}
	if _, ok := a.registry.Top().(*noticeScreen); !ok {
		t.Errorf("top screen = %T, want *noticeScreen", a.registry.Top())
	}
	if a.pendingWorkspace != nil {
		t.Error("pendingWorkspace set for /list-projects; want nil (no form step)")
	}
}

// TestFinishWorkspaceNewProjectCreates drives the full palette → form → submit
// flow for /new-project and proves it has real on-disk effect: the project tree
// is written, the form is replaced by a notice, the pending action is cleared,
// and the in-memory config is refreshed so the sidebar reflects the new project
// without a relaunch.
func TestFinishWorkspaceNewProjectCreates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PACKWRIGHT_HOME", home)

	a := newApp(nil, nil)
	a.home = home

	model, _ := a.Update(paletteSelectedMsg{Slash: slashNewProject, Title: "New project"})
	a = model.(app)
	if a.pendingWorkspace == nil {
		t.Fatal("setup: pendingWorkspace nil after opening the form")
	}

	model, _ = a.Update(screens.FormSubmitMsg{
		Title:  "New project",
		Values: map[string]string{"slug": "acme", "name": "Acme Corp"},
	})
	a = model.(app)

	if a.pendingWorkspace != nil {
		t.Error("pendingWorkspace still set after submit; want it consumed")
	}
	if _, ok := a.registry.Top().(*noticeScreen); !ok {
		t.Errorf("top screen = %T, want *noticeScreen", a.registry.Top())
	}
	if info, err := os.Stat(filepath.Join(home, "projects", "acme")); err != nil || !info.IsDir() {
		t.Errorf("project dir not created under projects/acme: err=%v", err)
	}
	if a.cfg == nil || len(a.cfg.Projects) != 1 || a.cfg.Projects[0].Slug != "acme" {
		t.Errorf("a.cfg.Projects = %+v, want exactly one project 'acme' (sidebar refresh)", a.cfg)
	}
}

// TestRunWorkspaceInvalidSlugReports proves validation errors surface as an
// error (no config returned, nothing written) rather than a silent success —
// the TUI shares the CLI's slug guard.
func TestRunWorkspaceInvalidSlugReports(t *testing.T) {
	t.Setenv("PACKWRIGHT_HOME", t.TempDir())

	_, cfg, err := runWorkspace(wsNewProject, map[string]string{"slug": "Bad Slug!"})
	if err == nil {
		t.Fatal("runWorkspace(invalid slug) = nil error, want a validation error")
	}
	if cfg != nil {
		t.Error("runWorkspace returned a config on error; want nil")
	}
}

// TestRunWorkspacePromoteRequiresPath proves the template verbs guard their
// required inputs and never return a config (they do not touch config.yaml).
func TestRunWorkspacePromoteRequiresPath(t *testing.T) {
	_, cfg, err := runWorkspace(wsPromoteTemplate, map[string]string{})
	if err == nil {
		t.Fatal("runWorkspace(promote, no path) = nil error, want a required-path error")
	}
	if cfg != nil {
		t.Error("runWorkspace(promote) returned a config; want nil for template ops")
	}
}
