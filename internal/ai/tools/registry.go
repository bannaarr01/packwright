package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Registry indexes the tool catalogue by canonical name. The same registry
// services every namespace: lookups, listing, and the gated Call entry
// point that read/* and write/* tools delegate consent through.
//
// Registry is safe for concurrent use. Register typically runs from init()
// and Call is invoked from the LLM-dispatch loop; both go through the
// same RWMutex.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry returns an empty Registry. Most callers reuse the package-
// level Default; constructors exist so tests can build isolated registries.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Default is the package-level Registry every built-in tool registers
// into from its init(). PR-02 (the LLM dispatch loop) consumes Default.
var Default = NewRegistry()

// Register installs t in r, returning an error when:
//
//   - t is nil;
//   - t.Name() is empty or differs from t.Schema().Name (catches typos
//     where a tool author updated one constant but not the other);
//   - t.Name() matches the hardcoded forbidden list — registration is
//     refused with ErrCodeForbidden and a security event is logged;
//   - another tool is already registered under the same canonical name.
//
// Registration errors are returned (not panicked) so a builtin-tools
// init() can choose how to handle them — typically Must(Register(...))
// to fail fast, but tests can register-and-assert without crashing the
// process.
func (r *Registry) Register(t Tool) error {
	if t == nil {
		return &ToolError{Code: ErrCodeBadArgs, Message: "tool is nil"}
	}
	name := t.Name()
	if name == "" {
		return &ToolError{Code: ErrCodeBadArgs, Message: "tool name is empty"}
	}
	if s := t.Schema(); s.Name != name {
		return &ToolError{
			Code: ErrCodeBadArgs, Tool: name,
			Message: fmt.Sprintf("schema name %q does not match tool name %q", s.Name, name),
		}
	}
	if IsForbidden(name) {
		// Log here as well as in Call: registration of a forbidden name is
		// itself suspicious (it shouldn't happen — every builtin tool is
		// audited against the forbidden list in code review), and we want
		// the trail.
		logForbiddenAttempt(context.Background(), name, nil)
		return &ToolError{
			Code: ErrCodeForbidden, Tool: name,
			Message: "tool name is on the hardcoded forbidden list and may not be registered",
		}
	}

	canon := normaliseName(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[canon]; exists {
		return &ToolError{
			Code: ErrCodeBadArgs, Tool: name,
			Message: "tool name is already registered",
		}
	}
	r.tools[canon] = t
	return nil
}

// MustRegister is the panic-on-error wrapper Register helpers in
// read/write subpackages use from init(). A registration error at init
// time is a programming error (duplicate name, forbidden name, mismatched
// schema) — every one of which we want to surface loudly during the test
// suite rather than silently dropping a tool.
func MustRegister(r *Registry, t Tool) {
	if err := r.Register(t); err != nil {
		panic(fmt.Sprintf("tools: MustRegister: %v", err))
	}
}

// Lookup returns the registered Tool for name and true, or (nil, false)
// when no such tool exists. The lookup is case-insensitive and treats
// ':' and '/' as the same separator (normaliseName); the LLM may use
// whichever spelling it likes.
func (r *Registry) Lookup(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[normaliseName(name)]
	return t, ok
}

// List returns every registered tool in name-sorted order. The returned
// slice is freshly allocated so callers may sort or filter it without
// affecting other readers.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// ListByPermission returns every tool whose Permission() equals p, in
// name-sorted order. Callers use this to render the LLM's tool-use prompt
// (read tools always shown, write tools shown only when the AI panel is
// in write-capable mode).
func (r *Registry) ListByPermission(p Permission) []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		if t.Permission() == p {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Call is the single entry point the LLM dispatch loop uses to invoke a
// tool by name. It enforces the three boundaries the catalogue exists to
// guarantee:
//
//  1. Forbidden names are refused regardless of registration state. A
//     forbidden call from a malformed prompt or a bug-injected name never
//     reaches Execute, and the attempt is logged as a security event so
//     operators have a trail.
//  2. Unknown names get a structured ErrCodeUnknown error the LLM can
//     reason about ("I tried to call cfn/typo, that tool doesn't exist").
//  3. Write tools are gated through the consent Gate. A deny is reported
//     as ErrCodeConsentDenied so the LLM can branch on the outcome and,
//     per ADR-0036, ask the user to perform the action manually.
//
// On success, Call returns whatever the tool's Execute returned. On
// failure, the error is always a *ToolError so callers can branch on
// Code without scraping prose.
func (r *Registry) Call(ctx context.Context, name string, args map[string]any) (any, error) {
	if IsForbidden(name) {
		logForbiddenAttempt(ctx, name, args)
		return nil, &ToolError{
			Code: ErrCodeForbidden, Tool: name,
			Message: "tool is on the hardcoded forbidden list; refusal logged as a security event",
		}
	}

	t, ok := r.Lookup(name)
	if !ok {
		return nil, &ToolError{
			Code: ErrCodeUnknown, Tool: name,
			Message: "no tool registered under that name",
		}
	}

	if t.Permission() == PermissionWrite {
		gate := resolveGate(ctx)
		decision, err := gate.Allow(ctx, ConsentRequest{
			Tool:       t.Name(),
			Permission: t.Permission(),
			Args:       args,
		})
		if err != nil || decision == DecisionDeny {
			cause := err
			if cause == nil {
				cause = ErrConsentDenied
			}
			return nil, &ToolError{
				Code: ErrCodeConsentDenied, Tool: t.Name(),
				Message: "write tool refused by consent gate",
				Cause:   cause,
			}
		}
	}

	res, err := t.Execute(ctx, args)
	if err != nil {
		// Preserve already-structured tool errors verbatim so callers can
		// branch on their Code; wrap anything else as ErrCodeBackend.
		var te *ToolError
		if errors.As(err, &te) {
			return nil, te
		}
		return nil, &ToolError{
			Code: ErrCodeBackend, Tool: t.Name(),
			Message: err.Error(),
			Cause:   err,
		}
	}
	return res, nil
}
