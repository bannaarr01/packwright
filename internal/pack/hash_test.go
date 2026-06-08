package pack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHash_StableAcrossRuns guards the headline guarantee of ADR-0025:
// identical pack content always produces the same hash, regardless of
// when or how often Hash is called. A flake here voids the consent-
// screen's "this is the pack you trusted" promise.
func TestHash_StableAcrossRuns(t *testing.T) {
	root := buildPack(t, map[string]string{
		"pack.yaml":              "name: stable\nversion: 0.1.0\n",
		"manifests/restart.yaml": shellManifest("/restart", "aws", "ecs", "update-service"),
		"templates/alb.yaml":     "Resources: {}\n",
		"README.md":              "stable readme\n",
	})

	first, err := Hash(root)
	if err != nil {
		t.Fatalf("Hash first call: %v", err)
	}

	for i := 0; i < 5; i++ {
		got, err := Hash(root)
		if err != nil {
			t.Fatalf("Hash call %d: %v", i, err)
		}
		if got != first {
			t.Fatalf("Hash call %d: got %q, want %q (unstable)", i, got, first)
		}
	}
}

// TestHash_FormatAndPrefix asserts the wire-format contract: the returned
// string is "sha256:" followed by 64 hex characters. The consent screen
// renders this verbatim and downstream tooling parses on the prefix.
func TestHash_FormatAndPrefix(t *testing.T) {
	root := buildPack(t, map[string]string{
		"pack.yaml": "name: format\nversion: 0.1.0\n",
	})

	got, err := Hash(root)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !strings.HasPrefix(got, "sha256:") {
		t.Fatalf("Hash missing prefix: %q", got)
	}
	hex := strings.TrimPrefix(got, "sha256:")
	if len(hex) != 64 {
		t.Fatalf("Hash hex length = %d, want 64 (%q)", len(hex), got)
	}
	for _, r := range hex {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Fatalf("Hash hex contains non-hex char %q in %q", r, got)
		}
	}
}

// TestHash_ShellCommandChange catches the DoD requirement that changing
// a single shell command line changes the hash. This is the primary
// signal that re-prompts the consent screen on update.
func TestHash_ShellCommandChange(t *testing.T) {
	before := buildPack(t, map[string]string{
		"pack.yaml":              "name: change\nversion: 0.1.0\n",
		"manifests/restart.yaml": shellManifest("/restart", "aws", "ecs", "update-service"),
	})
	after := buildPack(t, map[string]string{
		"pack.yaml":              "name: change\nversion: 0.1.0\n",
		"manifests/restart.yaml": shellManifest("/restart", "aws", "ecs", "force-new-deployment"),
	})

	beforeHash, err := Hash(before)
	if err != nil {
		t.Fatalf("Hash before: %v", err)
	}
	afterHash, err := Hash(after)
	if err != nil {
		t.Fatalf("Hash after: %v", err)
	}
	if beforeHash == afterHash {
		t.Fatalf("Hash unchanged after editing shell command line: %q", beforeHash)
	}
}

// TestHash_ReadmeChange is the symmetry guarantee from the DoD: a README
// edit changes the tree hash. Surface stays the same (covered by the
// Scan tests), but the tree hash must move. Together with the previous
// test, this proves Hash is sensitive to *any* file under packDir.
func TestHash_ReadmeChange(t *testing.T) {
	before := buildPack(t, map[string]string{
		"pack.yaml": "name: change\nversion: 0.1.0\n",
		"README.md": "v1 readme\n",
	})
	after := buildPack(t, map[string]string{
		"pack.yaml": "name: change\nversion: 0.1.0\n",
		"README.md": "v2 readme\n",
	})

	b, err := Hash(before)
	if err != nil {
		t.Fatalf("Hash before: %v", err)
	}
	a, err := Hash(after)
	if err != nil {
		t.Fatalf("Hash after: %v", err)
	}
	if a == b {
		t.Fatalf("Hash unchanged after README edit: %q", b)
	}
}

// TestHash_IgnoresGit asserts that the .git directory is excluded from
// the hash. Two checkouts of the same content with different .git state
// (different HEAD, different pack files) must hash identically.
func TestHash_IgnoresGit(t *testing.T) {
	with := buildPack(t, map[string]string{
		"pack.yaml":        "name: gitless\nversion: 0.1.0\n",
		".git/HEAD":        "ref: refs/heads/main\n",
		".git/pack/abc.pk": "binary-looking-stuff\n",
	})
	without := buildPack(t, map[string]string{
		"pack.yaml": "name: gitless\nversion: 0.1.0\n",
	})

	a, err := Hash(with)
	if err != nil {
		t.Fatalf("Hash with .git: %v", err)
	}
	b, err := Hash(without)
	if err != nil {
		t.Fatalf("Hash without .git: %v", err)
	}
	if a != b {
		t.Fatalf("Hash differs with vs without .git: %q vs %q", a, b)
	}
}

// TestHash_SubdirectoryReorder confirms the algorithm sorts on relative
// path: nested file ordering must not depend on directory-walk order.
// Two trees with identical content under different walk orders produce
// the same hash.
func TestHash_SubdirectoryReorder(t *testing.T) {
	root := buildPack(t, map[string]string{
		"manifests/a.yaml": shellManifest("/a", "echo", "a"),
		"manifests/b.yaml": shellManifest("/b", "echo", "b"),
		"templates/a.yaml": "Resources: {}\n",
		"templates/b.yaml": "Resources: {}\n",
	})
	mirror := buildPack(t, map[string]string{
		"templates/b.yaml": "Resources: {}\n",
		"templates/a.yaml": "Resources: {}\n",
		"manifests/b.yaml": shellManifest("/b", "echo", "b"),
		"manifests/a.yaml": shellManifest("/a", "echo", "a"),
	})

	a, err := Hash(root)
	if err != nil {
		t.Fatalf("Hash root: %v", err)
	}
	b, err := Hash(mirror)
	if err != nil {
		t.Fatalf("Hash mirror: %v", err)
	}
	if a != b {
		t.Fatalf("Hash sensitive to insertion order: %q vs %q", a, b)
	}
}

// TestHash_MissingDirectory reports a clear error rather than a zero
// hash when packDir does not exist. The caller (pack install) must be
// able to distinguish "empty pack" (legitimately empty tree) from
// "missing pack" (a bug in upstream code).
func TestHash_MissingDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := Hash(root); err == nil {
		t.Fatalf("Hash on missing directory: want error, got nil")
	}
}

// buildPack writes the supplied files into a fresh temp directory and
// returns the directory's absolute path. Keys are pack-relative paths
// (forward-slash); intermediate directories are created. Tests use this
// helper instead of hand-rolling os.MkdirAll/WriteFile so the table-
// driven hash tests stay focused on the property under test.
func buildPack(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll %q: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile %q: %v", path, err)
		}
	}
	return root
}

// shellManifest renders a minimal kind: shell manifest YAML with the
// given slash and argv. Returned as a string so callers can drop it
// directly into the buildPack file map.
func shellManifest(slash string, argv ...string) string {
	var b strings.Builder
	b.WriteString("schema_version: packwright.manifest.v1\n")
	b.WriteString("kind: shell\n")
	b.WriteString("slash: ")
	b.WriteString(slash)
	b.WriteString("\n")
	b.WriteString("title: ")
	b.WriteString(strings.TrimPrefix(slash, "/"))
	b.WriteString("\n")
	b.WriteString("run:\n  command:\n")
	for _, a := range argv {
		b.WriteString("    - ")
		b.WriteString(a)
		b.WriteString("\n")
	}
	return b.String()
}
