package scaffold

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/internal/manifest"
)

const albSourceYAML = `schema_version: packwright.manifest.v1
kind: resource
slash: /alb
title: ALB
template:
  kind: cloudformation
  path: ./alb.yaml
deploy:
  driver: script
  script: ./deploy.sh
`

// TestCopyTemplateWritesDraftWithProvenance covers PR-05's central
// definition of done: /copy-template /alb -> /alb-copy under project
// acme/dev writes projects/acme/dev/drafts/alb-copy.yaml with _draft: true
// and the expected _copied_from line.
func TestCopyTemplateWritesDraftWithProvenance(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "packs", "aws-elb", "manifests")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	srcPath := filepath.Join(srcDir, "alb.yaml")
	if err := os.WriteFile(srcPath, []byte(albSourceYAML), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}

	dstDir := filepath.Join(root, "projects", "acme", "dev", "drafts")
	dstPath := filepath.Join(dstDir, "alb-copy.yaml")

	if err := CopyTemplate(srcPath, dstPath, "/alb-copy"); err != nil {
		t.Fatalf("CopyTemplate: %v", err)
	}

	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	body := string(got)

	if !strings.Contains(body, "_draft: true") {
		t.Errorf("dst body missing _draft: true:\n%s", body)
	}
	wantProvenance := "_copied_from: " + srcPath
	// The actual line is `_copied_from: /alb @ <srcPath>` — assert both halves.
	if !strings.Contains(body, "/alb @ "+srcPath) {
		t.Errorf("dst body missing %q:\n%s", wantProvenance, body)
	}
	if !strings.Contains(body, "slash: /alb-copy") {
		t.Errorf("dst body missing rewritten slash:\n%s", body)
	}

	// The result must load back through the canonical manifest loader
	// and surface as a draft. This is the contract the watcher relies on.
	m, err := manifest.Load(dstPath)
	if err != nil {
		t.Fatalf("manifest.Load(copied draft): %v", err)
	}
	if !manifest.IsDraft(m) {
		t.Fatal("loaded copy is not a draft")
	}
	if m.Slash != "/alb-copy" {
		t.Errorf("loaded slash = %q, want /alb-copy", m.Slash)
	}
	if got := manifest.CopiedFrom(m); !strings.HasPrefix(got, "/alb @ ") {
		t.Errorf("CopiedFrom = %q, want it to start with '/alb @ '", got)
	}
}

// TestCopyTemplateRefusesNonDraftsDir guards the disk-layout invariant
// from ADR-0047: a copied manifest belongs in drafts/, never directly
// under manifests/.
func TestCopyTemplateRefusesNonDraftsDir(t *testing.T) {
	root := t.TempDir()
	srcPath := writeSource(t, root, albSourceYAML)
	dstPath := filepath.Join(root, "projects", "acme", "dev", "manifests", "alb-copy.yaml")
	err := CopyTemplate(srcPath, dstPath, "/alb-copy")
	if err == nil {
		t.Fatal("CopyTemplate(into manifests/) = nil, want refusal")
	}
	if !strings.Contains(err.Error(), "drafts/") {
		t.Errorf("error = %v, want it to mention drafts/", err)
	}
}

// TestCopyTemplateRefusesOverwrite verifies the copy never clobbers an
// existing file — the user must pick a different slash or remove the
// previous copy first.
func TestCopyTemplateRefusesOverwrite(t *testing.T) {
	root := t.TempDir()
	srcPath := writeSource(t, root, albSourceYAML)
	dstPath := filepath.Join(root, "projects", "acme", "dev", "drafts", "alb-copy.yaml")
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		t.Fatalf("mkdir dst: %v", err)
	}
	if err := os.WriteFile(dstPath, []byte("pre-existing"), 0o600); err != nil {
		t.Fatalf("write existing dst: %v", err)
	}
	err := CopyTemplate(srcPath, dstPath, "/alb-copy")
	if err == nil {
		t.Fatal("CopyTemplate(existing) = nil, want refusal")
	}
	if !strings.Contains(err.Error(), "overwrite") {
		t.Errorf("error = %v, want it to mention overwrite", err)
	}
}

// TestCopyTemplateRefusesSameSlash catches the silent-mistake case where
// the user re-issues the same slash and would otherwise produce a "copy"
// indistinguishable from the source.
func TestCopyTemplateRefusesSameSlash(t *testing.T) {
	root := t.TempDir()
	srcPath := writeSource(t, root, albSourceYAML)
	dstPath := filepath.Join(root, "projects", "acme", "dev", "drafts", "alb.yaml")
	err := CopyTemplate(srcPath, dstPath, "/alb")
	if err == nil {
		t.Fatal("CopyTemplate(same slash) = nil, want refusal")
	}
}

// TestPromoteTemplateClearsDraft mirrors the /promote-template definition
// of done: removes _draft: true and leaves the rest of the document —
// including _copied_from — untouched. The intermediate state writes
// through a .tmp + rename, which gives the "valid YAML at every observable
// moment" guarantee.
func TestPromoteTemplateClearsDraft(t *testing.T) {
	root := t.TempDir()
	srcPath := writeSource(t, root, albSourceYAML)
	dstPath := filepath.Join(root, "projects", "acme", "dev", "drafts", "alb-copy.yaml")

	if err := CopyTemplate(srcPath, dstPath, "/alb-copy"); err != nil {
		t.Fatalf("CopyTemplate: %v", err)
	}

	if err := PromoteTemplate(dstPath); err != nil {
		t.Fatalf("PromoteTemplate: %v", err)
	}

	got, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatalf("read promoted: %v", err)
	}
	body := string(got)

	if strings.Contains(body, "_draft") {
		t.Errorf("promoted file still contains _draft:\n%s", body)
	}
	if !strings.Contains(body, "_copied_from") {
		t.Errorf("promoted file lost _copied_from provenance:\n%s", body)
	}

	// And the loader treats it as deployable now.
	m, err := manifest.Load(dstPath)
	if err != nil {
		t.Fatalf("manifest.Load(promoted): %v", err)
	}
	if manifest.IsDraft(m) {
		t.Fatal("promoted manifest still reports IsDraft = true")
	}
	if err := manifest.Validate(m); err != nil {
		var draftErr *manifest.ErrDraftNotPromoted
		if errors.As(err, &draftErr) {
			t.Fatalf("promoted manifest still surfaces ErrDraftNotPromoted: %v", err)
		}
		t.Fatalf("promoted manifest Validate error = %v, want nil", err)
	}
}

// TestPromoteTemplateRejectsNonDraft surfaces the user-error case where
// promote is invoked on a manifest that isn't a draft to begin with.
func TestPromoteTemplateRejectsNonDraft(t *testing.T) {
	root := t.TempDir()
	srcPath := writeSource(t, root, albSourceYAML)
	err := PromoteTemplate(srcPath)
	if err == nil {
		t.Fatal("PromoteTemplate(non-draft) = nil, want refusal")
	}
	if !strings.Contains(err.Error(), "not a draft") {
		t.Errorf("error = %v, want it to say 'not a draft'", err)
	}
}

// writeSource writes albSourceYAML into a deterministic sub-path beneath
// root and returns the absolute path. Tests use it as a fixture creator
// so the per-case scaffolding stays short and uniform.
func writeSource(t *testing.T, root, body string) string {
	t.Helper()
	srcDir := filepath.Join(root, "packs", "aws-elb", "manifests")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	srcPath := filepath.Join(srcDir, "alb.yaml")
	if err := os.WriteFile(srcPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}
	return srcPath
}
