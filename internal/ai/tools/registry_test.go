package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeTool is a Tool stand-in tests use to exercise the registry without
// pulling in a real read/write subpackage (which would create an import
// cycle via the init()-driven registration). Its Permission is the
// permission tier set at construction; its Execute either returns the
// configured value or echoes the args back so call-through tests can assert
// on the round-trip.
type fakeTool struct {
	name string
	perm Permission
	exec func(ctx context.Context, args map[string]any) (any, error)
}

// Name reports the configured name.
func (f *fakeTool) Name() string { return f.name }

// Permission returns the configured tier.
func (f *fakeTool) Permission() Permission { return f.perm }

// Schema returns a minimal schema matching Name().
func (f *fakeTool) Schema() Schema {
	return Schema{
		Name:        f.name,
		Description: "fake tool for tests",
		Parameters:  map[string]any{"type": "object"},
	}
}

// Execute delegates to the configured exec; the default returns the args
// unchanged so callers can assert the dispatch path is plumbed correctly.
func (f *fakeTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	if f.exec != nil {
		return f.exec(ctx, args)
	}
	return args, nil
}

// allowAllGate is the test Gate that lets every write call through. Tests
// that exercise the write path install it via WithGate.
type allowAllGate struct{}

// Allow always returns DecisionApproveOnce.
func (allowAllGate) Allow(_ context.Context, _ ConsentRequest) (Decision, error) {
	return DecisionApproveOnce, nil
}

// TestRegister_RejectsForbiddenName is the compile-time-guarantee test
// ADR-0035 calls for: a tool that claims a name on the hardcoded forbidden
// list cannot be installed in the registry — full stop. The fake tool
// below names itself "iam/CreateUser"; this test exists to fail loudly if
// anyone weakens the forbidden gate.
func TestRegister_RejectsForbiddenName(t *testing.T) {
	r := NewRegistry()
	bad := &fakeTool{name: "iam/CreateUser", perm: PermissionWrite}
	err := r.Register(bad)
	if err == nil {
		t.Fatal("expected forbidden-name registration to fail, got nil")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T: %v", err, err)
	}
	if te.Code != ErrCodeForbidden {
		t.Fatalf("expected ErrCodeForbidden, got %q", te.Code)
	}
	// And the registry must not have stored the tool: a follow-up Lookup
	// returns false, and Call refuses the name even when Lookup would have
	// hit (which it must not).
	if _, ok := r.Lookup("iam/CreateUser"); ok {
		t.Fatal("forbidden tool ended up in the registry despite failed Register")
	}
}

// TestRegister_RejectsForbiddenAliases covers the spellings the LLM (or a
// careless tool author) might use to try sneaking a forbidden tool in:
// SDK-style "iam:CreateUser", uppercase, and the glob-pattern entries.
func TestRegister_RejectsForbiddenAliases(t *testing.T) {
	cases := []string{
		"iam:CreateUser",
		"IAM/CreateUser",
		"iam/createaccesskey",
		"s3/DeleteObject",
		"secretsmanager/DeleteSecret",
		"kms/ScheduleKeyDeletion",
		"cfn/disable-consent",
		"ecs/auto-approve",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			r := NewRegistry()
			err := r.Register(&fakeTool{name: name, perm: PermissionWrite})
			if err == nil {
				t.Fatalf("expected %q to be refused, registration succeeded", name)
			}
			var te *ToolError
			if !errors.As(err, &te) || te.Code != ErrCodeForbidden {
				t.Fatalf("expected ErrCodeForbidden for %q, got %v", name, err)
			}
		})
	}
}

// TestCall_ForbiddenReturnsStructuredError covers the DoD line "A forbidden
// call from a fake AI conversation returns a structured error and is
// logged as a security event." We assert the structured-error part here;
// the log line emission is exercised by TestLogForbiddenAttempt below.
func TestCall_ForbiddenReturnsStructuredError(t *testing.T) {
	r := NewRegistry()
	// Even though no tool is registered under this name, Call must refuse
	// it because IsForbidden runs *before* Lookup. This is the property
	// that makes prompt-injection irrelevant: the LLM cannot ask for a
	// forbidden tool by name and get past the gate, regardless of
	// registration state.
	_, err := r.Call(context.Background(), "iam/CreateUser", map[string]any{"username": "evil"})
	if err == nil {
		t.Fatal("expected forbidden Call to fail")
	}
	var te *ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected *ToolError, got %T: %v", err, err)
	}
	if te.Code != ErrCodeForbidden {
		t.Fatalf("expected ErrCodeForbidden, got %q", te.Code)
	}
	// errors.Is must work on the Code so audit pipelines can branch.
	if !errors.Is(err, &ToolError{Code: ErrCodeForbidden}) {
		t.Fatal("errors.Is(err, ErrCodeForbidden ToolError) returned false")
	}
}

// TestCall_UnknownTool covers the "LLM asked for a tool that doesn't exist"
// path. We get a structured ErrCodeUnknown so the model can adjust.
func TestCall_UnknownTool(t *testing.T) {
	r := NewRegistry()
	_, err := r.Call(context.Background(), "cfn/typo", nil)
	var te *ToolError
	if !errors.As(err, &te) || te.Code != ErrCodeUnknown {
		t.Fatalf("expected ErrCodeUnknown, got %v", err)
	}
}

