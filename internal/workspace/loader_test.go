package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCreateProjectMaterializesDisk(t *testing.T) {
	home := t.TempDir()

	got, err := CreateProject(home, Project{Slug: "acme", Name: "Acme"})
	if err != nil {
		t.Fatalf("CreateProject error = %v", err)
	}
	if got.Slug != "acme" {
		t.Errorf("Slug = %q, want %q", got.Slug, "acme")
	}

	projPath := filepath.Join(home, "projects", "acme", "project.yaml")
	data, err := os.ReadFile(projPath)
	if err != nil {
		t.Fatalf("project.yaml missing: %v", err)
	}
	if !strings.Contains(string(data), "slug: acme") {
		t.Errorf("project.yaml does not contain slug: acme:\n%s", string(data))
	}
}

func TestCreateProjectNormalizesSlug(t *testing.T) {
	home := t.TempDir()

	got, err := CreateProject(home, Project{Slug: "Acme", Name: "Acme"})
	if err != nil {
		t.Fatalf("CreateProject error = %v", err)
	}
	if got.Slug != "acme" {
		t.Errorf("Slug = %q, want lowercased %q", got.Slug, "acme")
	}
	if _, err := os.Stat(filepath.Join(home, "projects", "acme")); err != nil {
		t.Errorf("expected projects/acme dir, got: %v", err)
	}
}

func TestCreateProjectRejectsDuplicateCaseInsensitive(t *testing.T) {
	home := t.TempDir()

	if _, err := CreateProject(home, Project{Slug: "acme", Name: "Acme"}); err != nil {
		t.Fatalf("first CreateProject error = %v", err)
	}
	_, err := CreateProject(home, Project{Slug: "Acme", Name: "Acme #2"})
	if !errors.Is(err, ErrProjectExists) {
		t.Fatalf("second CreateProject error = %v, want ErrProjectExists", err)
	}
}

func TestCreateProjectRejectsInvalidSlug(t *testing.T) {
	home := t.TempDir()
	_, err := CreateProject(home, Project{Slug: "Acme!", Name: "no"})
	if !errors.Is(err, ErrSlugInvalid) {
		t.Fatalf("error = %v, want ErrSlugInvalid", err)
	}
	// No directory should have been created.
	if _, err := os.Stat(filepath.Join(home, "projects")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("projects/ should not exist after a rejected create, stat = %v", err)
	}
}

func TestCreateEnvMaterializesDisk(t *testing.T) {
	home := t.TempDir()
	if _, err := CreateProject(home, Project{Slug: "acme", Name: "Acme"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	got, err := CreateEnv(home, "acme", Env{Slug: "dev", Name: "Development"})
	if err != nil {
		t.Fatalf("CreateEnv error = %v", err)
	}
	if got.Slug != "dev" {
		t.Errorf("Slug = %q, want %q", got.Slug, "dev")
	}
	envPath := filepath.Join(home, "projects", "acme", "dev", "env.yaml")
	if _, err := os.Stat(envPath); err != nil {
		t.Fatalf("env.yaml missing: %v", err)
	}
	// Standard subtree created.
	for _, sub := range []string{"manifests", "drafts", "stacks"} {
		p := filepath.Join(home, "projects", "acme", "dev", sub)
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() {
			t.Errorf("subdir %q not created: stat = %v", sub, err)
		}
	}
}

func TestCreateEnvRequiresExistingProject(t *testing.T) {
	home := t.TempDir()
	_, err := CreateEnv(home, "ghost", Env{Slug: "dev", Name: "Development"})
	if !errors.Is(err, ErrProjectMissing) {
		t.Fatalf("error = %v, want ErrProjectMissing", err)
	}
}

func TestCreateEnvRejectsDuplicateCaseInsensitive(t *testing.T) {
	home := t.TempDir()
	if _, err := CreateProject(home, Project{Slug: "acme", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateEnv(home, "acme", Env{Slug: "dev", Name: "dev"}); err != nil {
		t.Fatal(err)
	}
	_, err := CreateEnv(home, "acme", Env{Slug: "Dev", Name: "Dev2"})
	if !errors.Is(err, ErrEnvExists) {
		t.Fatalf("error = %v, want ErrEnvExists", err)
	}
}

func TestLoadAllRoundTrip(t *testing.T) {
	home := t.TempDir()
	if _, err := CreateProject(home, Project{
		Slug: "acme", Name: "Acme", Description: "the project", Region: "us-east-1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateEnv(home, "acme", Env{Slug: "dev", Name: "Development", Region: "us-east-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateEnv(home, "acme", Env{Slug: "prd", Name: "Production", Region: "eu-west-1"}); err != nil {
		t.Fatal(err)
	}

	projects, warnings := LoadAll(home)
	if len(warnings) != 0 {
		t.Fatalf("LoadAll warnings = %v, want none", warnings)
	}
	if len(projects) != 1 {
		t.Fatalf("LoadAll = %d projects, want 1", len(projects))
	}
	p := projects[0]
	if p.Slug != "acme" || p.Name != "Acme" || p.Description != "the project" || p.Region != "us-east-1" {
		t.Errorf("project mismatch: %+v", p)
	}
	wantEnvs := []Env{
		{Slug: "dev", Name: "Development", Region: "us-east-1"},
		{Slug: "prd", Name: "Production", Region: "eu-west-1"},
	}
	if !reflect.DeepEqual(p.Envs, wantEnvs) {
		t.Errorf("envs = %+v, want %+v", p.Envs, wantEnvs)
	}
}

func TestLoadAllEmptyHome(t *testing.T) {
	home := t.TempDir()
	projects, warnings := LoadAll(home)
	if len(projects) != 0 {
		t.Errorf("LoadAll on empty home = %d projects, want 0", len(projects))
	}
	if len(warnings) != 0 {
		t.Errorf("LoadAll warnings on empty home = %v, want none", warnings)
	}
}

func TestLoadAllSkipsInvalidProjectDir(t *testing.T) {
	home := t.TempDir()
	if _, err := CreateProject(home, Project{Slug: "acme", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	// Drop a bare directory with an invalid slug — should be skipped with
	// a warning, not error out the whole load.
	if err := os.MkdirAll(filepath.Join(home, "projects", "Bad!Name"), 0o755); err != nil {
		t.Fatal(err)
	}
	projects, warnings := LoadAll(home)
	if len(projects) != 1 || projects[0].Slug != "acme" {
		t.Errorf("projects = %+v, want only acme", projects)
	}
	if len(warnings) == 0 {
		t.Errorf("expected at least one warning for skipped dir")
	}
}

func TestWriteAtomicLeavesNoTempBehind(t *testing.T) {
	home := t.TempDir()
	if _, err := CreateProject(home, Project{Slug: "acme", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "projects", "acme")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %q", e.Name())
		}
	}
}
