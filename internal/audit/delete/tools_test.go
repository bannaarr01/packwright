package delete

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// withFakeClients swaps the package-level clientFactory for the
// duration of t. Restores the previous factory on cleanup so the
// next test sees the production builder.
func withFakeClients(t *testing.T, c *Clients) {
	t.Helper()
	prev := clientFactory
	clientFactory = func(context.Context, string) (*Clients, error) { return c, nil }
	t.Cleanup(func() { clientFactory = prev })
}

func TestTools_AllRegistered(t *testing.T) {
	t.Parallel()
	want := []string{
		"audit/delete-volume",
		"audit/delete-snapshot",
		"audit/release-eip",
		"audit/delete-nat-gateway",
		"audit/delete-target-group",
		"audit/delete-log-group",
		"audit/delete-rds-snapshot",
		"audit/delete-ecr-image",
	}
	for _, name := range want {
		if _, ok := tools.Default.Lookup(name); !ok {
			t.Errorf("tool %q not registered in tools.Default", name)
		}
	}
}

func TestTools_ForbiddenKindsAreNotRegistered(t *testing.T) {
	t.Parallel()
	// ADR-0043 explicitly excludes RDS DB instance, S3 bucket,
	// and KMS key deletion from v1. We must not have registered
	// audit/delete-* tools for any of them.
	mustBeAbsent := []string{
		"audit/delete-rds-db-instance",
		"audit/delete-bucket",
		"audit/delete-kms-key",
		"audit/delete-secret",
	}
	for _, name := range mustBeAbsent {
		if _, ok := tools.Default.Lookup(name); ok {
			t.Errorf("tool %q is registered but ADR-0043 forbids it in v1", name)
		}
	}
}

func TestTools_AllArePermissionWrite(t *testing.T) {
	t.Parallel()
	names := []string{
		"audit/delete-volume",
		"audit/delete-snapshot",
		"audit/release-eip",
		"audit/delete-nat-gateway",
		"audit/delete-target-group",
		"audit/delete-log-group",
		"audit/delete-rds-snapshot",
		"audit/delete-ecr-image",
	}
	for _, name := range names {
		tool, ok := tools.Default.Lookup(name)
		if !ok {
			t.Fatalf("tool %q missing", name)
		}
		if tool.Permission() != tools.PermissionWrite {
			t.Errorf("%s.Permission() = %v, want PermissionWrite", name, tool.Permission())
		}
	}
}

func TestTools_DeleteVolume_RequiresReason(t *testing.T) {
	// No t.Parallel(): mutates the package-level clientFactory.
	withFakeClients(t, &Clients{EC2: &fakeEC2{}})
	tool, _ := tools.Default.Lookup("audit/delete-volume")
	_, err := tool.Execute(context.Background(), map[string]any{"volume_id": "vol-1"})
	if err == nil {
		t.Fatal("Execute without reason succeeded, want error")
	}
	var te *tools.ToolError
	if !errors.As(err, &te) || te.Code != tools.ErrCodeBadArgs {
		t.Errorf("err = %v, want *tools.ToolError with ErrCodeBadArgs", err)
	}
}

func TestTools_DeleteVolume_HappyPath(t *testing.T) {
	// No t.Parallel(): mutates the package-level clientFactory.
	ec2 := &fakeEC2{}
	withFakeClients(t, &Clients{EC2: ec2})
	tool, _ := tools.Default.Lookup("audit/delete-volume")
	out, err := tool.Execute(context.Background(), map[string]any{
		"volume_id": "vol-1",
		"reason":    "unused 142d",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if m, ok := out.(map[string]any); !ok || m["deleted"] != true {
		t.Errorf("Execute returned %v, want {\"deleted\":true}", out)
	}
	if calls := ec2.Calls(); len(calls) != 1 || calls[0] != "DeleteVolume" {
		t.Errorf("AWS calls = %v, want [DeleteVolume]", calls)
	}
}

func TestTools_DeleteECRImage_RequiresAllArgs(t *testing.T) {
	// No t.Parallel(): mutates the package-level clientFactory.
	withFakeClients(t, &Clients{ECR: &fakeECR{}})
	tool, _ := tools.Default.Lookup("audit/delete-ecr-image")
	cases := []map[string]any{
		// missing image_digest
		{"repository_name": "repo", "reason": "stale"},
		// missing repository_name
		{"image_digest": "sha256:abc", "reason": "stale"},
		// missing reason
		{"repository_name": "repo", "image_digest": "sha256:abc"},
	}
	for _, args := range cases {
		_, err := tool.Execute(context.Background(), args)
		if err == nil {
			t.Errorf("Execute(%v) succeeded, want error", args)
		}
	}
}

func TestTools_DeleteECRImage_HappyPath(t *testing.T) {
	// No t.Parallel(): mutates the package-level clientFactory.
	ecr := &fakeECR{}
	withFakeClients(t, &Clients{ECR: ecr})
	tool, _ := tools.Default.Lookup("audit/delete-ecr-image")
	_, err := tool.Execute(context.Background(), map[string]any{
		"repository_name": "myapp",
		"image_digest":    "sha256:abc",
		"reason":          "stale image, no consumers",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if calls := ecr.Calls(); len(calls) != 1 || calls[0] != "BatchDeleteImage" {
		t.Errorf("ECR calls = %v, want [BatchDeleteImage]", calls)
	}
}

func TestTools_SchemaNameMatchesToolName(t *testing.T) {
	t.Parallel()
	// MustRegister already enforces this, but the assertion
	// guards against a future refactor that bypasses it.
	for _, name := range []string{
		"audit/delete-volume", "audit/delete-snapshot",
		"audit/release-eip", "audit/delete-nat-gateway",
		"audit/delete-target-group", "audit/delete-log-group",
		"audit/delete-rds-snapshot", "audit/delete-ecr-image",
	} {
		tool, _ := tools.Default.Lookup(name)
		if tool.Schema().Name != name {
			t.Errorf("tool %q has Schema.Name=%q", name, tool.Schema().Name)
		}
		if tool.Schema().Description == "" {
			t.Errorf("tool %q has empty Schema.Description", name)
		}
		props, _ := tool.Schema().Parameters["properties"].(map[string]any)
		if props == nil {
			t.Errorf("tool %q has no properties in schema", name)
			continue
		}
		if _, ok := props["reason"]; !ok {
			t.Errorf("tool %q schema missing 'reason' parameter", name)
		}
	}
}

// TestTools_FactoryErrorIsSurfaced confirms that an error from
// clientFactory (e.g. no AWS client in context) flows through to
// the caller unwrapped enough to identify the misconfiguration.
func TestTools_FactoryErrorIsSurfaced(t *testing.T) {
	// No t.Parallel(): mutates the package-level clientFactory.
	prev := clientFactory
	clientFactory = func(context.Context, string) (*Clients, error) {
		return nil, &tools.ToolError{Code: tools.ErrCodeMisconfigured, Message: "no awsx"}
	}
	t.Cleanup(func() { clientFactory = prev })
	tool, _ := tools.Default.Lookup("audit/delete-volume")
	_, err := tool.Execute(context.Background(), map[string]any{
		"volume_id": "vol-1",
		"reason":    "test",
	})
	if err == nil {
		t.Fatal("Execute succeeded with missing client, want error")
	}
	if !strings.Contains(err.Error(), "misconfigured") && !strings.Contains(err.Error(), "no awsx") {
		t.Errorf("error = %v, expected mention of misconfiguration", err)
	}
}
