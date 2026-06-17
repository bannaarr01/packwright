package tools

import (
	"encoding/json"
	"fmt"
)

// argString reads a string-valued argument from args. When required is true
// and the key is absent or empty, it returns a structured ErrCodeBadArgs
// *ToolError naming both the tool and the missing field. When required is
// false, an absent or empty value is returned as ("", nil).
//
// Non-string values that can be JSON-rendered as strings (numbers, bools)
// are accepted via Sprint to keep the LLM's call-site forgiving; arrays
// and objects are not — the LLM should re-issue the call with the right
// type rather than ship a stringified blob.
func ArgString(toolName string, args map[string]any, key string, required bool) (string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		if required {
			return "", &ToolError{
				Code: ErrCodeBadArgs, Tool: toolName,
				Message: fmt.Sprintf("missing required string argument %q", key),
			}
		}
		return "", nil
	}
	switch t := v.(type) {
	case string:
		if t == "" && required {
			return "", &ToolError{
				Code: ErrCodeBadArgs, Tool: toolName,
				Message: fmt.Sprintf("argument %q is empty", key),
			}
		}
		return t, nil
	case json.Number:
		return t.String(), nil
	case float64, float32, int, int32, int64, bool:
		return fmt.Sprint(t), nil
	default:
		return "", &ToolError{
			Code: ErrCodeBadArgs, Tool: toolName,
			Message: fmt.Sprintf("argument %q must be a string, got %T", key, v),
		}
	}
}

// ArgInt reads an integer-valued argument from args. The LLM may legitimately
// send either a JSON number or a numeric string (when it formats args via a
// template), so both shapes are accepted.
func ArgInt(toolName string, args map[string]any, key string, required bool) (int, error) {
	v, ok := args[key]
	if !ok || v == nil {
		if required {
			return 0, &ToolError{
				Code: ErrCodeBadArgs, Tool: toolName,
				Message: fmt.Sprintf("missing required integer argument %q", key),
			}
		}
		return 0, nil
	}
	switch t := v.(type) {
	case int:
		return t, nil
	case int32:
		return int(t), nil
	case int64:
		return int(t), nil
	case float64:
		return int(t), nil
	case float32:
		return int(t), nil
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return 0, &ToolError{
				Code: ErrCodeBadArgs, Tool: toolName,
				Message: fmt.Sprintf("argument %q is not a valid integer: %v", key, err),
			}
		}
		return int(n), nil
	case string:
		var n int
		if _, err := fmt.Sscanf(t, "%d", &n); err != nil {
			return 0, &ToolError{
				Code: ErrCodeBadArgs, Tool: toolName,
				Message: fmt.Sprintf("argument %q is not a valid integer: %q", key, t),
			}
		}
		return n, nil
	default:
		return 0, &ToolError{
			Code: ErrCodeBadArgs, Tool: toolName,
			Message: fmt.Sprintf("argument %q must be an integer, got %T", key, v),
		}
	}
}

// ArgStringSlice reads a []string argument from args. JSON arrays come in
// as []any so we widen the element types as we go; a JSON-string value
// containing a single token is also accepted for LLM ergonomics.
func ArgStringSlice(toolName string, args map[string]any, key string, required bool) ([]string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		if required {
			return nil, &ToolError{
				Code: ErrCodeBadArgs, Tool: toolName,
				Message: fmt.Sprintf("missing required string-array argument %q", key),
			}
		}
		return nil, nil
	}
	switch t := v.(type) {
	case []string:
		return append([]string(nil), t...), nil
	case []any:
		out := make([]string, 0, len(t))
		for i, el := range t {
			s, ok := el.(string)
			if !ok {
				return nil, &ToolError{
					Code: ErrCodeBadArgs, Tool: toolName,
					Message: fmt.Sprintf("argument %q[%d] must be a string, got %T", key, i, el),
				}
			}
			out = append(out, s)
		}
		return out, nil
	case string:
		if t == "" {
			return nil, nil
		}
		return []string{t}, nil
	default:
		return nil, &ToolError{
			Code: ErrCodeBadArgs, Tool: toolName,
			Message: fmt.Sprintf("argument %q must be a string array, got %T", key, v),
		}
	}
}

// ArgMap reads a map[string]any argument from args. Used by tools that
// accept arbitrary nested structures (cfn parameter overrides, ecs task
// definitions). A missing optional argument returns (nil, nil).
func ArgMap(toolName string, args map[string]any, key string, required bool) (map[string]any, error) {
	v, ok := args[key]
	if !ok || v == nil {
		if required {
			return nil, &ToolError{
				Code: ErrCodeBadArgs, Tool: toolName,
				Message: fmt.Sprintf("missing required object argument %q", key),
			}
		}
		return nil, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, &ToolError{
			Code: ErrCodeBadArgs, Tool: toolName,
			Message: fmt.Sprintf("argument %q must be an object, got %T", key, v),
		}
	}
	return m, nil
}
