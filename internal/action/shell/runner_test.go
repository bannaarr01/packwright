package shell

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/action"
	"github.com/bannaarr01/packwright/manifest"
)

// shellManifest is the in-memory fixture used by every Runner test. The
// shim manifest at github.com/bannaarr01/packwright/manifest does not yet
// carry a Run/Spec section, so configuration travels on the Runner itself
// (see comment on ErrSpecMissing); the manifest here only carries the
// invariants Validate inspects.
func shellManifest() *manifest.Manifest {
	return &manifest.Manifest{
		SchemaVersion: "packwright.manifest.v1",
		ID:            "test-shell",
		Kind:          manifest.KindShell,
		Slash:         "/hello",
		Title:         "say hi",
	}
}

// TestRunner_Kind reports manifest.KindShell so the dispatcher's registry
// lookup hits the right entry.
func TestRunner_Kind(t *testing.T) {
	r := &Runner{}
	if got := r.Kind(); got != manifest.KindShell {
		t.Errorf("Kind() = %q, want %q", got, manifest.KindShell)
	}
}

// TestRunner_RegistersInActionRegistry confirms the package init() installs
// a *Runner under manifest.KindShell so action.Lookup returns the real
// implementation, replacing PR-01's stub.
func TestRunner_RegistersInActionRegistry(t *testing.T) {
	got, ok := action.Lookup(manifest.KindShell)
	if !ok {
		t.Fatal("action.Lookup: no runner registered for KindShell")
	}
	if _, ok := got.(*Runner); !ok {
		t.Errorf("action.Lookup: registered runner is %T, want *shell.Runner", got)
	}
}

// TestRunner_Validate_NilManifest is the first Validate guard: a nil
// manifest never reaches Run.
func TestRunner_Validate_NilManifest(t *testing.T) {
	r := &Runner{}
	if err := r.Validate(nil); err == nil {
		t.Fatal("Validate(nil): expected error, got nil")
	}
}

// TestRunner_Validate_WrongKind rejects manifests routed by mistake.
func TestRunner_Validate_WrongKind(t *testing.T) {
	r := &Runner{}
	m := &manifest.Manifest{Kind: manifest.KindResource}
	if err := r.Validate(m); err == nil {
		t.Fatal("Validate(resource manifest): expected error, got nil")
	}
}

