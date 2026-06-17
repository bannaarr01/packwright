package write

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// TestSandboxPath_BlocksTraversal mirrors the read-side check: the write
// sandbox must refuse ../ traversal at least as aggressively as the read
// one, because the blast radius of a sandbox-escape on file/write is much
// worse than on file/read.
func TestSandboxPath_BlocksTraversal(t *testing.T) {
	home := t.TempDir()
	for _, c := range []string{"../etc/passwd", "../../etc/passwd", "x/../../etc/y"} {
		t.Run(c, func(t *testing.T) {
			_, err := sandboxPath("file/write", home, c)
			var te *tools.ToolError
			if !errors.As(err, &te) || te.Code != tools.ErrCodePathEscape {
				t.Fatalf("expected ErrCodePathEscape, got %v", err)
			}
		})
	}
}

// TestSandboxPath_BlocksSymlinkParentEscape exercises the write-time check
// where the file itself doesn't exist yet but its parent directory is a
// symlink pointing outside the sandbox. file/write would otherwise happily
// create the file in the outside directory.
func TestSandboxPath_BlocksSymlinkParentEscape(t *testing.T) {
	home := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(home, "escape-dir")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	_, err := sandboxPath("file/write", home, "escape-dir/new-file")
	if err == nil {
		t.Fatal("expected write through symlinked parent to be refused")
	}
}

// TestSandboxPath_AllowsInsideHome rounds out the happy-path so a refactor
// that tightens the rules can't accidentally also break in-sandbox writes.
func TestSandboxPath_AllowsInsideHome(t *testing.T) {
	home := t.TempDir()
	got, err := sandboxPath("file/write", home, "sub/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := filepath.Join(home, "sub", "file.txt"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
