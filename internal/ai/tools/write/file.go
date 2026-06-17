package write

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// fileWrite writes (or overwrites) a file under $PACKWRIGHT_HOME with the
// supplied content. The consent modal renders a unified diff for existing
// files — PR-04 implements that; the tool here just performs the write once
// the Gate has approved.
type fileWrite struct{}

// Name reports the catalogue name.
func (fileWrite) Name() string { return "file/write" }

// Permission returns the const PermissionWrite.
func (fileWrite) Permission() tools.Permission { return tools.PermissionWrite }

// Schema declares the args.
func (t fileWrite) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Write a UTF-8 file under $PACKWRIGHT_HOME. Creates parent directories as needed. Existing files are overwritten only after the consent modal approves the diff.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Path relative to $PACKWRIGHT_HOME."},
				"content": map[string]any{"type": "string", "description": "Full file contents."},
				"reason":  map[string]any{"type": "string", "description": "Why the file is being written — surfaced in the consent modal."},
			},
			"required": []string{"path", "content", "reason"},
		},
	}
}

// Execute writes the file (after the registry has consulted the consent Gate).
func (t fileWrite) Execute(ctx context.Context, args map[string]any) (any, error) {
	rel, err := tools.ArgString(t.Name(), args, "path", true)
	if err != nil {
		return nil, err
	}
	// content is required, but an empty file is legitimate — accept the key
	// when it is present and a string, even if the string itself is empty.
	contentRaw, ok := args["content"]
	if !ok || contentRaw == nil {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBadArgs, Tool: t.Name(),
			Message: "missing required string argument \"content\"",
		}
	}
	content, ok := contentRaw.(string)
	if !ok {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBadArgs, Tool: t.Name(),
			Message: "argument \"content\" must be a string",
		}
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
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBackend, Tool: t.Name(),
			Message: fmt.Sprintf("mkdir -p %q: %v", filepath.Dir(rel), err),
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

// fileDelete removes a file under $PACKWRIGHT_HOME.
type fileDelete struct{}

// Name reports the catalogue name.
func (fileDelete) Name() string { return "file/delete" }

// Permission returns the const PermissionWrite.
func (fileDelete) Permission() tools.Permission { return tools.PermissionWrite }

// Schema declares the args.
func (t fileDelete) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Delete a file under $PACKWRIGHT_HOME. Directories are refused — pass a single file path.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "Path relative to $PACKWRIGHT_HOME."},
				"reason": map[string]any{"type": "string", "description": "Why the file is being deleted."},
			},
			"required": []string{"path", "reason"},
		},
	}
}

// Execute removes the resolved file.
func (t fileDelete) Execute(ctx context.Context, args map[string]any) (any, error) {
	rel, err := tools.ArgString(t.Name(), args, "path", true)
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
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBackend, Tool: t.Name(),
			Message: fmt.Sprintf("stat %q: %v", rel, err),
			Cause:   err,
		}
	}
	if info.IsDir() {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBadArgs, Tool: t.Name(),
			Message: fmt.Sprintf("path %q is a directory; file/delete only removes single files", rel),
		}
	}
	if err := os.Remove(abs); err != nil {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBackend, Tool: t.Name(),
			Message: fmt.Sprintf("remove %q: %v", rel, err),
			Cause:   err,
		}
	}
	return map[string]any{"deleted": rel}, nil
}

func init() {
	tools.MustRegister(tools.Default, fileWrite{})
	tools.MustRegister(tools.Default, fileDelete{})
}
