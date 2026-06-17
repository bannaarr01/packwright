package write

import (
	"context"

	"github.com/bannaarr01/packwright/action"
	"github.com/bannaarr01/packwright/internal/action/shell"
	"github.com/bannaarr01/packwright/internal/ai/tools"
	"github.com/bannaarr01/packwright/manifest"
)

// shellRunner is a function variable so tests can substitute a no-op runner.
// In production it builds a fresh internal/action/shell.Runner per call —
// the runner is stateless past its Spec so a per-call construction is fine
// and keeps shell tools free of package-level mutable state.
var shellRunner = func(spec *shell.Spec) action.Runner {
	return &shell.Runner{Spec: spec}
}

// shellExec runs a single shell command via internal/action/shell.Runner.
// It always returns PermissionWrite — even a "harmless" command can mutate
// state (touch a file, mutate the environment, exec a binary that calls
// AWS), so per ADR-0035 shell is unconditionally a write tool.
type shellExec struct{}

// Name reports the catalogue name.
func (shellExec) Name() string { return "shell/exec" }

// Permission returns the const PermissionWrite.
func (shellExec) Permission() tools.Permission { return tools.PermissionWrite }

// Schema declares the args.
func (t shellExec) Schema() tools.Schema {
	return tools.Schema{
		Name:        t.Name(),
		Description: "Run a shell command via the internal/action/shell runner. Defaults to array form (exec.Command); pass shell=\"bash\" to invoke through bash -c.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Command + args (array form), or a single bash command line when shell == \"bash\".",
				},
				"shell": map[string]any{
					"type":        "string",
					"description": "Empty for array form. \"bash\" to invoke through bash -c (command must hold exactly one element).",
				},
				"env": map[string]any{
					"type":        "object",
					"description": "Environment overrides (map of name -> value).",
				},
				"reason": map[string]any{"type": "string", "description": "Why the command is being run — surfaced in the consent modal."},
			},
			"required": []string{"command", "reason"},
		},
	}
}

// Execute runs the shell command and returns stdout / stderr / exit code.
func (t shellExec) Execute(ctx context.Context, args map[string]any) (any, error) {
	cmd, err := tools.ArgStringSlice(t.Name(), args, "command", true)
	if err != nil {
		return nil, err
	}
	if len(cmd) == 0 {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBadArgs, Tool: t.Name(),
			Message: "command is empty",
		}
	}
	shellMode, err := tools.ArgString(t.Name(), args, "shell", false)
	if err != nil {
		return nil, err
	}
	if _, err := tools.ArgString(t.Name(), args, "reason", true); err != nil {
		return nil, err
	}
	envRaw, err := tools.ArgMap(t.Name(), args, "env", false)
	if err != nil {
		return nil, err
	}
	env := make(map[string]string, len(envRaw))
	for k, v := range envRaw {
		s, ok := v.(string)
		if !ok {
			return nil, &tools.ToolError{
				Code: tools.ErrCodeBadArgs, Tool: t.Name(),
				Message: "env[" + k + "] must be a string",
			}
		}
		env[k] = s
	}

	r := shellRunner(&shell.Spec{
		Command: cmd,
		Shell:   shellMode,
		Env:     env,
	})
	res, err := r.Run(ctx, &manifest.Manifest{Kind: manifest.KindShell}, action.Inputs{})
	if err != nil {
		return nil, &tools.ToolError{
			Code: tools.ErrCodeBackend, Tool: t.Name(),
			Message: err.Error(),
			Cause:   err,
		}
	}
	shellResult, _ := res.Value.(*shell.Result)
	out := map[string]any{}
	if shellResult != nil {
		out["exit_code"] = shellResult.ExitCode
		out["stdout"] = shellResult.Stdout
		out["stderr"] = shellResult.Stderr
	}
	return out, nil
}

func init() {
	tools.MustRegister(tools.Default, shellExec{})
}
