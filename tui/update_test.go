package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/record"
	"github.com/bannaarr01/packwright/internal/update"
	"github.com/bannaarr01/packwright/tui/screens"
)

// writeStackFixture lays out a manifest (resource kind, with a template path
// and a scaling block) plus a persisted StackRecord pointing at it, and returns
// the store and the record's coordinates. The actual template file is written
// too so newUpdateScreen's eager read succeeds.
func writeStackFixture(t *testing.T) (store *record.Store, project, env, stack string) {
	t.Helper()
	home := t.TempDir()
	mdir := filepath.Join(home, "packs", "demo", "manifests")
	if err := os.MkdirAll(mdir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(mdir, "alb.yaml")
	manifestYAML := `schema_version: "1.0"
id: demo.alb
kind: resource
slash: /alb
title: ALB
template:
  kind: cloudformation
  path: alb-template.yaml
deploy:
  driver: script
  script: deploy.sh
scaling:
  - param: DesiredCount
    label: Desired count
    kind: integer
    min: 1
    max: 10
`
	if err := os.WriteFile(manifestPath, []byte(manifestYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mdir, "alb-template.yaml"), []byte("Resources: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store = record.NewStore(home)
	project, env, stack = "demo", "dev", "alb-dev"
	rec := &record.StackRecord{
		StackName:  stack,
		Project:    project,
		Env:        env,
		Region:     "us-east-1",
		Profile:    "default",
		Manifest:   record.ManifestRef{Slash: "/alb", Source: manifestPath},
		Parameters: record.Parameters{"DesiredCount": "2"},
	}
	if err := store.Write(rec); err != nil {
		t.Fatal(err)
	}
	return store, project, env, stack
}

// TestAppRecordActionUpdatePushesUpdateScreen verifies the sidebar 'u' action
// resolves the stack and pushes the update flow — the mvp7 PR-06 "sidebar
// Update entry point".
func TestAppRecordActionUpdatePushesUpdateScreen(t *testing.T) {
	store, project, env, stack := writeStackFixture(t)
	a := newApp(nil, nil)
	a.store = store
	a.cfg = &config.Config{}

	model, _ := a.Update(screens.RecordActionMsg{Action: screens.RecordActionUpdate, Project: project, Env: env, Stack: stack})
	a = model.(app)

	if d := a.registry.Depth(); d != 2 {
		t.Fatalf("after update action, depth = %d, want 2", d)
	}
	if _, ok := a.registry.Top().(*updateScreen); !ok {
		t.Errorf("top screen = %T, want *updateScreen", a.registry.Top())
	}
}

// TestAppRecordActionScalePushesForm verifies the 'scale' action opens the
// pre-filled scaling form and stashes a pendingScale run (mvp7 PR-07).
func TestAppRecordActionScalePushesForm(t *testing.T) {
	store, project, env, stack := writeStackFixture(t)
	a := newApp(nil, nil)
	a.store = store
	a.cfg = &config.Config{}

	model, _ := a.Update(screens.RecordActionMsg{Action: screens.RecordActionScale, Project: project, Env: env, Stack: stack})
	a = model.(app)

	if _, ok := a.registry.Top().(*screens.FormScreen); !ok {
		t.Fatalf("top screen = %T, want *screens.FormScreen", a.registry.Top())
	}
	if a.pendingRun == nil || a.pendingRun.kind != pendingScale {
		t.Errorf("pendingRun = %+v, want a pendingScale run", a.pendingRun)
	}
}

// TestAppRecordActionDeletePushesNotice verifies the 'delete' action surfaces
// the deletion hand-off notice rather than silently doing nothing.
func TestAppRecordActionDeletePushesNotice(t *testing.T) {
	store, project, env, stack := writeStackFixture(t)
	a := newApp(nil, nil)
	a.store = store

	model, _ := a.Update(screens.RecordActionMsg{Action: screens.RecordActionDelete, Project: project, Env: env, Stack: stack})
	a = model.(app)

	if _, ok := a.registry.Top().(*noticeScreen); !ok {
		t.Errorf("top screen = %T, want *noticeScreen", a.registry.Top())
	}
}

// TestUpdateScreenScaleConfirmGateCancels verifies the ADR-0049 env-guard
// confirmation fires before any AWS call: a screen built with a scaleReason
// starts in the confirm phase and 'n' cancels without dispatching.
func TestUpdateScreenScaleConfirmGateCancels(t *testing.T) {
	tmpl := filepath.Join(t.TempDir(), "t.yaml")
	if err := os.WriteFile(tmpl, []byte("Resources: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	us := newUpdateScreen(DefaultKeyMap(), nil, 80, 24, "scale", "alb-prd", "", "", tmpl,
		map[string]string{"DesiredCount": "30"}, map[string]string{"DesiredCount": "2"},
		"desc", "scale on prd env")
	if us.phase != phaseConfirmScale {
		t.Fatalf("phase = %d, want phaseConfirmScale", us.phase)
	}
	if cmd := us.Init(); cmd != nil {
		t.Error("Init() returned a command while awaiting scale confirmation; want nil")
	}
	screen, _ := us.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	us = screen.(*updateScreen)
	if us.phase != phaseFinished {
		t.Errorf("after 'n', phase = %d, want phaseFinished (cancelled)", us.phase)
	}
}

// TestUpdateScreenTemplateReadError verifies a missing template fails inline
// (before any AWS call) rather than panicking.
func TestUpdateScreenTemplateReadError(t *testing.T) {
	us := newUpdateScreen(DefaultKeyMap(), nil, 80, 24, "update", "alb-dev", "", "",
		filepath.Join(t.TempDir(), "missing.yaml"), nil, nil, "desc", "")
	cmd := us.Init()
	if cmd == nil {
		t.Fatal("Init() returned nil; want a command surfacing the template read error")
	}
	msg := cmd()
	ready, ok := msg.(updateReadyMsg)
	if !ok || ready.err == nil {
		t.Fatalf("Init cmd produced %#v, want updateReadyMsg with a non-nil err", msg)
	}
	screen, _ := us.Update(ready)
	us = screen.(*updateScreen)
	if us.phase != phaseFinished {
		t.Errorf("phase = %d, want phaseFinished after error", us.phase)
	}
}

// TestReplacementConsentFailClosed verifies that a replacement-consent request
// arriving while no update screen is on top is denied (fail-closed) so the
// blocked coordinator goroutine never leaks.
func TestReplacementConsentFailClosed(t *testing.T) {
	a := newApp(nil, nil) // launcher on top, not an update screen
	reply := make(chan update.ConsentDecision, 1)
	a.Update(replacementConsentMsg{payload: update.ReplacementPayload{Count: 1}, reply: reply})
	select {
	case got := <-reply:
		if got != update.ConsentDeny {
			t.Errorf("reply = %v, want ConsentDeny (fail-closed)", got)
		}
	default:
		t.Error("no consent decision was sent; the coordinator goroutine would leak")
	}
}
