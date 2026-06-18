package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/workspace"
)

// withHome plants an isolated Packwright home in a per-test temp dir so the
// slash-command code paths exercise real config.Load / Save round-trips
// without touching the developer's actual config.yaml. AWS_REGION is also
// cleared so default-region behaviour stays deterministic.
func withHome(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "pw")
	t.Setenv("PACKWRIGHT_HOME", root)
	t.Setenv("AWS_REGION", "")
	return root
}

func TestNewProjectCreatesDiskAndConfig(t *testing.T) {
	home := withHome(t)

	out := &bytes.Buffer{}
	cmd := newProjectCmd
	cmd.SetOut(out)
	cmd.SetErr(out)
	if err := cmd.RunE(cmd, []string{"acme"}); err != nil {
		t.Fatalf("new-project error = %v", err)
	}

	// Disk side: project.yaml exists.
	if _, err := os.Stat(filepath.Join(home, "projects", "acme", "project.yaml")); err != nil {
		t.Fatalf("project.yaml missing on disk: %v", err)
	}
	// Config side: project mirrored.
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.HasProject("acme") {
		t.Errorf("config.HasProject(acme) = false, want true")
	}
}

func TestNewProjectRejectsDuplicateCaseInsensitive(t *testing.T) {
	withHome(t)

	out := &bytes.Buffer{}
	cmd := newProjectCmd
	cmd.SetOut(out)
	cmd.SetErr(out)

	if err := cmd.RunE(cmd, []string{"acme"}); err != nil {
		t.Fatalf("first new-project error = %v", err)
	}
	err := cmd.RunE(cmd, []string{"Acme"})
	if !errors.Is(err, workspace.ErrProjectExists) {
		t.Fatalf("second new-project error = %v, want ErrProjectExists", err)
	}
}

func TestNewProjectRejectsInvalidSlug(t *testing.T) {
	withHome(t)
	out := &bytes.Buffer{}
	cmd := newProjectCmd
	cmd.SetOut(out)
	cmd.SetErr(out)
	err := cmd.RunE(cmd, []string{"Acme!"})
	if !errors.Is(err, workspace.ErrSlugInvalid) {
		t.Fatalf("error = %v, want ErrSlugInvalid", err)
	}
}

func TestNewEnvCreatesUnderProject(t *testing.T) {
	home := withHome(t)

	out := &bytes.Buffer{}
	pcmd := newProjectCmd
	pcmd.SetOut(out)
	if err := pcmd.RunE(pcmd, []string{"acme"}); err != nil {
		t.Fatal(err)
	}

	ecmd := newEnvCmd
	ecmd.SetOut(out)
	if err := ecmd.RunE(ecmd, []string{"acme", "dev"}); err != nil {
		t.Fatalf("new-env error = %v", err)
	}
	envFile := filepath.Join(home, "projects", "acme", "dev", "env.yaml")
	if _, err := os.Stat(envFile); err != nil {
		t.Fatalf("env.yaml missing on disk: %v", err)
	}
}

func TestNewEnvErrorsWhenProjectMissing(t *testing.T) {
	withHome(t)
	out := &bytes.Buffer{}
	ecmd := newEnvCmd
	ecmd.SetOut(out)
	err := ecmd.RunE(ecmd, []string{"ghost", "dev"})
	if !errors.Is(err, workspace.ErrProjectMissing) {
		t.Fatalf("error = %v, want ErrProjectMissing", err)
	}
}

func TestSwitchProjectUpdatesConfig(t *testing.T) {
	withHome(t)
	out := &bytes.Buffer{}
	for _, args := range [][]string{{"acme"}} {
		if err := newProjectCmd.RunE(newProjectCmd, args); err != nil {
			t.Fatal(err)
		}
	}
	if err := newEnvCmd.RunE(newEnvCmd, []string{"acme", "dev"}); err != nil {
		t.Fatal(err)
	}

	cmd := switchProjectCmd
	cmd.SetOut(out)
	if err := cmd.RunE(cmd, []string{"acme", "dev"}); err != nil {
		t.Fatalf("switch-project error = %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ActiveProject != "acme" || cfg.ActiveEnv != "dev" {
		t.Errorf("Active = %q/%q, want acme/dev", cfg.ActiveProject, cfg.ActiveEnv)
	}
}

func TestListProjectsPrintsTree(t *testing.T) {
	withHome(t)
	if err := newProjectCmd.RunE(newProjectCmd, []string{"acme"}); err != nil {
		t.Fatal(err)
	}
	if err := newEnvCmd.RunE(newEnvCmd, []string{"acme", "dev"}); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	cmd := listProjectsCmd
	cmd.SetOut(out)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("list-projects error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "acme") || !strings.Contains(got, "dev") {
		t.Errorf("list output missing acme/dev:\n%s", got)
	}
}

func TestSecondLaunchPicksUpDiskState(t *testing.T) {
	home := withHome(t)

	// First launch creates project + env + active selection.
	if err := newProjectCmd.RunE(newProjectCmd, []string{"acme"}); err != nil {
		t.Fatal(err)
	}
	if err := newEnvCmd.RunE(newEnvCmd, []string{"acme", "dev"}); err != nil {
		t.Fatal(err)
	}
	if err := switchProjectCmd.RunE(switchProjectCmd, []string{"acme", "dev"}); err != nil {
		t.Fatal(err)
	}

	// "Second launch" — fresh Load + Reconcile.
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Reconcile(home); err != nil {
		t.Fatal(err)
	}
	if cfg.ActiveProject != "acme" || cfg.ActiveEnv != "dev" {
		t.Errorf("Active = %q/%q, want acme/dev after second launch", cfg.ActiveProject, cfg.ActiveEnv)
	}
	if len(cfg.Projects) != 1 || len(cfg.Projects[0].Envs) != 1 {
		t.Errorf("Projects = %+v, want one project with one env", cfg.Projects)
	}
}

func TestManifestUnderProjectInfersScope(t *testing.T) {
	home := withHome(t)
	if _, err := workspace.CreateProject(home, workspace.Project{Slug: "acme", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.CreateEnv(home, "acme", workspace.Env{Slug: "dev", Name: "Dev"}); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(home, "projects", "acme", "dev", "manifests", "foo.yaml")
	if err := os.WriteFile(manifest, []byte("name: foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scope := workspace.ScopeOf(manifest)
	want := workspace.Scope{Kind: workspace.ScopeProject, Project: "acme", Env: "dev"}
	if scope != want {
		t.Errorf("ScopeOf = %+v, want %+v", scope, want)
	}
}
