package ai

import (
	"strings"
	"testing"

	"github.com/bannaarr01/packwright/config"
)

func TestValidate_NilAndEmptyAreValid(t *testing.T) {
	if err := Validate(nil); err != nil {
		t.Fatalf("Validate(nil) = %v, want nil", err)
	}
	if err := Validate(&config.Config{}); err != nil {
		t.Fatalf("Validate(empty) = %v, want nil", err)
	}
	if err := Validate(&config.Config{AI: map[string]any{}}); err != nil {
		t.Fatalf("Validate(empty AI) = %v, want nil", err)
	}
}

func TestValidate_CleanAutoApproveIsValid(t *testing.T) {
	cfg := &config.Config{AI: map[string]any{
		"enabled":            true,
		configKeyAutoApprove: []any{"cfn/update-stack", "ecs/update-service"},
	}}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate(clean) = %v, want nil", err)
	}
}

func TestValidate_RejectsForbiddenAutoApprove(t *testing.T) {
	// A user cannot smuggle a forbidden tool onto the auto-approve list.
	for _, name := range []string{"iam/createuser", "IAM/CreateUser", "iam:CreateAccessKey", "s3/deletebucket"} {
		cfg := &config.Config{AI: map[string]any{
			configKeyAutoApprove: []any{"cfn/update-stack", name},
		}}
		err := Validate(cfg)
		if err == nil {
			t.Fatalf("Validate with forbidden %q returned nil error", name)
		}
		if !strings.Contains(err.Error(), "forbidden") {
			t.Fatalf("error for %q = %q, want it to mention 'forbidden'", name, err.Error())
		}
	}
}

func TestValidate_RejectsSafetyBypassKeys(t *testing.T) {
	for _, key := range safetyBypassKeys {
		cfg := &config.Config{AI: map[string]any{key: true}}
		if err := Validate(cfg); err == nil {
			t.Fatalf("Validate with safety-bypass key %q returned nil error", key)
		}
	}
}

func TestAutoApproveTools_DropsForbiddenAndEmpty(t *testing.T) {
	cfg := &config.Config{AI: map[string]any{
		configKeyAutoApprove: []any{"cfn/update-stack", "", "iam/createuser", "ecs/update-service", 42},
	}}
	got := AutoApproveTools(cfg)
	want := []string{"cfn/update-stack", "ecs/update-service"}
	if len(got) != len(want) {
		t.Fatalf("AutoApproveTools = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AutoApproveTools = %v, want %v", got, want)
		}
	}
}

func TestAutoApproveTools_HandlesStringSlice(t *testing.T) {
	cfg := &config.Config{AI: map[string]any{
		configKeyAutoApprove: []string{"cfn/update-stack"},
	}}
	if got := AutoApproveTools(cfg); len(got) != 1 || got[0] != "cfn/update-stack" {
		t.Fatalf("AutoApproveTools = %v, want [cfn/update-stack]", got)
	}
}
