package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestAdd_Local_Symlink covers the DoD requirement: `packs add
// ./local/pack` creates a working symlink into <home>/packs/. On
// systems where symlink creation is disabled by default (Windows),
// the test path falls through to copyTree, which is exercised
// separately by TestAdd_Local_ForceCopy.
func TestAdd_Local_Symlink(t *testing.T) {
	if forceCopy {
		t.Skip("symlink creation gated on this OS; covered by TestAdd_Local_ForceCopy")
	}
	setConsent(t, alwaysTrust)

	home := makeHome(t)
	src := t.TempDir()
	writePackFiles(t, src, map[string]string{
		"pack.yaml":              "name: localpack\nversion: 0.1.0\n",
		"manifests/restart.yaml": shellManifestYAML("/restart", "aws", "ecs", "update-service"),
	})

	meta, err := Add(context.Background(), home, src) // absolute path
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !meta.Local {
		t.Errorf("Local = false; want true")
	}
	if meta.LocalSource != src {
		t.Errorf("LocalSource = %q, want %q", meta.LocalSource, src)
	}

	dest := filepath.Join(home, "packs", meta.Name)
	info, err := os.Lstat(dest)
	if err != nil {
		t.Fatalf("Lstat dest: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected symlink at %q, got mode %v", dest, info.Mode())
	}
	target, err := os.Readlink(dest)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if target != src {
		t.Errorf("Readlink = %q, want %q", target, src)
	}

	// Edits in the source tree are visible through the symlink — the
	// developer-ergonomics property that justifies symlinks over copy.
	writePackFiles(t, src, map[string]string{"new.txt": "hello\n"})
	if _, err := os.Stat(filepath.Join(dest, "new.txt")); err != nil {
		t.Errorf("edit not visible through symlink: %v", err)
	}
}

// TestAdd_Local_ForceCopy exercises the Windows-style fallback path
// — flip forceCopy and verify a real tree is materialised rather
// than a symlink. This lets the CI matrix on Linux/macOS still cover
// the copy code without needing a Windows runner.
func TestAdd_Local_ForceCopy(t *testing.T) {
	setConsent(t, alwaysTrust)

	prev := forceCopy
	forceCopy = true
	t.Cleanup(func() { forceCopy = prev })

	home := makeHome(t)
	src := t.TempDir()
	writePackFiles(t, src, map[string]string{
		"pack.yaml":              "name: copied\nversion: 0.1.0\n",
		"manifests/restart.yaml": shellManifestYAML("/restart", "aws", "ecs", "update-service"),
		"nested/deep/file.txt":   "deep\n",
	})

	if _, err := Add(context.Background(), home, src); err != nil {
		t.Fatalf("Add: %v", err)
	}
	dest := filepath.Join(home, "packs", "copied")
	info, err := os.Lstat(dest)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("expected regular directory, got symlink at %q", dest)
	}
	if !info.IsDir() {
		t.Fatalf("expected directory, got mode %v", info.Mode())
	}

	// Files copied recursively, content preserved.
	got, err := os.ReadFile(filepath.Join(dest, "nested", "deep", "file.txt"))
	if err != nil {
		t.Fatalf("read deep file: %v", err)
	}
	if string(got) != "deep\n" {
		t.Errorf("deep file content = %q, want %q", got, "deep\n")
	}

	// Edits to the source must NOT propagate (this is the trade-off
	// the copy fallback accepts).
	writePackFiles(t, src, map[string]string{"new.txt": "hello\n"})
	if _, err := os.Stat(filepath.Join(dest, "new.txt")); !os.IsNotExist(err) {
		t.Errorf("copy unexpectedly propagated edits: %v", err)
	}
}

// TestAdd_Local_Denied removes both the link/copy and the metadata
// on a denied consent — same posture as the git path.
func TestAdd_Local_Denied(t *testing.T) {
	setConsent(t, alwaysDeny)

	home := makeHome(t)
	src := t.TempDir()
	writePackFiles(t, src, map[string]string{
		"pack.yaml":              "name: denied\nversion: 0.1.0\n",
		"manifests/restart.yaml": shellManifestYAML("/restart", "rm", "-rf", "/"),
	})

	_, err := Add(context.Background(), home, src)
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("Add error = %v, want ErrDenied", err)
	}
	if _, err := os.Stat(filepath.Join(home, "packs", "denied")); !os.IsNotExist(err) {
		t.Errorf("packs/denied still exists after denial: %v", err)
	}
}

// TestAdd_Local_ExplicitRelative ensures `./relative` paths resolve
// against the test's working directory before installation. We
// briefly chdir to the source's parent so a `./<name>` argument
// works.
func TestAdd_Local_ExplicitRelative(t *testing.T) {
	if forceCopy {
		t.Skip("relative path test focuses on symlink behaviour")
	}
	setConsent(t, alwaysTrust)

	home := makeHome(t)
	parent := t.TempDir()
	src := filepath.Join(parent, "pack")
	writePackFiles(t, src, map[string]string{
		"pack.yaml":              "name: rel\nversion: 0.1.0\n",
		"manifests/restart.yaml": shellManifestYAML("/restart", "aws", "ecs", "update-service"),
	})

	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(parent); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prevWD) })

	if _, err := Add(context.Background(), home, "./pack"); err != nil {
		t.Fatalf("Add ./pack: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, "packs", "rel")); err != nil {
		t.Errorf("expected packs/rel: %v", err)
	}
}

// TestUpdate_Local re-hashes a local install on update and does NOT
// invoke git. Mutating the source tree after install and calling
// Update reflects the change in the trusted hash.
func TestUpdate_Local(t *testing.T) {
	if forceCopy {
		t.Skip("local Update relies on the symlink seeing fresh source content")
	}
	setConsent(t, alwaysTrust)

	home := makeHome(t)
	src := t.TempDir()
	writePackFiles(t, src, map[string]string{
		"pack.yaml":              "name: live\nversion: 0.1.0\n",
		"manifests/restart.yaml": shellManifestYAML("/restart", "aws", "ecs", "update-service"),
	})
	meta, err := Add(context.Background(), home, src)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	prevHash := meta.TrustedHash

	writePackFiles(t, src, map[string]string{"README.md": "new readme\n"})
	updated, err := Update(context.Background(), home, "live")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.TrustedHash == prevHash {
		t.Errorf("TrustedHash unchanged after local source edit: %q", prevHash)
	}
}

// TestRemove_LocalSymlinkPreservesSource: removing a symlinked local
// install must delete only the link, never the source tree it
// targets. This guards against `os.RemoveAll` accidentally following
// the symlink (Go's standard library does the right thing; the test
// nails it down so a future refactor cannot regress).
func TestRemove_LocalSymlinkPreservesSource(t *testing.T) {
	if forceCopy {
		t.Skip("only meaningful for the symlink path")
	}
	setConsent(t, alwaysTrust)

	home := makeHome(t)
	src := t.TempDir()
	writePackFiles(t, src, map[string]string{
		"pack.yaml": "name: keep\nversion: 0.1.0\n",
		"marker":    "do-not-delete\n",
	})
	if _, err := Add(context.Background(), home, src); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := Remove(home, "keep"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "marker")); err != nil {
		t.Fatalf("source tree damaged by Remove: %v", err)
	}
}
