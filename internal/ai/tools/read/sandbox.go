package read

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// sandboxPath resolves rel against the home root, refusing any path that
// escapes the root via traversal (..) or absolute components. Symlink escape
// is blocked by passing the result through filepath.EvalSymlinks: if the
// fully-resolved path no longer lives under home, ErrCodePathEscape is
// returned.
//
// The empty rel is treated as the home root itself (callers that want a
// listing of $HOME pass "").
func sandboxPath(toolName, home, rel string) (string, error) {
	if home == "" {
		return "", &tools.ToolError{
			Code: tools.ErrCodeMisconfigured, Tool: toolName,
			Message: "no $PACKWRIGHT_HOME bound to context",
		}
	}
	absHome, err := filepath.Abs(home)
	if err != nil {
		return "", &tools.ToolError{
			Code: tools.ErrCodeMisconfigured, Tool: toolName,
			Message: fmt.Sprintf("home directory is not absolute: %v", err),
			Cause:   err,
		}
	}
	cleaned := filepath.Clean(filepath.Join(absHome, rel))
	if !pathWithin(absHome, cleaned) {
		return "", &tools.ToolError{
			Code: tools.ErrCodePathEscape, Tool: toolName,
			Message: fmt.Sprintf("path %q resolves outside the $PACKWRIGHT_HOME sandbox", rel),
		}
	}
	// Symlink-escape check: walk up from cleaned until we find a path that
	// exists, EvalSymlinks it, and confirm the result still lives under home.
	// This catches both "file is a symlink pointing out" and "the parent
	// directory is a symlink pointing out" — the latter would otherwise slip
	// past a naive EvalSymlinks(cleaned) when the leaf doesn't yet exist.
	probe := cleaned
	for probe != "" && probe != absHome {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			if !pathWithin(absHome, resolved) {
				return "", &tools.ToolError{
					Code: tools.ErrCodePathEscape, Tool: toolName,
					Message: fmt.Sprintf("path %q resolves through a symlink to outside the sandbox", rel),
				}
			}
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	return cleaned, nil
}

// pathWithin reports whether candidate is the same path as root or lives
// strictly beneath it. Both arguments must be cleaned absolute paths.
func pathWithin(root, candidate string) bool {
	if root == candidate {
		return true
	}
	prefix := root
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(candidate, prefix)
}
