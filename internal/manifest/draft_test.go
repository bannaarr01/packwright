package manifest

import (
	"errors"
	"strings"
	"testing"
)

// fixtureValid returns a Manifest that already passes every structural
// rule. Tests adjust one field at a time to isolate the behaviour under
// test rather than re-stamping the boilerplate per case.
func fixtureValid() *Manifest {
	return &Manifest{
		SchemaVersion: SchemaVersionV1,
		Kind:          KindResource,
		Slash:         "/alb",
		Title:         "ALB",
		Template: &TemplateSpec{
			Kind: "cloudformation",
			Path: "./alb.yaml",
		},
		Deploy: &DeploySpec{
			Driver: "script",
			Script: "./deploy.sh",
		},
	}
}

func TestIsDraftReportsField(t *testing.T) {
	if IsDraft(nil) {
		t.Fatal("IsDraft(nil) = true, want false")
	}
	m := fixtureValid()
	if IsDraft(m) {
		t.Fatal("IsDraft(non-draft) = true, want false")
	}
	m.Draft = true
	if !IsDraft(m) {
		t.Fatal("IsDraft(draft) = false, want true")
	}
}

func TestMarkDraftAndPromoteAreIdempotent(t *testing.T) {
	m := fixtureValid()
	MarkDraft(m)
	if !m.Draft {
		t.Fatal("MarkDraft did not set Draft")
	}
	MarkDraft(m) // idempotent
	if !m.Draft {
		t.Fatal("MarkDraft second call mutated Draft to false")
	}

	Promote(m)
	if m.Draft {
		t.Fatal("Promote did not clear Draft")
	}
	Promote(m) // idempotent
	if m.Draft {
		t.Fatal("Promote second call mutated Draft to true")
	}
}

func TestPromoteKeepsCopiedFromProvenance(t *testing.T) {
	m := fixtureValid()
	m.Draft = true
	m.CopiedFrom = "/alb @ packs/aws-elb/manifests/alb.yaml"
	Promote(m)
	if m.Draft {
		t.Fatal("Promote did not clear Draft")
	}
	if CopiedFrom(m) != "/alb @ packs/aws-elb/manifests/alb.yaml" {
		t.Fatalf("Promote dropped CopiedFrom = %q", m.CopiedFrom)
	}
}

func TestCopiedFromNil(t *testing.T) {
	if CopiedFrom(nil) != "" {
		t.Fatalf("CopiedFrom(nil) = %q, want \"\"", CopiedFrom(nil))
	}
}

// TestValidateReturnsErrDraftNotPromoted is the central assertion: the
// engine's existing error pipeline (Validate, called by Execute / Load /
// authoring tools) surfaces a typed ErrDraftNotPromoted carrying the
// manifest's slash. The UI's error-card model unwraps that with errors.As
// and renders "Promote this draft before deploying".
func TestValidateReturnsErrDraftNotPromoted(t *testing.T) {
	m := fixtureValid()
	MarkDraft(m)

	err := Validate(m)
	if err == nil {
		t.Fatal("Validate(draft) = nil, want ErrDraftNotPromoted")
	}

	var draftErr *ErrDraftNotPromoted
	if !errors.As(err, &draftErr) {
		t.Fatalf("Validate(draft) = %v, want errors.As => *ErrDraftNotPromoted", err)
	}
	if draftErr.Slash != "/alb" {
		t.Errorf("ErrDraftNotPromoted.Slash = %q, want %q", draftErr.Slash, "/alb")
	}
	if !strings.Contains(draftErr.Error(), "/alb") {
		t.Errorf("ErrDraftNotPromoted.Error() = %q, want it to name the slash", draftErr.Error())
	}
}

// TestValidateStructuralErrorWinsOverDraft ensures the draft check runs
// AFTER structural validation. An author with a broken draft sees the
// actionable field error first; the draft-not-promoted message only
// surfaces once the manifest is otherwise valid.
func TestValidateStructuralErrorWinsOverDraft(t *testing.T) {
	m := fixtureValid()
	m.Slash = "" // structural error
	MarkDraft(m)

	err := Validate(m)
	if err == nil {
		t.Fatal("Validate(broken draft) = nil, want a structural error")
	}
	var draftErr *ErrDraftNotPromoted
	if errors.As(err, &draftErr) {
		t.Fatalf("Validate(broken draft) = %v, want a structural error before the draft check", err)
	}
	var vErr *ValidationError
	if !errors.As(err, &vErr) {
		t.Fatalf("Validate(broken draft) = %v, want *ValidationError", err)
	}
	if vErr.Path != "slash" {
		t.Errorf("ValidationError.Path = %q, want %q", vErr.Path, "slash")
	}
}

// TestLoadKeepsDraftLoadable mirrors the watcher / hot-reload contract:
// a draft YAML file must Load successfully so it appears in the sidebar
// as a draft row. The deploy-time ErrDraftNotPromoted is the engine's
// concern, not the loader's. We verify both halves by Load + Validate.
func TestLoadKeepsDraftLoadable(t *testing.T) {
	path := writeTempYAML(t, `
schema_version: packwright.manifest.v1
kind: resource
slash: /alb-copy
title: ALB copy
_draft: true
_copied_from: "/alb @ packs/aws-elb/manifests/alb.yaml"
template:
  kind: cloudformation
  path: ./alb.yaml
deploy:
  driver: script
  script: ./d.sh
`)

	m, err := Load(path)
	if err != nil {
		t.Fatalf("Load(draft) error = %v, want nil so the watcher can register it", err)
	}
	if !IsDraft(m) {
		t.Fatal("Load(draft) returned a manifest with Draft = false")
	}
	if CopiedFrom(m) != "/alb @ packs/aws-elb/manifests/alb.yaml" {
		t.Errorf("CopiedFrom = %q, want %q", CopiedFrom(m), "/alb @ packs/aws-elb/manifests/alb.yaml")
	}

	// And Validate on the loaded manifest surfaces the draft error so
	// the engine's deploy path knows to block.
	err = Validate(m)
	var draftErr *ErrDraftNotPromoted
	if !errors.As(err, &draftErr) {
		t.Fatalf("Validate(loaded draft) = %v, want *ErrDraftNotPromoted", err)
	}
	if draftErr.Slash != "/alb-copy" {
		t.Errorf("ErrDraftNotPromoted.Slash = %q, want %q", draftErr.Slash, "/alb-copy")
	}
}

// TestLoadAcceptsUnknownUnderscoreRootKey verifies the loader-side half
// of ADR-0047's "_"-prefix contract: an unknown root key prefixed with
// "_" produces a warning, the manifest still loads, and non-prefixed
// unknown keys remain hard errors (covered by TestLoadRejectsUnknownYAMLKey).
func TestLoadAcceptsUnknownUnderscoreRootKey(t *testing.T) {
	path := writeTempYAML(t, `
schema_version: packwright.manifest.v1
kind: resource
slash: /x
title: X
_archived: true
template:
  kind: cloudformation
  path: ./x.yaml
deploy:
  driver: script
  script: ./d.sh
`)

	m, warnings, err := LoadWithWarnings(path)
	if err != nil {
		t.Fatalf("Load(unknown _-prefixed key) error = %v, want nil", err)
	}
	if m == nil {
		t.Fatal("Load returned a nil manifest")
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	if warnings[0].Key != "_archived" {
		t.Errorf("warning.Key = %q, want %q", warnings[0].Key, "_archived")
	}
}
