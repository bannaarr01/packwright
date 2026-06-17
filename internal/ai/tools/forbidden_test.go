package tools

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	pwlog "github.com/bannaarr01/packwright/log"
)

// TestIsForbidden_NormalisationCases exercises the colon→slash and
// case-fold normalisation: every entry here must trip IsForbidden because
// it points at the same forbidden tool the ADR-0035 list names, just spelled
// differently. Regression-friendly: if anyone widens normaliseName, new
// equivalent spellings can be added here without rewriting the body.
func TestIsForbidden_NormalisationCases(t *testing.T) {
	cases := []struct {
		name      string
		forbidden bool
	}{
		// Exact-list entries.
		{"iam/CreateUser", true},
		{"iam:CreateUser", true},
		{"IAM/CreateUser", true},
		{"  iam/CreateUser  ", true}, // whitespace trim
		{"s3/DeleteObject", true},
		{"s3:DeleteBucket", true},
		// Glob entries.
		{"secretsmanager/DeleteSecret", true},
		{"secretsmanager/CreateSecretVersion", true},
		{"kms/ScheduleKeyDeletion", true},
		{"kms/DisableKey", true},
		{"cfn/disable-consent", true},
		{"ecs/auto-approve-forever", true},
		// Allowed entries — explicitly non-forbidden.
		{"cfn/describe-stack", false},
		{"ecs/describe-cluster", false},
		{"sts/get-caller-identity", false},
		{"file/read", false},
		{"shell/exec", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsForbidden(c.name); got != c.forbidden {
				t.Fatalf("IsForbidden(%q) = %v, want %v", c.name, got, c.forbidden)
			}
		})
	}
}

// TestLogForbiddenAttempt_EmitsSecurityEvent covers the DoD line "A
// forbidden call from a fake AI conversation … is logged as a security
// event." We swap log.Default for a buffer-backed logger, drive the same
// path the registry uses on a forbidden Call, and assert the warn-level
// record carries the security-event marker and tool name.
func TestLogForbiddenAttempt_EmitsSecurityEvent(t *testing.T) {
	var buf bytes.Buffer
	prev := pwlog.Default
	t.Cleanup(func() { pwlog.Default = prev })
	pwlog.Default = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	r := NewRegistry()
	_, err := r.Call(context.Background(), "iam/CreateUser", map[string]any{"username": "evil"})
	if err == nil {
		t.Fatal("expected forbidden Call to fail")
	}

	got := buf.String()
	for _, want := range []string{
		"event=ai_tool_forbidden_call",
		"tool=iam/CreateUser",
		"level=WARN",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log output missing %q\nfull output:\n%s", want, got)
		}
	}
	// arg_keys should mention the username key (sorted), without leaking
	// the actual value "evil" — that's the redaction property we depend on.
	if !strings.Contains(got, "username") {
		t.Fatal("expected arg_keys to include the requested key")
	}
	if strings.Contains(got, "evil") {
		t.Fatal("argument value leaked into the security event log")
	}
}

// TestPermissionString covers the rendering path used in audit logs.
func TestPermissionString(t *testing.T) {
	if PermissionRead.String() != "read" {
		t.Fatalf("PermissionRead.String() = %q", PermissionRead.String())
	}
	if PermissionWrite.String() != "write" {
		t.Fatalf("PermissionWrite.String() = %q", PermissionWrite.String())
	}
	if Permission(0).String() != "unknown" {
		t.Fatalf("zero-value Permission.String() = %q", Permission(0).String())
	}
}
