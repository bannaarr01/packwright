package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bannaarr01/packwright/action"
	"github.com/bannaarr01/packwright/manifest"
)

// defaultGracePeriod is the SIGTERM-to-SIGKILL window per ADR-0014.
const defaultGracePeriod = 5 * time.Second

// Spec is a shell action's runtime configuration. Authors declare it in the
// manifest's run: block; the runner reads it from a Runner instance set by
// the manifest-loading code (or, for this PR, by tests and in-process
// callers).
type Spec struct {
	// Command is the command to run. In array form (Shell == ""), the first
	// element is the program and the remaining elements are individual
	// args; each is template-substituted as a single token, so a templated
	// value containing whitespace or shell metacharacters is passed
	// verbatim — never re-split. In shell form (Shell == "bash"), Command
	// must hold exactly one element: the templated bash -c command line.
	Command []string

	// Shell selects the invocation path. The empty string (default) means
	// array form (exec.Command(name, args...)). "bash" means bash -c. Any
	// other value is rejected by Validate; ADR-0014's trust-prompt covers
	// "should this run as bash at all" so the runner does not further
	// restrict what bash sees.
	Shell string

	// Env adds or overrides environment variables. Values are template-
	// substituted; keys pass through verbatim. Entries here override
	// inherited variables of the same name.
	Env map[string]string

	// RequiresEnv lists environment variables that must be present in
	// os.Environ() before the command runs. A missing var causes Run to
	// return a *MissingEnvError listing every name that was unset, without
	// starting the subprocess.
	RequiresEnv []string
}

// Result is the kind-specific value placed in action.Result.Value on a
// successful Run. Stdout and Stderr are the full buffered output for this
// PR; once the streaming primitive in PR-05 lands callers will receive
// incremental lines, but the buffered fields are preserved for tests and
// non-streaming consumers.
type Result struct {
	// ExitCode is the subprocess exit status. Zero on clean exit; non-zero
	// values reach the caller as a successful Run with a non-nil Result
	// (no error), so the caller can decide how to surface failures.
	ExitCode int
	// Stdout is the complete buffered stdout stream.
	Stdout string
	// Stderr is the complete buffered stderr stream.
	Stderr string
}

// MissingEnvError is returned by Run when one or more requires_env entries
// are absent from os.Environ(). Missing names appear in the order they were
// declared in the Spec so error messages stay deterministic.
type MissingEnvError struct {
	// Missing lists the env-var names that were absent from the parent
	// process environment.
	Missing []string
}

// Error renders a structured message that includes every missing var.
func (e *MissingEnvError) Error() string {
	return fmt.Sprintf("shell: missing required environment variables: %s",
		strings.Join(e.Missing, ", "))
}

// ErrSpecMissing is returned by Run when the Runner was registered without
// an attached Spec. The dispatcher-stub instance installed by init() has no
// Spec; the manifest-loading PR that populates Spec from YAML is still in
// flight. Until that lands, in-process callers construct &Runner{Spec: ...}
// and invoke it directly.
var ErrSpecMissing = errors.New("shell: spec not configured")

// Runner is the action.Runner for kind: shell. The zero value answers Kind
// and Validate correctly; Run returns ErrSpecMissing until a Spec is
// attached.
type Runner struct {
	// Spec is the shell-action configuration. Nil for the global instance
	// the package init() registers; set explicitly by tests and any
	// in-process caller that already holds the Spec.
	Spec *Spec

	// GracePeriod overrides the SIGTERM-to-SIGKILL window per Run. Zero
	// uses defaultGracePeriod (ADR-0014's 5 seconds). Tests set this short
	// so cancellation behaviour is exercised in real-time.
	GracePeriod time.Duration
}

// Kind reports manifest.KindShell.
func (*Runner) Kind() manifest.Kind { return manifest.KindShell }

// Validate enforces the manifest-structure invariants the shell runner
// requires (manifest non-nil, kind matches) and, when a Spec is attached,
// the per-Spec invariants (non-empty command, allowed shell mode).
func (r *Runner) Validate(m *manifest.Manifest) error {
	if m == nil {
		return errors.New("shell: manifest is nil")
	}
	if m.Kind != manifest.KindShell {
		return fmt.Errorf("shell: manifest kind %q does not match runner kind %q",
			m.Kind, manifest.KindShell)
	}
	if r.Spec != nil {
		return validateSpec(r.Spec)
	}
	return nil
}

// validateSpec runs the shell-config-only checks that do not depend on a
// manifest: command shape and the shell-mode allow-list.
func validateSpec(s *Spec) error {
	if len(s.Command) == 0 {
		return errors.New("shell: spec.command is empty")
	}
	switch s.Shell {
	case "":
		// array form — any non-empty command is valid
	case "bash":
		if len(s.Command) != 1 {
			return errors.New(`shell: spec.command for shell: "bash" must hold exactly one element`)
		}
	default:
		return fmt.Errorf("shell: unsupported shell mode %q (only %q is allowed)", s.Shell, "bash")
	}
	return nil
}

// Run executes the configured shell action, honouring cancellation,
// requires_env, and env-override semantics described on Spec. It returns
// action.Result with Value set to *Result on success; a non-zero subprocess
// exit code is reported via Result.ExitCode (not as an error) so the caller
// can choose how to surface it.
func (r *Runner) Run(ctx context.Context, m *manifest.Manifest, in action.Inputs) (action.Result, error) {
	if r.Spec == nil {
		return action.Result{Kind: manifest.KindShell}, ErrSpecMissing
	}
	if err := validateSpec(r.Spec); err != nil {
		return action.Result{Kind: manifest.KindShell}, err
	}
	if err := checkRequiredEnv(r.Spec.RequiresEnv); err != nil {
		return action.Result{Kind: manifest.KindShell}, err
	}
	res, err := r.execute(ctx, r.Spec, in)
	if err != nil {
		// Surface the captured streams alongside the error so callers
		// (logs, the trust-prompt UI) can show what the subprocess wrote
		// before failing.
		return action.Result{Kind: manifest.KindShell, Value: res}, err
	}
	return action.Result{Kind: manifest.KindShell, Value: res}, nil
}

