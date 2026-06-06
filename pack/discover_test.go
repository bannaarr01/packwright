package pack

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testdataHome builds a home directory layout that wraps the committed
// testdata/packs fixture, so Discover can be exercised against
// <home>/packs/sample-pack/... without copying the fixture.
func testdataHome(t *testing.T) string {
	t.Helper()

	abs, err := filepath.Abs(filepath.Join("testdata"))
	if err != nil {
		t.Fatalf("resolving testdata path: %v", err)
	}
	return abs
}

func TestDiscoverYieldsSamplePack(t *testing.T) {
	packs, err := Discover(testdataHome(t))
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(packs) != 1 {
		t.Fatalf("Discover returned %d packs, want 1", len(packs))
	}

	p := packs[0]
	if p.Name != "sample-pack" {
		t.Errorf("pack name = %q, want %q", p.Name, "sample-pack")
	}
	if p.Version != "0.1.0" {
		t.Errorf("pack version = %q, want %q", p.Version, "0.1.0")
	}
	if got, want := p.Meta.Requires["packwright"], ">=0.1.0"; got != want {
		t.Errorf("requires[packwright] = %q, want %q", got, want)
	}
	if !filepath.IsAbs(p.Dir) {
		t.Errorf("pack Dir = %q, want absolute path", p.Dir)
	}
	if len(p.Manifests) != 1 {
		t.Fatalf("len(Manifests) = %d, want 1", len(p.Manifests))
	}
	if got, want := p.Manifests[0].Slash, "/example"; got != want {
		t.Errorf("manifest slash = %q, want %q", got, want)
	}
}

func TestDiscoverMissingPacksDirReturnsEmpty(t *testing.T) {
	// A fresh install has no packs directory yet — must not error.
	home := t.TempDir()
	packs, err := Discover(home)
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(packs) != 0 {
		t.Fatalf("Discover returned %d packs from empty home, want 0", len(packs))
	}
}

func TestDiscoverMalformedPackYAMLReportsError(t *testing.T) {
	home := t.TempDir()
	badDir := filepath.Join(home, "packs", "broken")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatalf("creating pack dir: %v", err)
	}
	// Tab indentation + unterminated value triggers a parse error.
	if err := os.WriteFile(
		filepath.Join(badDir, "pack.yaml"),
		[]byte("name: broken\n\tversion: 0.0.1\n"),
		0o644,
	); err != nil {
		t.Fatalf("writing pack.yaml: %v", err)
	}

	packs, err := Discover(home)
	if err == nil {
		t.Fatal("Discover returned nil error for malformed pack.yaml, want error")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error %q does not name the offending pack", err)
	}
	if !strings.Contains(err.Error(), "pack.yaml") {
		t.Errorf("error %q does not name the offending file", err)
	}
	if len(packs) != 0 {
		t.Errorf("Discover returned %d packs alongside the malformed pack, want 0", len(packs))
	}
}

func TestDiscoverMalformedPackYAMLAlongsideValidPack(t *testing.T) {
	// A malformed pack must not prevent its healthy neighbours from being
	// returned: callers can render the error message in the UI while still
	// listing the working packs.
	home := t.TempDir()
	goodDir := filepath.Join(home, "packs", "good")
	if err := os.MkdirAll(goodDir, 0o755); err != nil {
		t.Fatalf("creating pack dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(goodDir, "pack.yaml"),
		[]byte("name: good\nversion: 1.0.0\n"),
		0o644,
	); err != nil {
		t.Fatalf("writing pack.yaml: %v", err)
	}
	badDir := filepath.Join(home, "packs", "broken")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatalf("creating pack dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(badDir, "pack.yaml"),
		[]byte("name: broken\n\tversion: 0.0.1\n"),
		0o644,
	); err != nil {
		t.Fatalf("writing pack.yaml: %v", err)
	}

	packs, err := Discover(home)
	if err == nil {
		t.Fatal("Discover returned nil error, want error for the broken pack")
	}
	if len(packs) != 1 || packs[0].Name != "good" {
		t.Fatalf("Discover packs = %v, want [good]", packs)
	}
}

func TestDiscoverUnknownPackYAMLFieldIsRejected(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "packs", "typo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating pack dir: %v", err)
	}
	// "auther" is a typo of "author"; strict decoding must catch it.
	if err := os.WriteFile(
		filepath.Join(dir, "pack.yaml"),
		[]byte("name: typo\nversion: 0.0.1\nauther: Someone\n"),
		0o644,
	); err != nil {
		t.Fatalf("writing pack.yaml: %v", err)
	}

	_, err := Discover(home)
	if err == nil {
		t.Fatal("Discover accepted unknown pack.yaml field, want strict rejection")
	}
}

func TestDiscoverIgnoresNonDirectoryEntries(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "packs"), 0o755); err != nil {
		t.Fatalf("creating packs dir: %v", err)
	}
	// Stray file at the packs root (e.g. an editor lock) must be skipped,
	// not parsed as a pack.
	if err := os.WriteFile(filepath.Join(home, "packs", ".DS_Store"), []byte{}, 0o644); err != nil {
		t.Fatalf("writing stray file: %v", err)
	}

	packs, err := Discover(home)
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if len(packs) != 0 {
		t.Fatalf("Discover returned %d packs, want 0", len(packs))
	}
}

func TestDiscoverSortsPacksByName(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"zeta", "alpha", "mike"} {
		dir := filepath.Join(home, "packs", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("creating pack dir: %v", err)
		}
		body := "name: " + name + "\nversion: 1.0.0\n"
		if err := os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte(body), 0o644); err != nil {
			t.Fatalf("writing pack.yaml: %v", err)
		}
	}

	packs, err := Discover(home)
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	got := []string{packs[0].Name, packs[1].Name, packs[2].Name}
	want := []string{"alpha", "mike", "zeta"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Discover order = %v, want %v", got, want)
		}
	}
}

func TestDiscoverIOErrorIsWrapped(t *testing.T) {
	// A read error on the packs root that is not ErrNotExist must surface.
	home := t.TempDir()
	// Create a file at the packs path so os.ReadDir returns ENOTDIR.
	if err := os.WriteFile(filepath.Join(home, "packs"), []byte{}, 0o644); err != nil {
		t.Fatalf("writing decoy file: %v", err)
	}

	_, err := Discover(home)
	if err == nil {
		t.Fatal("Discover returned nil error when packs root is a regular file")
	}
	// Sanity: the error should not be ErrNotExist (that path returns nil).
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Discover error = %v, want non-ErrNotExist", err)
	}
}

func TestLoadUserScopeReturnsEmptyPack(t *testing.T) {
	home := t.TempDir()
	p, err := LoadUserScope(home)
	if err != nil {
		t.Fatalf("LoadUserScope error: %v", err)
	}
	if p == nil {
		t.Fatal("LoadUserScope returned nil pack")
	}
	if len(p.Manifests) != 0 {
		t.Errorf("LoadUserScope manifests = %d, want 0", len(p.Manifests))
	}
	if want := filepath.Join(home, "commands"); p.Dir != want {
		t.Errorf("LoadUserScope Dir = %q, want %q", p.Dir, want)
	}
}
