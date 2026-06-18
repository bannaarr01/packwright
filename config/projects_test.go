package config

import (
	"errors"
	"testing"

	"github.com/bannaarr01/packwright/internal/workspace"
)

func TestAddProjectNormalizesAndRejectsDuplicate(t *testing.T) {
	c := &Config{}
	if err := c.AddProject(workspace.Project{Slug: "Acme", Name: "Acme"}); err != nil {
		t.Fatalf("AddProject error = %v", err)
	}
	if len(c.Projects) != 1 || c.Projects[0].Slug != "acme" {
		t.Errorf("Projects = %+v, want one acme entry with lowercase slug", c.Projects)
	}
	err := c.AddProject(workspace.Project{Slug: "acme", Name: "Acme2"})
	if !errors.Is(err, workspace.ErrProjectExists) {
		t.Errorf("duplicate AddProject error = %v, want ErrProjectExists", err)
	}
}

func TestAddEnv(t *testing.T) {
	c := &Config{}
	if err := c.AddProject(workspace.Project{Slug: "acme", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddEnv("acme", workspace.Env{Slug: "dev", Name: "dev"}); err != nil {
		t.Fatalf("AddEnv error = %v", err)
	}
	// Duplicate env case-insensitive.
	err := c.AddEnv("acme", workspace.Env{Slug: "Dev", Name: "Dev"})
	if !errors.Is(err, workspace.ErrEnvExists) {
		t.Errorf("duplicate AddEnv error = %v, want ErrEnvExists", err)
	}
	// Unknown project.
	err = c.AddEnv("ghost", workspace.Env{Slug: "dev", Name: "dev"})
	if !errors.Is(err, ErrUnknownProject) {
		t.Errorf("AddEnv unknown project error = %v, want ErrUnknownProject", err)
	}
}

func TestSetActive(t *testing.T) {
	c := &Config{}
	if err := c.AddProject(workspace.Project{Slug: "acme", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddEnv("acme", workspace.Env{Slug: "dev", Name: "Dev"}); err != nil {
		t.Fatal(err)
	}

	if err := c.SetActive("Acme", "Dev"); err != nil {
		t.Fatalf("SetActive error = %v", err)
	}
	if c.ActiveProject != "acme" || c.ActiveEnv != "dev" {
		t.Errorf("Active = %q/%q, want acme/dev", c.ActiveProject, c.ActiveEnv)
	}

	// Clearing.
	if err := c.SetActive("", ""); err != nil {
		t.Fatalf("clear SetActive error = %v", err)
	}
	if c.ActiveProject != "" || c.ActiveEnv != "" {
		t.Errorf("after clear Active = %q/%q, want both empty", c.ActiveProject, c.ActiveEnv)
	}

	// Project without env.
	if err := c.SetActive("acme", ""); err != nil {
		t.Fatalf("SetActive(acme,'') error = %v", err)
	}
	if c.ActiveProject != "acme" || c.ActiveEnv != "" {
		t.Errorf("Active = %q/%q, want acme/empty", c.ActiveProject, c.ActiveEnv)
	}

	// Unknown project.
	err := c.SetActive("ghost", "")
	if !errors.Is(err, ErrUnknownProject) {
		t.Errorf("error = %v, want ErrUnknownProject", err)
	}
	// Unknown env.
	err = c.SetActive("acme", "ghost")
	if !errors.Is(err, ErrUnknownEnv) {
		t.Errorf("error = %v, want ErrUnknownEnv", err)
	}
}

func TestSetActiveRejectsEnvWithoutProject(t *testing.T) {
	c := &Config{}
	if err := c.SetActive("", "dev"); err == nil {
		t.Fatal("SetActive('','dev') error = nil, want non-nil")
	}
}

func TestReconcileEmptyHome(t *testing.T) {
	home := t.TempDir()
	c := &Config{Projects: []workspace.Project{{Slug: "stale", Name: "Stale"}}}
	warnings, err := c.Reconcile(home)
	if err != nil {
		t.Fatalf("Reconcile error = %v", err)
	}
	if len(c.Projects) != 0 {
		t.Errorf("Projects = %+v, want empty after reconcile against empty disk", c.Projects)
	}
	if len(warnings) == 0 {
		t.Errorf("expected drift warning for orphan project")
	}
}

func TestReconcileImportsDiskState(t *testing.T) {
	home := withHome(t)
	if _, err := workspace.CreateProject(home, workspace.Project{Slug: "acme", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.CreateEnv(home, "acme", workspace.Env{Slug: "dev", Name: "Dev"}); err != nil {
		t.Fatal(err)
	}
	c := &Config{}
	if _, err := c.Reconcile(home); err != nil {
		t.Fatalf("Reconcile error = %v", err)
	}
	if len(c.Projects) != 1 || c.Projects[0].Slug != "acme" {
		t.Fatalf("Projects = %+v, want one acme", c.Projects)
	}
	if len(c.Projects[0].Envs) != 1 || c.Projects[0].Envs[0].Slug != "dev" {
		t.Errorf("Envs = %+v, want one dev", c.Projects[0].Envs)
	}
}

func TestReconcileRepairsStaleActiveSelection(t *testing.T) {
	home := withHome(t)
	if _, err := workspace.CreateProject(home, workspace.Project{Slug: "acme", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	c := &Config{ActiveProject: "ghost", ActiveEnv: "dev"}
	warnings, err := c.Reconcile(home)
	if err != nil {
		t.Fatal(err)
	}
	if c.ActiveProject != "" || c.ActiveEnv != "" {
		t.Errorf("Active = %q/%q, want both cleared", c.ActiveProject, c.ActiveEnv)
	}
	if len(warnings) == 0 {
		t.Errorf("expected at least one warning")
	}
}

func TestSaveLoadProjectsRoundTrip(t *testing.T) {
	home := withHome(t)
	if _, err := workspace.CreateProject(home, workspace.Project{Slug: "acme", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.CreateEnv(home, "acme", workspace.Env{Slug: "dev", Name: "Dev"}); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		Profile:       "prod",
		Region:        "us-east-1",
		Theme:         "auto",
		LogLevel:      "info",
		ActiveProject: "acme",
		ActiveEnv:     "dev",
	}
	if _, err := cfg.Reconcile(home); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ActiveProject != "acme" || loaded.ActiveEnv != "dev" {
		t.Errorf("loaded Active = %q/%q, want acme/dev", loaded.ActiveProject, loaded.ActiveEnv)
	}
	if len(loaded.Projects) != 1 || loaded.Projects[0].Slug != "acme" {
		t.Errorf("loaded Projects = %+v", loaded.Projects)
	}
}