// TestRunner_Validate_SpecChecks fires only when a Spec is attached, so the
// registered global instance (Spec == nil) passes structural Validate.
func TestRunner_Validate_SpecChecks(t *testing.T) {
	cases := []struct {
		name    string
		spec    *Spec
		wantErr string
	}{
		{"empty command", &Spec{Command: nil}, "empty"},
		{"bash with multi-element command", &Spec{Shell: "bash", Command: []string{"echo", "hi"}}, "exactly one"},
		{"unknown shell", &Spec{Shell: "zsh", Command: []string{"echo"}}, "unsupported shell"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{Spec: tc.spec}
			err := r.Validate(shellManifest())
			if err == nil {
				t.Fatalf("Validate: expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Validate error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestRunner_Run_NoSpec returns ErrSpecMissing for the registered global
// instance until the manifest-loading PR wires a Spec into it.
func TestRunner_Run_NoSpec(t *testing.T) {
	r := &Runner{}
	_, err := r.Run(context.Background(), shellManifest(), nil)
	if !errors.Is(err, ErrSpecMissing) {
		t.Errorf("Run: error = %v, want ErrSpecMissing", err)
	}
}

// TestRunner_Run_ArrayForm_TemplatedCommand is the DoD fixture: a shell
// kind manifest with `echo {{ .Greeting }}` executes and returns the
// expected stdout.
func TestRunner_Run_ArrayForm_TemplatedCommand(t *testing.T) {
	r := &Runner{Spec: &Spec{Command: []string{"echo", "{{ .Greeting }}"}}}
	got, err := r.Run(context.Background(), shellManifest(), action.Inputs{"Greeting": "hi"})
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	res, ok := got.Value.(*Result)
	if !ok {
		t.Fatalf("Run: Result.Value = %T, want *shell.Result", got.Value)
	}
	if strings.TrimRight(res.Stdout, "\n") != "hi" {
		t.Errorf("Run stdout = %q, want %q (trimmed)", res.Stdout, "hi")
	}
	if res.ExitCode != 0 {
		t.Errorf("Run exit code = %d, want 0", res.ExitCode)
	}
}

// TestRunner_Run_ArrayForm_PreservesSingleArg is the security property
// from ADR-0014: shell metacharacters in a templated value are passed to
// the subprocess as a single argument and never re-split by a shell.
func TestRunner_Run_ArrayForm_PreservesSingleArg(t *testing.T) {
	// "$(echo pwned)" would be expanded by a shell; printf with %s should
	// emit it verbatim because no shell sees it.
	r := &Runner{Spec: &Spec{Command: []string{"printf", "%s", "{{ .Payload }}"}}}
	payload := "$(echo pwned)"
	got, err := r.Run(context.Background(), shellManifest(), action.Inputs{"Payload": payload})
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	res := got.Value.(*Result)
	if res.Stdout != payload {
		t.Errorf("Run stdout = %q, want %q (verbatim — no shell expansion)", res.Stdout, payload)
	}
}

// TestRunner_Run_BashForm executes the templated string via bash -c, which
// IS shell-expanded — that is the entire point of the opt-in.
func TestRunner_Run_BashForm(t *testing.T) {
	r := &Runner{Spec: &Spec{Shell: "bash", Command: []string{`echo "{{ .Greeting }}"`}}}
	got, err := r.Run(context.Background(), shellManifest(), action.Inputs{"Greeting": "hi from bash"})
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	res := got.Value.(*Result)
	if strings.TrimRight(res.Stdout, "\n") != "hi from bash" {
		t.Errorf("Run stdout = %q, want %q (trimmed)", res.Stdout, "hi from bash")
	}
}

// TestRunner_Run_MissingRequiredEnv refuses to run when any required env
// variable is absent, returning a *MissingEnvError listing the names.
func TestRunner_Run_MissingRequiredEnv(t *testing.T) {
	r := &Runner{Spec: &Spec{
		Command:     []string{"echo", "ok"},
		RequiresEnv: []string{"PW_TEST_DEFINITELY_UNSET_1", "PW_TEST_DEFINITELY_UNSET_2"},
	}}
	_, err := r.Run(context.Background(), shellManifest(), nil)
	if err == nil {
		t.Fatal("Run: expected MissingEnvError, got nil")
	}
	var me *MissingEnvError
	if !errors.As(err, &me) {
		t.Fatalf("Run: error type = %T (%v), want *MissingEnvError", err, err)
	}
	if len(me.Missing) != 2 {
		t.Errorf("MissingEnvError.Missing = %v, want 2 entries", me.Missing)
	}
}

// TestRunner_Run_PresentRequiredEnv runs the command when every requires_env
// entry is set, with the value flowing through to the subprocess.
func TestRunner_Run_PresentRequiredEnv(t *testing.T) {
	t.Setenv("PW_SHELL_TEST_REQUIRED", "x")
	r := &Runner{Spec: &Spec{
		Command:     []string{"printf", "%s", "ok"},
		RequiresEnv: []string{"PW_SHELL_TEST_REQUIRED"},
	}}
	got, err := r.Run(context.Background(), shellManifest(), nil)
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if got.Value.(*Result).Stdout != "ok" {
		t.Errorf("Run stdout = %q, want %q", got.Value.(*Result).Stdout, "ok")
	}
}

// TestRunner_Run_EnvOverride wires a manifest-declared env var through to
// the subprocess, overriding any inherited value of the same name.
func TestRunner_Run_EnvOverride(t *testing.T) {
	t.Setenv("PW_SHELL_TEST_OVERRIDE", "inherited")
	r := &Runner{Spec: &Spec{
		Command: []string{"bash", "-c", `printf %s "$PW_SHELL_TEST_OVERRIDE"`},
		Env:     map[string]string{"PW_SHELL_TEST_OVERRIDE": "{{ .V }}"},
	}}
	got, err := r.Run(context.Background(), shellManifest(), action.Inputs{"V": "overridden"})
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	if got.Value.(*Result).Stdout != "overridden" {
		t.Errorf("Run stdout = %q, want %q", got.Value.(*Result).Stdout, "overridden")
	}
}

// TestRunner_Run_NonZeroExitNotAnError keeps the subprocess's exit status
// as data on Result.ExitCode rather than as a returned error, so callers
// can render the failure in the UI without unwrapping.
func TestRunner_Run_NonZeroExitNotAnError(t *testing.T) {
	r := &Runner{Spec: &Spec{Command: []string{"false"}}}
	got, err := r.Run(context.Background(), shellManifest(), nil)
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}
	res := got.Value.(*Result)
	if res.ExitCode == 0 {
		t.Errorf("Run exit code = 0, want non-zero from `false`")
	}
}

// TestRunner_Run_TemplateError surfaces a render failure (missing key)
// before the subprocess is even started.
func TestRunner_Run_TemplateError(t *testing.T) {
	r := &Runner{Spec: &Spec{Command: []string{"echo", "{{ .Missing }}"}}}
	_, err := r.Run(context.Background(), shellManifest(), action.Inputs{})
	if err == nil {
		t.Fatal("Run: expected template error, got nil")
	}
}

// TestRunner_Run_CancelSIGTERM exercises the cooperative cancellation path:
// the subprocess respects SIGTERM and exits inside the grace window. The
// returned error mirrors ctx.Err() so callers can branch on context.Canceled.
func TestRunner_Run_CancelSIGTERM(t *testing.T) {
	r := &Runner{
		Spec:        &Spec{Command: []string{"sleep", "30"}},
		GracePeriod: 2 * time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := r.Run(ctx, shellManifest(), nil)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: error = %v, want context.Canceled", err)
	}
	// sleep exits on SIGTERM well inside the 2-second grace.
	if elapsed >= 2*time.Second {
		t.Errorf("Run elapsed = %v, want it to exit on SIGTERM inside the grace window", elapsed)
	}
}

// TestRunner_Run_CancelEscalatesToSIGKILL exercises the grace-then-SIGKILL
// path: a subprocess that traps SIGTERM is still terminated after the
// grace window expires. The grace is shortened so the test runs quickly.
func TestRunner_Run_CancelEscalatesToSIGKILL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("trap-based SIGTERM ignore is POSIX-only")
	}
	grace := 200 * time.Millisecond
	r := &Runner{
		// trap "" TERM ignores SIGTERM; only SIGKILL can terminate.
		Spec:        &Spec{Shell: "bash", Command: []string{`trap "" TERM; sleep 30`}},
		GracePeriod: grace,
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := r.Run(ctx, shellManifest(), nil)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run: error = %v, want context.Canceled", err)
	}
	// Must take at least the grace period (SIGTERM ignored), then SIGKILL.
	if elapsed < grace {
		t.Errorf("Run elapsed = %v, want >= grace %v (SIGTERM was ignored)", elapsed, grace)
	}
	// And must NOT take ten times longer — proving SIGKILL actually fired.
	if elapsed > grace+2*time.Second {
		t.Errorf("Run elapsed = %v, want SIGKILL to terminate shortly after grace %v", elapsed, grace)
	}
}

// TestMergeEnv_OverridesAndPreservesOrder confirms overrides replace
// existing entries in place and net-new entries are appended.
func TestMergeEnv_OverridesAndPreservesOrder(t *testing.T) {
	base := []string{"A=1", "B=2", "C=3"}
	got := mergeEnv(base, map[string]string{"B": "two", "D": "4"})

	gotMap := envSliceToMap(got)
	wantPairs := map[string]string{"A": "1", "B": "two", "C": "3", "D": "4"}
	for k, v := range wantPairs {
		if gotMap[k] != v {
			t.Errorf("mergeEnv %s = %q, want %q", k, gotMap[k], v)
		}
	}
	if len(got) != 4 {
		t.Errorf("mergeEnv len = %d, want 4 (no duplicate B)", len(got))
	}
}

// envSliceToMap parses os.Environ()-style KEY=VALUE pairs into a map for
// assertion convenience.
func envSliceToMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, e := range env {
		if i := strings.IndexByte(e, '='); i >= 0 {
			out[e[:i]] = e[i+1:]
		}
	}
	return out
}