// TestCall_ReadGoesStraightThrough confirms read tools never touch the
// Gate: even with the default deny-all Gate in place, a read tool runs.
func TestCall_ReadGoesStraightThrough(t *testing.T) {
	r := NewRegistry()
	called := false
	if err := r.Register(&fakeTool{
		name: "cfn/list-stacks",
		perm: PermissionRead,
		exec: func(_ context.Context, _ map[string]any) (any, error) {
			called = true
			return map[string]any{"ok": true}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	out, err := r.Call(context.Background(), "cfn/list-stacks", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !called {
		t.Fatal("Execute was not invoked")
	}
	m, ok := out.(map[string]any)
	if !ok || m["ok"] != true {
		t.Fatalf("unexpected result: %v", out)
	}
}

// TestCall_WriteDeniedByDefault confirms ADR-0035's safety posture: with no
// Gate override and the package-level DefaultGate (denyAll{}), a write
// tool's Execute is never invoked.
func TestCall_WriteDeniedByDefault(t *testing.T) {
	r := NewRegistry()
	called := false
	if err := r.Register(&fakeTool{
		name: "cfn/update-stack",
		perm: PermissionWrite,
		exec: func(_ context.Context, _ map[string]any) (any, error) {
			called = true
			return nil, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	_, err := r.Call(context.Background(), "cfn/update-stack", nil)
	if err == nil {
		t.Fatal("expected ConsentDenied, got nil")
	}
	var te *ToolError
	if !errors.As(err, &te) || te.Code != ErrCodeConsentDenied {
		t.Fatalf("expected ErrCodeConsentDenied, got %v", err)
	}
	if called {
		t.Fatal("Execute ran despite default-deny Gate")
	}
}

// TestCall_WriteAllowedViaContextGate confirms WithGate threads through:
// callers (PR-04, tests) install their own Gate for a single ctx without
// mutating DefaultGate.
func TestCall_WriteAllowedViaContextGate(t *testing.T) {
	r := NewRegistry()
	called := false
	if err := r.Register(&fakeTool{
		name: "cfn/update-stack",
		perm: PermissionWrite,
		exec: func(_ context.Context, _ map[string]any) (any, error) {
			called = true
			return map[string]any{"ok": true}, nil
		},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ctx := WithGate(context.Background(), allowAllGate{})
	if _, err := r.Call(ctx, "cfn/update-stack", nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !called {
		t.Fatal("Execute was not invoked")
	}
}

// TestRegister_RejectsNilAndDuplicates rounds out the registry's input
// validation: nil tools, empty names, and double-registration are all
// surface-level bugs that should fail loudly.
func TestRegister_RejectsNilAndDuplicates(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); err == nil {
		t.Fatal("expected nil-tool to fail")
	}
	if err := r.Register(&fakeTool{name: "", perm: PermissionRead}); err == nil {
		t.Fatal("expected empty-name to fail")
	}
	if err := r.Register(&fakeTool{name: "x/y", perm: PermissionRead}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register(&fakeTool{name: "x/y", perm: PermissionRead}); err == nil {
		t.Fatal("expected duplicate Register to fail")
	}
}

// TestRegister_RejectsSchemaMismatch verifies the schema-name guard catches
// tool authors who update one constant and forget the other.
func TestRegister_RejectsSchemaMismatch(t *testing.T) {
	r := NewRegistry()
	t1 := &mismatchedTool{name: "cfn/list-stacks", schemaName: "cfn/listt-stacks"}
	err := r.Register(t1)
	if err == nil {
		t.Fatal("expected schema mismatch to fail")
	}
	if !strings.Contains(err.Error(), "schema name") {
		t.Fatalf("expected schema mismatch error, got %v", err)
	}
}

// mismatchedTool reports a Schema name that disagrees with Name() — used by
// the schema-mismatch test.
type mismatchedTool struct {
	name       string
	schemaName string
}

// Name reports the configured name.
func (m *mismatchedTool) Name() string { return m.name }

// Permission returns PermissionRead.
func (m *mismatchedTool) Permission() Permission { return PermissionRead }

// Schema returns a Schema whose Name differs from m.Name() so Register
// trips the schema-mismatch guard.
func (m *mismatchedTool) Schema() Schema {
	return Schema{Name: m.schemaName, Description: "mismatched", Parameters: map[string]any{}}
}

// Execute is a no-op; this tool never runs.
func (m *mismatchedTool) Execute(context.Context, map[string]any) (any, error) {
	return nil, nil
}

// TestListByPermission confirms the helper used by PR-02's prompt
// rendering returns only the requested tier and sorts by name.
func TestListByPermission(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"a/read", "b/write", "c/read", "d/write"} {
		perm := PermissionRead
		if strings.HasSuffix(name, "/write") {
			perm = PermissionWrite
		}
		if err := r.Register(&fakeTool{name: name, perm: perm}); err != nil {
			t.Fatalf("Register %q: %v", name, err)
		}
	}
	reads := r.ListByPermission(PermissionRead)
	if len(reads) != 2 || reads[0].Name() != "a/read" || reads[1].Name() != "c/read" {
		t.Fatalf("unexpected reads: %v", names(reads))
	}
	writes := r.ListByPermission(PermissionWrite)
	if len(writes) != 2 || writes[0].Name() != "b/write" || writes[1].Name() != "d/write" {
		t.Fatalf("unexpected writes: %v", names(writes))
	}
}

// names extracts tool names for assertion messages.
func names(ts []Tool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name()
	}
	return out
}
