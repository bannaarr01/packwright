package read

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// fileReadMaxBytes caps how much of a file the read tool returns. The AI
// rarely needs more than this; a multi-megabyte read would blow out the LLM
// context for no benefit.
const fileReadMaxBytes = 256 * 1024

// fileRead returns the contents of a file living under $PACKWRIGHT_HOME.
type fileRead struct{}

// Name reports the catalogue name.
func (fileRead) Name() string { return "file/read" }

// Permission returns the const PermissionRead.
func (fileRead) Permission() tools.Permission { return tools.PermissionRead }

// Schema declares the args.
func (t fileRead) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Read a UTF-8 file under $PACKWRIGHT_HOME. Output is byte-capped at 256 KB; truncated reads return truncated=true.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path relative to $PACKWRIGHT_HOME. Absolute paths and traversal (..) are refused.",
				},
			},
			"required": []string{"path"},
		},
	}
}

// Execute opens the resolved path and returns its contents (capped).
func (t fileRead) Execute(ctx context.Context, args map[string]any) (any, error) {
	rel, err := tools.ArgString(t.Name(), args, "path", true)
	if err != nil {
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
			Message: fmt.Sprintf("path %q is a directory, not a file", rel),
		}
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBackend, Tool: t.Name(),
			Message: fmt.Sprintf("open %q: %v", rel, err),
			Cause:   err,
		}
	}
	defer f.Close()
	buf := make([]byte, fileReadMaxBytes+1)
	n, err := readFull(f, buf)
	if err != nil {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBackend, Tool: t.Name(),
			Message: fmt.Sprintf("read %q: %v", rel, err),
			Cause:   err,
		}
	}
	truncated := n > fileReadMaxBytes
	if truncated {
		n = fileReadMaxBytes
	}
	return map[string]any{
		"path":      rel,
		"size":      info.Size(),
		"truncated": truncated,
		"content":   string(buf[:n]),
	}, nil
}

// readFull reads from r into buf, ignoring io.EOF — n is the byte count read,
// which the caller compares to len(buf) to decide whether the file was
// truncated. A non-EOF error propagates.
func readFull(r io.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			if errors.Is(err, io.EOF) {
				return total, nil
			}
			return total, err
		}
	}
	return total, nil
}

func init() {
	tools.MustRegister(tools.Default, fileRead{})
}
