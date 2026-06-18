package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bannaarr01/packwright/tui/screens"
)

// writeDemoPack lays out a minimal installed pack under <home>/packs/demo with
// a single resource manifest (/alb) that declares a one-field form, so the
// palette-routing tests can resolve and run it without touching AWS.
func writeDemoPack(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, "packs", "demo")
	manifests := filepath.Join(dir, "manifests")
	if err := os.MkdirAll(manifests, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"),
		[]byte("name: demo\nversion: 0.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `schema_version: "1.0"
id: demo.alb
kind: resource
slash: /alb
title: Deploy ALB
template:
  kind: cloudformation
  path: ../templates/alb.yaml
deploy:
  driver: script
  script: ../deploy.sh
  env:
    STACK_NAME: "alb-{{ .Project }}"
form:
  - id: Project
    label: Project
    type: string
    required: true
`
	if err := os.WriteFile(filepath.Join(manifests, "alb.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestResolveManifestForSlash proves the palette slash resolves to its backing
// manifest and that the base directory points at the manifest's own folder —
// the value the engine needs to find the relative template / script (the fix
// that lets a dispatched deploy locate its files).
func TestResolveManifestForSlash(t *testing.T) {
	home := t.TempDir()
	writeDemoPack(t, home)

	m, baseDir, ok := resolveManifestForSlash(home, nil, "/alb")
	if !ok {
		t.Fatal("resolveManifestForSlash(/alb) = !ok, want a resolved manifest")
	}
	if m.Slash != "/alb" {
		t.Errorf("resolved slash = %q, want /alb", m.Slash)
	}
	wantBase := filepath.Join(home, "packs", "demo", "manifests")
	if baseDir != wantBase {
		t.Errorf("baseDir = %q, want %q", baseDir, wantBase)
	}

	if _, _, ok := resolveManifestForSlash(home, nil, "/does-not-exist"); ok {
		t.Error("resolveManifestForSlash(/does-not-exist) = ok, want !ok")
	}
}

// TestAppManifestSlashOpensForm verifies the core Phase-2 win: selecting a
// manifest-backed slash in the palette pushes its input form instead of being
// the old no-op. Before this wiring the registry stayed at depth 1.
func TestAppManifestSlashOpensForm(t *testing.T) {
	home := t.TempDir()
	writeDemoPack(t, home)

	a := newApp(nil, nil)
	a.home = home

	model, _ := a.Update(paletteSelectedMsg{Slash: "/alb", Title: "Deploy ALB"})
	a = model.(app)

	if d := a.registry.Depth(); d != 2 {
		t.Fatalf("after picking /alb, registry depth = %d, want 2 (form pushed)", d)
	}
	if _, ok := a.registry.Top().(*screens.FormScreen); !ok {
		t.Errorf("top screen = %T, want *screens.FormScreen", a.registry.Top())
	}
	if a.pendingRun == nil {
		t.Error("pendingRun is nil after opening the form; want the manifest stashed for submit")
	}
}

// TestAppFormSubmitReplacesWithRunScreen verifies that submitting the input
// form swaps the form for the run screen (in place, so Esc returns to the
// launcher) and clears the pending run.
func TestAppFormSubmitReplacesWithRunScreen(t *testing.T) {
	home := t.TempDir()
	writeDemoPack(t, home)

	a := newApp(nil, nil)
	a.home = home
	model, _ := a.Update(paletteSelectedMsg{Slash: "/alb", Title: "Deploy ALB"})
	a = model.(app)
	if a.pendingRun == nil {
		t.Fatal("setup: pendingRun not set after opening form")
	}

	model, _ = a.Update(screens.FormSubmitMsg{Title: "Deploy ALB", Values: map[string]string{"Project": "demo"}})
	a = model.(app)

	if d := a.registry.Depth(); d != 2 {
		t.Fatalf("after submit, registry depth = %d, want 2 (form replaced by run screen)", d)
	}
	if _, ok := a.registry.Top().(*runScreen); !ok {
		t.Errorf("top screen = %T, want *runScreen", a.registry.Top())
	}
	if a.pendingRun != nil {
		t.Error("pendingRun still set after submit; want it consumed")
	}
}

// TestAppWizardSlashOpensForm verifies the built-in scaffolder wizards
// (/new-command, /new-pack) are reachable through the same palette → form →
// dispatch path even though they are not on-disk packs.
func TestAppWizardSlashOpensForm(t *testing.T) {
	a := newApp(nil, nil)
	a.home = t.TempDir() // no packs installed; the wizard must still resolve

	model, _ := a.Update(paletteSelectedMsg{Slash: "/new-pack", Title: "New pack"})
	a = model.(app)

	if d := a.registry.Depth(); d != 2 {
		t.Fatalf("after picking /new-pack, registry depth = %d, want 2 (wizard form pushed)", d)
	}
	if _, ok := a.registry.Top().(*screens.FormScreen); !ok {
		t.Errorf("top screen = %T, want *screens.FormScreen", a.registry.Top())
	}
}

// TestAppFormSubmitWithoutPendingRunIsNoop guards the defensive path: a stray
// FormSubmitMsg with no pending run must not push anything or panic.
func TestAppFormSubmitWithoutPendingRunIsNoop(t *testing.T) {
	a := newApp(nil, nil)
	model, _ := a.Update(screens.FormSubmitMsg{Title: "x", Values: map[string]string{}})
	a = model.(app)
	if d := a.registry.Depth(); d != 1 {
		t.Errorf("registry depth = %d, want 1 (no pending run → no screen)", d)
	}
}

// compile-time guarantee that runScreen satisfies the screen contract.
var _ screens.Screen = (*runScreen)(nil)
