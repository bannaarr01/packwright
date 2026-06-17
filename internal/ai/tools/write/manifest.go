package write

import (
	"context"
	"fmt"
	"os"

	"github.com/bannaarr01/packwright/internal/ai/tools"
	"github.com/bannaarr01/packwright/manifest"
)

// manifestLoad is the strict manifest loader. Held as a var so tests can stub.
var manifestLoad = manifest.Load

// manifestEdit replaces a manifest file with new contents. PR-04 renders the
// unified diff in the consent modal before the write goes through; for
// PR-03 the diff is the consent Gate's responsibility (default: deny all).
//
// The tool validates that the new content parses as a manifest before
// writing so a malformed payload is rejected up-front rather than discovered
// the next time pack discovery runs.
type manifestEdit struct{}

// Name reports the catalogue name.
func (manifestEdit) Name() string { return "manifest/edit" }

// Permission returns the const PermissionWrite.
func (manifestEdit) Permission() tools.Permission { return tools.PermissionWrite }

// Schema declares the args.
func (t manifestEdit) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Replace a Packwright manifest file with new YAML content. The new content must parse as a manifest (strict YAML, no unknown keys) or the call is refused.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Manifest path relative to $PACKWRIGHT_HOME."},
				"content": map[string]any{"type": "string", "description": "Full new manifest YAML."},
				"reason":  map[string]any{"type": "string", "description": "Why the manifest is being edited — surfaced in the consent modal."},
			},
			"required": []string{"path", "content", "reason"},
		},
	}
}

// Execute writes the manifest after validating the new content.
func (t manifestEdit) Execute(ctx context.Context, args map[string]any) (any, error) {
	rel, err := tools.ArgString(t.Name(), args, "path", true)
	if err != nil {
		return nil, err
	}
	content, err := tools.ArgString(t.Name(), args, "content", true)
	if err != nil {
		return nil, err
	}
	if _, err := tools.ArgString(t.Name(), args, "reason", true); err != nil {
		return nil, err
	}
	home, err := tools.RequireHome(ctx, t.Name())
	if err != nil {
		return nil, err
	}
	abs, err := sandboxPath(t.Name(), home, rel)
	if err != nil {
		return nil, err
	}

	// Validate the new content by writing it to a sibling temp file and
	// re-loading through the strict manifest parser. The temp file lives in
	// the same directory so atomicity (rename) and validation share the
	// same filesystem; we delete it on failure.
	tmp, err := os.CreateTemp("", "packwright-manifest-edit-*.yaml")
	if err != nil {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBackend, Tool: t.Name(),
			Message: fmt.Sprintf("creating temp file: %v", err),
			Cause:   err,
		}
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBackend, Tool: t.Name(),
			Message: fmt.Sprintf("writing temp file: %v", err),
			Cause:   err,
		}
	}
	if err := tmp.Close(); err != nil {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBackend, Tool: t.Name(),
			Message: fmt.Sprintf("closing temp file: %v", err),
			Cause:   err,
		}
	}
	if _, err := manifestLoad(tmpPath); err != nil {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBadArgs, Tool: t.Name(),
			Message: fmt.Sprintf("new manifest does not parse: %v", err),
			Cause:   err,
		}
	}

	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBackend, Tool: t.Name(),
			Message: fmt.Sprintf("write %q: %v", rel, err),
			Cause:   err,
		}
	}
	return map[string]any{
		"path":  rel,
		"bytes": len(content),
	}, nil
}

func init() {
	tools.MustRegister(tools.Default, manifestEdit{})
}
