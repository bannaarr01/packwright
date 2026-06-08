package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestNewPack_HappyPath exercises the full directory-tree scaffold: every
// declared sub-directory, the .gitkeep placeholders, pack.yaml, and the
// README must all land on disk with non-empty contents.
func TestNewPack_HappyPath(t *testing.T) {
	parent := t.TempDir()
	root, err := NewPack(parent, PackSpec{
		Name:        "my-pack",
		Description: "Demo pack",
		Author:      "Jane Doe",
		Homepage:    "https://example.com",
	})
	if err != nil {
		t.Fatalf("NewPack: %v", err)
	}
	if want := filepath.Join(parent, "my-pack"); root != want {
		t.Errorf("returned root = %q, want %q", root, want)
	}

	for _, sub := range packSubdirs {
		dir := filepath.Join(root, sub)
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			t.Errorf("subdir %q missing or not a directory: %v", sub, err)
		}
		gitkeep := filepath.Join(dir, ".gitkeep")
		if _, err := os.Stat(gitkeep); err != nil {
			t.Errorf(".gitkeep missing in %q: %v", sub, err)
		}
	}

	packYAML, err := os.ReadFile(filepath.Join(root, "pack.yaml"))
	if err != nil {
		t.Fatalf("read pack.yaml: %v", err)
	}
	var meta map[string]any
	if err := yaml.Unmarshal(packYAML, &meta); err != nil {
		t.Fatalf("pack.yaml is not valid YAML: %v\n%s", err, packYAML)
	}
	if meta["name"] != "my-pack" {
		t.Errorf("pack.yaml name = %v, want %q", meta["name"], "my-pack")
	}
	if meta["description"] != "Demo pack" {
		t.Errorf("pack.yaml description = %v", meta["description"])
	}
	if meta["author"] != "Jane Doe" {
		t.Errorf("pack.yaml author = %v", meta["author"])
	}

	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	if !strings.Contains(string(readme), "my-pack") {
		t.Errorf("README.md missing pack name:\n%s", readme)
	}
}

// TestNewPack_RefusesOverwrite checks the safety guard: if the destination
// directory already exists, NewPack must refuse rather than silently
// merging into an author's existing tree.
func TestNewPack_RefusesOverwrite(t *testing.T) {
	parent := t.TempDir()
	if _, err := NewPack(parent, PackSpec{Name: "twin"}); err != nil {
		t.Fatalf("NewPack #1: %v", err)
	}
	_, err := NewPack(parent, PackSpec{Name: "twin"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("NewPack #2: err = %v, want already-exists error", err)
	}
}

// TestNewPack_ValidatesName covers the portability checks: empty,
// separator-containing, and dot-prefixed names are rejected.
func TestNewPack_ValidatesName(t *testing.T) {
	parent := t.TempDir()
	cases := []struct {
		name    string
		want    string
		comment string
	}{
		{"", "Name is required", "empty"},
		{"a/b", "path separators", "slash"},
		{".hidden", "dot", "leading dot"},
		{"has space", "whitespace", "whitespace"},
	}
	for _, tc := range cases {
		t.Run(tc.comment, func(t *testing.T) {
			_, err := NewPack(parent, PackSpec{Name: tc.name})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("NewPack(%q): err = %v, want it to mention %q", tc.name, err, tc.want)
			}
		})
	}
}

// TestNewPack_MinimalSpec confirms that optional metadata is omitted from
// pack.yaml cleanly — no empty `description:` key, no orphaned colons.
func TestNewPack_MinimalSpec(t *testing.T) {
	parent := t.TempDir()
	root, err := NewPack(parent, PackSpec{Name: "minimal"})
	if err != nil {
		t.Fatalf("NewPack: %v", err)
	}
	packYAML, err := os.ReadFile(filepath.Join(root, "pack.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(packYAML)
	if strings.Contains(got, "description:") {
		t.Errorf("pack.yaml carries empty description key:\n%s", got)
	}
	if strings.Contains(got, "author:") {
		t.Errorf("pack.yaml carries empty author key:\n%s", got)
	}
}
