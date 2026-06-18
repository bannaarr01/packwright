package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const albFixtureYAML = `schema_version: packwright.manifest.v1
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

// TestCopyTemplateCommandWritesDraftUnderProjectScope wires the exact
// happy-path PR-05 calls out in its definition of done: /copy-template
// /alb -> /alb-copy under project acme/dev writes
// projects/acme/dev/drafts/alb-copy.yaml with _draft: true and the
// expected _copied_from line.
func TestCopyTemplateCommandWritesDraftUnderProjectScope(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "packs", "aws-elb", "manifests")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	srcPath := filepath.Join(srcDir, "alb.yaml")
	if err := os.WriteFile(srcPath, []byte(albFixtureYAML), 0o600); err != nil {
		t.Fatalf("write src: %v", err)
	}

	scopeDir := filepath.Join(root, "projects", "acme", "dev")

	// Snapshot and restore the package-level flag bag so the test does
	// not leak state into siblings that also build a root command.
	saved := copyTemplateOpts
	t.Cleanup(func() { copyTemplateOpts = saved })

	c := newRootCmd()
	c.SetArgs([]string{
		"copy-template",
		"--src", srcPath,
		"--dest", scopeDir,
		"--slash", "/alb-copy",
	})
	buf := &bytes.Buffer{}
	c.SetOut(buf)
	c.SetErr(buf)

	if err := c.Execute(); err != nil {
		t.Fatalf("Execute(copy-template): %v\noutput:\n%s", err, buf.String())
	}

	wantPath := filepath.Join(scopeDir, "drafts", "alb-copy.yaml")
	body, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read %s: %v", wantPath, err)
	}
	got := string(body)
	if !strings.Contains(got, "_draft: true") {
		t.Errorf("draft file missing _draft: true:\n%s", got)
	}
	if !strings.Contains(got, "_copied_from: /alb @ "+srcPath) {
		t.Errorf("draft file missing expected _copied_from line:\n%s", got)
	}
	if !strings.Contains(got, "slash: /alb-copy") {
		t.Errorf("draft file did not rewrite slash:\n%s", got)
	}
}

// TestCopyTemplateCommandRejectsMissingFlags surfaces the friendlier
// error path: a CI caller forgetting --src / --dest / --slash should see
// the field name in the error rather than a downstream "empty path" stack.
func TestCopyTemplateCommandRejectsMissingFlags(t *testing.T) {
	saved := copyTemplateOpts
	t.Cleanup(func() { copyTemplateOpts = saved })

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing src", []string{"copy-template", "--dest", "/tmp", "--slash", "/x"}, "--src"},
		{"missing dest", []string{"copy-template", "--src", "/tmp/x.yaml", "--slash", "/x"}, "--dest"},
		{"missing slash", []string{"copy-template", "--src", "/tmp/x.yaml", "--dest", "/tmp"}, "--slash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Reset flags between sub-tests — cobra parses into shared globals.
			copyTemplateOpts = copyTemplateFlags{}
			c := newRootCmd()
			c.SetArgs(tc.args)
			buf := &bytes.Buffer{}
			c.SetOut(buf)
			c.SetErr(buf)
			err := c.Execute()
			if err == nil {
				t.Fatalf("Execute(%v) = nil, want error mentioning %q", tc.args, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
