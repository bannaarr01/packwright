package read

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// TestSandboxPath_BlocksTraversal asserts ../ traversal is rejected. The
// sandbox is the entire safety story for file/read — if a path escapes
// $PACKWRIGHT_HOME, the AI can read anything on disk regardless of the
// LLM's intent.
func TestSandboxPath_BlocksTraversal(t *testing.T) {
	home := t.TempDir()
	cases := []string{
		"../etc/passwd",
		"../../etc/passwd",
		"subdir/../../../etc/passwd",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			_, err := sandboxPath("file/read", home, c)
			var te *tools.ToolError
			if !errors.As(err, &te) || te.Code != tools.ErrCodePathEscape {
				t.Fatalf("expected ErrCodePathEscape for %q, got %v", c, err)
			}
		})
	}
}

// TestSandboxPath_BlocksAbsolutePath asserts that an absolute path supplied
// as "rel" still resolves under the home root (because filepath.Join
// silently absorbs it on Unix). The literal /etc/passwd becomes
// $HOME/etc/passwd — within sandbox — and either reads a non-existent file
// or returns no content. Either way it cannot reach /etc/passwd directly.
func TestSandboxPath_AbsolutePathContained(t *testing.T) {
	home := t.TempDir()
	got, err := sandboxPath("file/read", home, "/etc/passwd")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !filepath.HasPrefix(got, home) {
		t.Fatalf("resolved path %q escaped home %q", got, home)
	}
}

// TestSandboxPath_BlocksSymlinkEscape covers the EvalSymlinks check: a
// symlink under home that points outside must be refused.
func TestSandboxPath_BlocksSymlinkEscape(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir() // a directory NOT under home

	link := filepath.Join(home, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported on this filesystem: %v", err)
	}

	_, err := sandboxPath("file/read", home, "escape/foo")
	if err == nil {
		t.Fatal("expected sandbox escape via symlink to be refused")
	}
}

// TestSandboxPath_AllowsInsideHome covers the happy path: a clean
// home-relative path resolves to home/rel and returns no error.
func TestSandboxPath_AllowsInsideHome(t *testing.T) {
	home := t.TempDir()
	got, err := sandboxPath("file/read", home, "subdir/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(home, "subdir", "file.txt")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
