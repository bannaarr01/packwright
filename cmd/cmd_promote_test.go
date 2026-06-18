package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPromoteTemplateCommandClearsDraft wires the /promote-template DOD:
// running promote on a draft removes `_draft: true`, keeps the rest of
// the document intact, and surfaces a confirmation line on stdout.
func TestPromoteTemplateCommandClearsDraft(t *testing.T) {
	root := t.TempDir()
	draftPath := filepath.Join(root, "projects", "acme", "dev", "drafts", "alb-copy.yaml")
	if err := os.MkdirAll(filepath.Dir(draftPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const draft = `_draft: true
_copied_from: /alb @ packs/aws-elb/manifests/alb.yaml
schema_version: packwright.manifest.v1
kind: resource
slash: /alb-copy
title: ALB copy
template:
  kind: cloudformation
  path: ./alb.yaml
deploy:
  driver: script
  script: ./deploy.sh
`
	if err := os.WriteFile(draftPath, []byte(draft), 0o600); err != nil {
		t.Fatalf("write draft: %v", err)
	}

	c := newRootCmd()
	c.SetArgs([]string{"promote-template", draftPath})
	buf := &bytes.Buffer{}
	c.SetOut(buf)
	c.SetErr(buf)
	if err := c.Execute(); err != nil {
		t.Fatalf("Execute(promote-template): %v\noutput:\n%s", err, buf.String())
	}

	body, err := os.ReadFile(draftPath)
	if err != nil {
		t.Fatalf("read promoted: %v", err)
	}
	got := string(body)
	if strings.Contains(got, "_draft") {
		t.Errorf("promoted file still contains _draft:\n%s", got)
	}
	if !strings.Contains(got, "_copied_from: /alb @") {
		t.Errorf("promoted file lost _copied_from provenance:\n%s", got)
	}
	if !strings.Contains(buf.String(), "Promoted") {
		t.Errorf("stdout = %q, want it to acknowledge the promotion", buf.String())
	}
}

// TestPromoteTemplateCommandRejectsNonDraft documents the "no, it isn't a
// draft" path: the operator gets a clear error rather than a silent no-op.
func TestPromoteTemplateCommandRejectsNonDraft(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-draft.yaml")
	const body = `schema_version: packwright.manifest.v1
kind: resource
slash: /x
title: X
template:
  kind: cloudformation
  path: ./x.yaml
deploy:
  driver: script
  script: ./d.sh
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	c := newRootCmd()
	c.SetArgs([]string{"promote-template", path})
	buf := &bytes.Buffer{}
	c.SetOut(buf)
	c.SetErr(buf)
	err := c.Execute()
	if err == nil {
		t.Fatal("Execute(promote-template, non-draft) = nil, want refusal")
	}
	if !strings.Contains(err.Error(), "not a draft") {
		t.Errorf("error = %v, want it to mention 'not a draft'", err)
	}
}
