package write

import (
	"context"
	"errors"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// RunCommandHandler is the callback the runtime wires up to actually invoke
// a slash command on the user's behalf. Keeping it as a package-level
// function variable avoids an import cycle: the slash-command dispatcher
// lives outside internal/ai/tools and would otherwise have to import this
// package and vice versa.
//
// PR-04 (consent flow) and the cmd-wiring PR populate this variable from
// init(); until then the default returns ErrRunCommandUnwired so the AI
// learns the action is unavailable rather than getting a panic.
type RunCommandHandler func(ctx context.Context, slash string, inputs map[string]any) (any, error)

// ErrRunCommandUnwired is the sentinel returned by the default
// RunCommandHandler. Callers can errors.Is(err, ErrRunCommandUnwired) to
// detect that the host hasn't registered a handler.
var ErrRunCommandUnwired = errors.New("ai tools: packwright/run-command handler is not wired up")

// runCommand is the package-level handler. The cmd-wiring PR replaces it
// with the real dispatcher; tests can also swap it via t.Cleanup.
var runCommand RunCommandHandler = func(context.Context, string, map[string]any) (any, error) {
	return nil, ErrRunCommandUnwired
}

// SetRunCommandHandler installs the host's run-command implementation. The
// previous handler is returned so callers can restore it (PR-04 swaps
// during setup; tests restore in cleanup).
func SetRunCommandHandler(h RunCommandHandler) RunCommandHandler {
	prev := runCommand
	if h == nil {
		runCommand = func(context.Context, string, map[string]any) (any, error) {
			return nil, ErrRunCommandUnwired
		}
	} else {
		runCommand = h
	}
	return prev
}

// runCommandTool is the catalogue entry for packwright/run-command. It calls
// the handler set via SetRunCommandHandler — the dispatcher applies its own
// per-command consent (form rendering, scope checks) on top of the consent
// the AI catalogue already enforced.
type runCommandTool struct{}

// Name reports the catalogue name.
func (runCommandTool) Name() string { return "packwright/run-command" }

// Permission returns the const PermissionWrite.
func (runCommandTool) Permission() tools.Permission { return tools.PermissionWrite }

// Schema declares the args.
func (t runCommandTool) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Invoke an existing Packwright /slash command on the user's behalf with the supplied form inputs. The command's own consent applies on top of the catalogue's.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slash":  map[string]any{"type": "string", "description": "Slash command identifier (e.g. \"/alb-create\"). Leading slash is optional."},
				"inputs": map[string]any{"type": "object", "description": "Form values to pass to the command."},
				"reason": map[string]any{"type": "string", "description": "Why this command is being run."},
			},
			"required": []string{"slash", "reason"},
		},
	}
}

// Execute forwards to the registered RunCommandHandler.
func (t runCommandTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	slash, err := tools.ArgString(t.Name(), args, "slash", true)
	if err != nil {
		return nil, err
	}
	if _, err := tools.ArgString(t.Name(), args, "reason", true); err != nil {
		return nil, err
	}
	inputs, err := tools.ArgMap(t.Name(), args, "inputs", false)
	if err != nil {
		return nil, err
	}
	out, err := runCommand(ctx, slash, inputs)
	if err != nil {
		if errors.Is(err, ErrRunCommandUnwired) {
			return nil, &tools.ToolError{
				Code: tools.ErrCodeMisconfigured, Tool: t.Name(),
				Message: err.Error(),
				Cause:   err,
			}
		}
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBackend, Tool: t.Name(),
			Message: err.Error(),
			Cause:   err,
		}
	}
	return map[string]any{"result": out}, nil
}

func init() {
	tools.MustRegister(tools.Default, runCommandTool{})
}