// checkRequiredEnv inspects os.Environ() for each name in required and
// returns a *MissingEnvError listing every entry that is unset. An empty
// or nil required slice is a no-op.
func checkRequiredEnv(required []string) error {
	if len(required) == 0 {
		return nil
	}
	var missing []string
	for _, name := range required {
		if _, ok := os.LookupEnv(name); !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return &MissingEnvError{Missing: missing}
	}
	return nil
}

// execute runs the command described by spec and inputs, returning the
// captured Result on completion (success, non-zero exit, or cancellation)
// and an error only when the subprocess could not be launched, the inputs
// failed template substitution, or the context was cancelled before exit.
func (r *Runner) execute(ctx context.Context, spec *Spec, in action.Inputs) (*Result, error) {
	data := map[string]any(in)

	cmd, err := buildCmd(spec, data)
	if err != nil {
		return nil, err
	}

	envMap, err := substituteEnv(spec.Env, data)
	if err != nil {
		return nil, err
	}
	cmd.Env = mergeEnv(os.Environ(), envMap)

	// Buffered capture. A lockedWriter wraps each buffer so the wait-and-
	// signal goroutine cannot race with the os/exec copying goroutines on
	// the rare cmd.Run() teardown path; bytes.Buffer is not goroutine-
	// safe and exec.Cmd internally spawns one copier goroutine per stream.
	var stdout, stderr bytes.Buffer
	var stdoutMu, stderrMu sync.Mutex
	cmd.Stdout = &lockedWriter{w: &stdout, mu: &stdoutMu}
	cmd.Stderr = &lockedWriter{w: &stderr, mu: &stderrMu}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("shell: start: %w", err)
	}

	// Cancellation watcher. The subprocess gets SIGTERM on ctx.Done();
	// if it has not exited inside grace, the watcher follows up with
	// SIGKILL. The watcher exits as soon as Wait returns to avoid
	// leaking past the run.
	waitDone := make(chan struct{})
	go r.watchCancel(ctx, cmd, waitDone)

	waitErr := cmd.Wait()
	close(waitDone)

	// snapshot stdout/stderr under their locks so any in-flight writes
	// from the os/exec copier goroutines are observed.
	stdoutMu.Lock()
	stdoutStr := stdout.String()
	stdoutMu.Unlock()
	stderrMu.Lock()
	stderrStr := stderr.String()
	stderrMu.Unlock()

	res := &Result{
		ExitCode: cmd.ProcessState.ExitCode(),
		Stdout:   stdoutStr,
		Stderr:   stderrStr,
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		// ctx.Err() is the primary cancellation signal; the wrapped
		// ExitError is incidental noise.
		return res, ctxErr
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			// Non-zero subprocess exit is data, not an error.
			return res, nil
		}
		return res, fmt.Errorf("shell: wait: %w", waitErr)
	}
	return res, nil
}

// buildCmd constructs the *exec.Cmd for spec, applying template substitution
// to the command (array form) or the bash command line (shell form). The
// command does not yet have Env, Stdout, or Stderr set — the caller wires
// those after substituteEnv runs.
func buildCmd(spec *Spec, data map[string]any) (*exec.Cmd, error) {
	if spec.Shell == "bash" {
		rendered, err := substituteArg(spec.Command[0], data)
		if err != nil {
			return nil, err
		}
		return exec.Command("bash", "-c", rendered), nil
	}
	args, err := substituteArgs(spec.Command, data)
	if err != nil {
		return nil, err
	}
	return exec.Command(args[0], args[1:]...), nil
}

// watchCancel is the goroutine body that translates ctx cancellation into
// SIGTERM and, after the grace period, SIGKILL. It returns as soon as
// waitDone closes (the parent's cmd.Wait has returned).
func (r *Runner) watchCancel(ctx context.Context, cmd *exec.Cmd, waitDone <-chan struct{}) {
	select {
	case <-ctx.Done():
	case <-waitDone:
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)

	grace := r.GracePeriod
	if grace <= 0 {
		grace = defaultGracePeriod
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()

	select {
	case <-waitDone:
		// Exited within the grace period; no SIGKILL needed.
	case <-timer.C:
		_ = cmd.Process.Kill()
	}
}

// mergeEnv returns a copy of base with overrides applied: keys in over
// replace the corresponding entry from base. Both the base slice and the
// resulting slice use the canonical "KEY=VALUE" format.
func mergeEnv(base []string, over map[string]string) []string {
	if len(over) == 0 {
		out := make([]string, len(base))
		copy(out, base)
		return out
	}
	idx := make(map[string]int, len(base))
	out := make([]string, 0, len(base)+len(over))
	for _, e := range base {
		if i := strings.IndexByte(e, '='); i >= 0 {
			idx[e[:i]] = len(out)
		}
		out = append(out, e)
	}
	for k, v := range over {
		entry := k + "=" + v
		if i, ok := idx[k]; ok {
			out[i] = entry
			continue
		}
		idx[k] = len(out)
		out = append(out, entry)
	}
	return out
}

// lockedWriter serialises Write calls into the underlying writer. exec.Cmd
// spawns one copier goroutine per output stream; the locks here let the
// post-Wait reader observe a consistent buffer in the rare case the copier
// is still flushing pipe contents when Wait returns.
type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

// Write satisfies io.Writer.
func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func init() { action.Register(&Runner{}) }
