package write

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// sandboxPath resolves rel against the home root, refusing any path that
// escapes the root via traversal or absolute components, and any path whose
// existing target resolves through a symlink to outside the sandbox.
//
// See read/sandbox.go for the matching read-side helper — the two are kept
// in lock-step because the sandbox rules are part of ADR-0035, and any
// divergence would create a write-only escape hatch the read tools cannot
// observe.
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
	// Walk up the path until we hit something that exists, then resolve
	// symlinks and confirm the result still lives under home. This catches
	// "parent is a symlink pointing outside" cases the leaf-only check
	// misses when the file doesn't exist yet (file/write).
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

// pathWithin reports whether candidate equals root or sits beneath it. Both
// inputs must be cleaned absolute paths.
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
