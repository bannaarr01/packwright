package all

import (
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// TestAllToolsRegistered confirms importing the all/ package wires every
// ADR-0035 tool name into tools.Default. The test is what stops a future
// PR from quietly dropping a read tool's init() block — adding a new tool
// to the ADR without also extending this list will fail loudly here.
func TestAllToolsRegistered(t *testing.T) {
	wantRead := []string{
		"cfn/describe-stack",
		"cfn/describe-stack-events",
		"cfn/describe-stack-resources",
		"cfn/list-stacks",
		"cw/get-metric-data",
		"cw-logs/start-query",
		"cw-logs/get-query-results",
		"cw-logs/filter-log-events",
		"ecs/describe-cluster",
		"ecs/describe-service",
		"ecs/describe-tasks",
		"elbv2/describe-load-balancers",
		"elbv2/describe-target-health",
		"rds/describe-db-instance",
		"efs/describe-file-system",
		"pipeline/get-execution",
		"sts/get-caller-identity",
		"manifest/read",
		"manifest/list",
		"file/read",
		"pricing/get-product",
	}
	wantWrite := []string{
		"cfn/update-stack",
		"cfn/create-stack",
		"cfn/delete-stack",
		"cfn/cancel-update-stack",
		"ecs/update-service",
		"manifest/edit",
		"file/write",
		"file/delete",
		"shell/exec",
		"packwright/run-command",
	}

	for _, name := range wantRead {
		t.Run("read/"+name, func(t *testing.T) {
			tool, ok := tools.Default.Lookup(name)
			if !ok {
				t.Fatalf("read tool %q is not registered in tools.Default", name)
			}
			if tool.Permission() != tools.PermissionRead {
				t.Fatalf("tool %q has permission %v, want PermissionRead",
					name, tool.Permission())
			}
		})
	}
	for _, name := range wantWrite {
		t.Run("write/"+name, func(t *testing.T) {
			tool, ok := tools.Default.Lookup(name)
			if !ok {
				t.Fatalf("write tool %q is not registered in tools.Default", name)
			}
			if tool.Permission() != tools.PermissionWrite {
				t.Fatalf("tool %q has permission %v, want PermissionWrite",
					name, tool.Permission())
			}
		})
	}

	// Sanity: every registered tool's Schema.Name agrees with Tool.Name —
	// the Register check catches mismatches at install time, but a future
	// refactor might bypass Register from inside the package.
	for _, tool := range tools.Default.List() {
		if got := tool.Schema().Name; got != tool.Name() {
			t.Errorf("tool %q has Schema.Name %q", tool.Name(), got)
		}
		if !strings.Contains(tool.Name(), "/") {
			t.Errorf("tool name %q has no namespace separator", tool.Name())
		}
	}
}
