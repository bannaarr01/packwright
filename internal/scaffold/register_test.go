package scaffold

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/action"
	canonical "github.com/bannaarr01/packwright/internal/manifest"
	"github.com/bannaarr01/packwright/manifest"
)

// TestWizardManifests_ContainsBothSlashes confirms the wizard slot returns
// /new-command and /new-pack in stable order so palette ordering is
// deterministic.
func TestWizardManifests_ContainsBothSlashes(t *testing.T) {
	ms := WizardManifests()
	if len(ms) != 2 {
		t.Fatalf("WizardManifests returned %d entries, want 2", len(ms))
	}
	if ms[0].Slash != "/new-command" || ms[1].Slash != "/new-pack" {
		t.Errorf("slashes = [%q, %q], want [/new-command, /new-pack]", ms[0].Slash, ms[1].Slash)
	}
}

// TestRunners_RegisteredWithDispatcher proves the init() in register.go
// fired and the dispatcher can now route a wizard manifest to its runner.
// We resolve via action.Lookup directly so the test does not also exercise
// dispatch.Dispatch (covered in command_test.go's round-trip).
func TestRunners_RegisteredWithDispatcher(t *testing.T) {
	for _, kind := range []manifest.Kind{KindNewCommand, KindNewPack} {
		r, ok := action.Lookup(kind)
		if !ok {
			t.Fatalf("action.Lookup(%q) = (_, false), want a registered runner", kind)
		}
		if r.Kind() != kind {
			t.Errorf("runner.Kind() = %q, want %q", r.Kind(), kind)
		}
	}
}

// TestNewCommandRunner_WritesGeneratedYAML drives the full /new-command
// happy path: inputs in, manifest YAML on disk, and that disk file passes
// the canonical loader. This is the integration analogue of the
// command_test.go round-trip.
func TestNewCommandRunner_WritesGeneratedYAML(t *testing.T) {
	saveDir := t.TempDir()
	inputs := action.Inputs{
		FieldKind:    string(manifest.KindShell),
		FieldSlash:   "/open-docs",
		FieldTitle:   "Open AWS docs",
		FieldSaveDir: saveDir,
	}
	r, _ := action.Lookup(KindNewCommand)
	res, err := r.Run(context.Background(), newCommandWizard(), inputs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	out, ok := res.Value.(*NewCommandResult)
	if !ok || out == nil {
		t.Fatalf("Result.Value = %T, want *NewCommandResult", res.Value)
	}
	if want := filepath.Join(saveDir, "open-docs.yaml"); out.Path != want {
		t.Errorf("Path = %q, want %q", out.Path, want)
	}
	if _, err := canonical.Load(out.Path); err != nil {
		t.Fatalf("canonical.Load(generated): %v", err)
	}
}

// TestNewCommandRunner_RefusesOverwrite covers the secondary safety check:
// running /new-command twice with the same slash must not silently clobber
// the first file.
func TestNewCommandRunner_RefusesOverwrite(t *testing.T) {
	saveDir := t.TempDir()
	inputs := action.Inputs{
		FieldKind:    string(manifest.KindShell),
		FieldSlash:   "/twin",
		FieldTitle:   "Twin",
		FieldSaveDir: saveDir,
	}
	r, _ := action.Lookup(KindNewCommand)
	if _, err := r.Run(context.Background(), newCommandWizard(), inputs); err != nil {
		t.Fatalf("Run #1: %v", err)
	}
	_, err := r.Run(context.Background(), newCommandWizard(), inputs)
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("Run #2: err = %v, want overwrite-refusal", err)
	}
}

