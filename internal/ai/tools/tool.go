// Package tools is the AI tool catalogue: a typed registry of every action
// the AI may ask Packwright to perform, partitioned into read tools (callable
// without prompting), write tools (callable only after a consent decision
// from the Gate), and a hard-coded forbidden list that nothing can register.
//
// The catalogue exists so the trust boundary is code, not prompt. No amount
// of jailbreaking can promote a read tool to a write tool or convince the
// runtime to call a forbidden one — Permission() is constant per tool, and
// Register refuses forbidden names regardless of how they were spelled.
//
// PR-03 (this package) ships every read tool with a working backing call,
// every write tool gated by Gate (which defaults to "deny everything"), and
// a hardcoded forbidden list. PR-04 replaces the default Gate with the real
// consent modal flow. See ADR-0035 for the full design.
package tools

import (
	"context"
	"errors"
)

// Permission is the trust tier a Tool advertises. Each tool returns a
// constant from its Permission() method — the value cannot vary with
// configuration, environment, or runtime state. The compiler enforces this
// because every implementation in this catalogue literally writes
// `return PermissionRead` or `return PermissionWrite`.
type Permission int

// Permission values. The zero value is intentionally unused so an uninitialised
// Tool cannot accidentally claim a permission tier.
const (
	// PermissionRead names a tool that does not mutate state. Read tools are
	// callable without a consent prompt; every call is still recorded.
	PermissionRead Permission = iota + 1
	// PermissionWrite names a tool that may mutate state (AWS API write,
	// disk write, shell exec, slash-command invocation). Write tools are
	// callable only after Gate.Allow returns a non-deny decision.
	PermissionWrite
)

// String renders the Permission for log lines and error messages.
func (p Permission) String() string {
	switch p {
	case PermissionRead:
		return "read"
	case PermissionWrite:
		return "write"
	default:
		return "unknown"
	}
}

// Schema describes a tool's user-facing surface: its name, a one-line
// description for the LLM, and a JSON-Schema-style parameter map. The
// parameter map is a plain map[string]any so callers can hand it to any
// JSON-Schema-capable LLM SDK verbatim.
type Schema struct {
	// Name is the namespaced tool identifier (e.g. "cfn/describe-stack").
	// Must match Tool.Name() exactly.
	Name string `json:"name"`
	// Description is a short, LLM-readable summary of what the tool does.
	Description string `json:"description"`
	// Parameters is a JSON-Schema object describing the args map the LLM
	// should pass to Execute. Tools that take no parameters set this to a
	// minimal {"type": "object", "properties": {}} document.
	Parameters map[string]any `json:"parameters"`
}

// Tool is the cross-namespace contract every entry in the catalogue
// implements. Concrete implementations live in tools/read/* and tools/write/*
// and register themselves into Default from init().
type Tool interface {
	// Name returns the namespaced identifier (e.g. "cfn/describe-stack").
	// Two tools may not share a name; Register rejects the second one.
	Name() string

	// Permission returns the tool's trust tier. Implementations MUST return
	// a constant — never read config, env, or any package state.
	Permission() Permission

	// Schema returns the LLM-facing description of this tool, including the
	// JSON-Schema for its argument map. Schema.Name must equal Name().
	Schema() Schema

	// Execute runs the tool with the supplied args and returns its result.
	// The returned value is JSON-serialisable; the runtime hands it back to
	// the LLM as the tool's output. Errors are wrapped as *ToolError when
	// they cross the registry boundary so callers can branch on Code.
	Execute(ctx context.Context, args map[string]any) (any, error)
}

// ErrorCode classifies the structured tool errors callers see. Codes are
// stable strings so PR-04's audit log and PR-02's LLM-facing error mapper
// can pattern-match on them without scraping prose.
type ErrorCode string

// Stable error codes returned via *ToolError.
const (
	// ErrCodeForbidden means the tool name matched the hardcoded forbidden
	// list. The call was refused and a security event was logged.
	ErrCodeForbidden ErrorCode = "forbidden"
	// ErrCodeUnknown means no tool with the supplied name is registered.
	ErrCodeUnknown ErrorCode = "unknown_tool"
	// ErrCodeConsentDenied means a write tool's consent Gate denied the
	// call. The AI is told it cannot perform this action.
	ErrCodeConsentDenied ErrorCode = "consent_denied"
	// ErrCodeBadArgs means the args map failed schema validation inside the
	// tool's Execute (missing required field, wrong type, out of range).
	ErrCodeBadArgs ErrorCode = "bad_args"
	// ErrCodeBackend means the underlying API call (AWS, disk, subprocess)
	// returned an error. The cause is wrapped via errors.Unwrap.
	ErrCodeBackend ErrorCode = "backend_error"
	// ErrCodePathEscape means a file/* tool was handed a path that resolved
	// outside $PACKWRIGHT_HOME (either via traversal or a symlink).
	ErrCodePathEscape ErrorCode = "path_escape"
	// ErrCodeMisconfigured means a precondition the tool relies on is
	// missing (no awsx.Client in ctx, no home directory bound, etc.).
	ErrCodeMisconfigured ErrorCode = "misconfigured"
)

// ToolError is the structured error returned across the registry boundary.
// Callers branch on Code; PR-02's LLM-facing layer renders the message back
// to the model verbatim so the AI can adjust its plan.
type ToolError struct {
	// Code is a stable identifier — see the ErrCode* constants.
	Code ErrorCode
	// Tool is the catalogue name of the tool the error pertains to.
	Tool string
	// Message is a human-readable explanation suitable for the LLM.
	Message string
	// Cause is the wrapped underlying error, when one exists.
	Cause error
}

// Error renders the structured error in the form "tool.<name>: <code>: <msg>".
func (e *ToolError) Error() string {
	if e.Tool == "" {
		return "tools: " + string(e.Code) + ": " + e.Message
	}
	return "tools." + e.Tool + ": " + string(e.Code) + ": " + e.Message
}

// Unwrap exposes the wrapped cause for errors.Is / errors.As.
func (e *ToolError) Unwrap() error { return e.Cause }

// Is reports whether target is a *ToolError with the same Code. This lets
// callers write errors.Is(err, &ToolError{Code: ErrCodeForbidden}) without
// caring about the specific Tool or Message.
func (e *ToolError) Is(target error) bool {
	var t *ToolError
	if !errors.As(target, &t) {
		return false
	}
	return t.Code == e.Code
}
