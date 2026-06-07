package shell

import (
	"strings"
	"testing"
)

// TestSubstituteArg_RendersValue confirms the happy path: a templated
// argument with a single {{ .Foo }} reference produces the form value.
func TestSubstituteArg_RendersValue(t *testing.T) {
	got, err := substituteArg("hello {{ .Name }}", map[string]any{"Name": "world"})
	if err != nil {
		t.Fatalf("substituteArg: unexpected error: %v", err)
	}
	if got != "hello world" {
		t.Errorf("substituteArg = %q, want %q", got, "hello world")
	}
}

// TestSubstituteArg_MissingKeyIsError documents the missingkey=error
// override: an unknown form field produces an error rather than the Go-
// template default of "<no value>".
func TestSubstituteArg_MissingKeyIsError(t *testing.T) {
	_, err := substituteArg("{{ .Missing }}", map[string]any{"Other": "x"})
	if err == nil {
		t.Fatal("substituteArg: expected error for missing key, got nil")
	}
	if !strings.Contains(err.Error(), "Missing") {
		t.Errorf("substituteArg error = %v, want it to mention the missing key", err)
	}
}

// TestSubstituteArg_NoTemplateIsLiteral exercises the no-template path: a
// plain string passes through unchanged.
func TestSubstituteArg_NoTemplateIsLiteral(t *testing.T) {
	got, err := substituteArg("plain text", nil)
	if err != nil {
		t.Fatalf("substituteArg: unexpected error: %v", err)
	}
	if got != "plain text" {
		t.Errorf("substituteArg = %q, want %q", got, "plain text")
	}
}

// TestSubstituteArgs_RendersIndependently is the key safety property: each
// element is substituted as a single token, so a value containing spaces or
// shell metacharacters does NOT re-split into multiple args.
func TestSubstituteArgs_RendersIndependently(t *testing.T) {
	args := []string{"aws", "ecs", "--cluster", "{{ .Cluster }}"}
	got, err := substituteArgs(args, map[string]any{"Cluster": "prod cluster; rm -rf /"})
	if err != nil {
		t.Fatalf("substituteArgs: unexpected error: %v", err)
	}
	want := []string{"aws", "ecs", "--cluster", "prod cluster; rm -rf /"}
	if len(got) != len(want) {
		t.Fatalf("substituteArgs: len = %d, want %d (got %#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("substituteArgs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestSubstituteArgs_PropagatesError surfaces any per-element rendering
// error to the caller; a single bad entry fails the whole batch.
func TestSubstituteArgs_PropagatesError(t *testing.T) {
	_, err := substituteArgs([]string{"ok", "{{ .Missing }}"}, map[string]any{})
	if err == nil {
		t.Fatal("substituteArgs: expected error, got nil")
	}
}

// TestSubstituteEnv_TemplatesValues confirms env values are rendered while
// keys pass through literally.
func TestSubstituteEnv_TemplatesValues(t *testing.T) {
	env := map[string]string{"AWS_PROFILE": "{{ .Profile }}", "LITERAL": "v"}
	got, err := substituteEnv(env, map[string]any{"Profile": "prod"})
	if err != nil {
		t.Fatalf("substituteEnv: unexpected error: %v", err)
	}
	if got["AWS_PROFILE"] != "prod" {
		t.Errorf("AWS_PROFILE = %q, want %q", got["AWS_PROFILE"], "prod")
	}
	if got["LITERAL"] != "v" {
		t.Errorf("LITERAL = %q, want %q", got["LITERAL"], "v")
	}
}

// TestSubstituteEnv_PropagatesError surfaces a templated value's failure
// (e.g. missing key) with the offending env-var name in the wrapper.
func TestSubstituteEnv_PropagatesError(t *testing.T) {
	_, err := substituteEnv(
		map[string]string{"AWS_PROFILE": "{{ .Missing }}"},
		map[string]any{},
	)
	if err == nil {
		t.Fatal("substituteEnv: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "AWS_PROFILE") {
		t.Errorf("substituteEnv error = %v, want it to mention AWS_PROFILE", err)
	}
}

// TestSubstituteEnv_EmptyInput returns a non-nil empty map so callers can
// range without a nil check.
func TestSubstituteEnv_EmptyInput(t *testing.T) {
	got, err := substituteEnv(nil, nil)
	if err != nil {
		t.Fatalf("substituteEnv(nil): unexpected error: %v", err)
	}
	if got == nil {
		t.Error("substituteEnv(nil): map is nil, want empty non-nil map")
	}
	if len(got) != 0 {
		t.Errorf("substituteEnv(nil): len = %d, want 0", len(got))
	}
}