// TestNewPackRunner_CreatesTree validates the parallel happy path for the
// /new-pack runner: a parent dir + a name come in, a populated pack tree
// goes out.
func TestNewPackRunner_CreatesTree(t *testing.T) {
	parent := t.TempDir()
	inputs := action.Inputs{
		FieldPackName:        "demo",
		FieldPackParentDir:   parent,
		FieldPackDescription: "demo desc",
	}
	r, _ := action.Lookup(KindNewPack)
	res, err := r.Run(context.Background(), newPackWizard(), inputs)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	out, ok := res.Value.(*NewPackResult)
	if !ok || out == nil {
		t.Fatalf("Result.Value = %T, want *NewPackResult", res.Value)
	}
	if want := filepath.Join(parent, "demo"); out.Root != want {
		t.Errorf("Root = %q, want %q", out.Root, want)
	}
	if _, err := os.Stat(filepath.Join(out.Root, "pack.yaml")); err != nil {
		t.Errorf("pack.yaml missing: %v", err)
	}
}

// TestRunner_Validate_RejectsWrongKind ensures the runners' Validate is
// strict: a kind mismatch must surface before Run is reached. This guards
// against a misconfigured registry routing the wrong manifest to a wizard
// runner.
func TestRunner_Validate_RejectsWrongKind(t *testing.T) {
	cases := []struct {
		runner action.Runner
		kind   manifest.Kind
	}{
		{newCommandRunner{}, KindNewPack},
		{newPackRunner{}, KindNewCommand},
	}
	for _, tc := range cases {
		err := tc.runner.Validate(&manifest.Manifest{Kind: tc.kind})
		if err == nil {
			t.Errorf("%T.Validate(kind=%q): err = nil, want mismatch error", tc.runner, tc.kind)
		}
	}
}

// TestRunner_Validate_RejectsNilManifest documents the nil guard. The
// dispatcher returns ErrNoManifest before reaching here in production, but
// runners should not panic if called directly.
func TestRunner_Validate_RejectsNilManifest(t *testing.T) {
	for _, r := range []action.Runner{newCommandRunner{}, newPackRunner{}} {
		if err := r.Validate(nil); err == nil {
			t.Errorf("%T.Validate(nil): err = nil, want non-nil", r)
		}
	}
}

// TestSlugFromSlash documents the filename derivation: slashes become
// dashes so /aws/alb produces aws-alb.yaml, not a nested directory.
func TestSlugFromSlash(t *testing.T) {
	cases := []struct {
		slash string
		want  string
	}{
		{"/restart-api", "restart-api"},
		{"/aws/alb", "aws-alb"},
		{"/x", "x"},
	}
	for _, tc := range cases {
		if got := slugFromSlash(tc.slash); got != tc.want {
			t.Errorf("slugFromSlash(%q) = %q, want %q", tc.slash, got, tc.want)
		}
	}
}

// TestNewCommandRunner_MissingSaveDirIsTyped checks that missing inputs
// produce a clean error mentioning the field, so the wizard front-end can
// route the message to the right form input.
func TestNewCommandRunner_MissingSaveDirIsTyped(t *testing.T) {
	inputs := action.Inputs{
		FieldKind:  string(manifest.KindShell),
		FieldSlash: "/x",
		FieldTitle: "X",
	}
	r, _ := action.Lookup(KindNewCommand)
	_, err := r.Run(context.Background(), newCommandWizard(), inputs)
	if err == nil || !strings.Contains(err.Error(), FieldSaveDir) {
		t.Errorf("err = %v, want it to mention %q", err, FieldSaveDir)
	}
}

// errSentinel proves the runner returns a plain error (not a panic) when
// the spec it builds from inputs fails validation. We feed an empty slash
// so validateSpec fires.
func TestNewCommandRunner_GenerateErrorPropagates(t *testing.T) {
	saveDir := t.TempDir()
	inputs := action.Inputs{
		FieldKind:    string(manifest.KindShell),
		FieldSlash:   "",
		FieldTitle:   "X",
		FieldSaveDir: saveDir,
	}
	r, _ := action.Lookup(KindNewCommand)
	_, err := r.Run(context.Background(), newCommandWizard(), inputs)
	var nilErr error
	if errors.Is(err, nilErr) {
		t.Fatal("err = nil, want propagated validateSpec error")
	}
	if err == nil || !strings.Contains(err.Error(), "Slash") {
		t.Errorf("err = %v, want it to mention Slash", err)
	}
}
