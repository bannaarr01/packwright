// Package shell implements the action.Runner for kind: shell. It executes
// command-line operations declared by a manifest's shell Spec, substituting
// form inputs into individual arguments (array form, the default) or into a
// single bash -c command string when the manifest opts into shell: bash.
//
// The runner inherits the parent process environment, applies manifest-
// declared env overrides, refuses to run when requires_env entries are
// missing, and cancels via SIGTERM + 5-second grace + SIGKILL on ctx.Done(),
// per ADR-0014.
//
// The manifest-loading path that populates Spec from YAML is the
// responsibility of a follow-up PR; until that ships, in-process callers
// (tests, embedders) construct &Runner{Spec: ...} directly. The init() in
// runner.go registers an empty Runner so the dispatcher can route shell
// kind, with Run returning ErrSpecMissing until a Spec is attached.
package shell

import (
	"bytes"
	"fmt"
	"text/template"
)

// substituteArg renders a single templated argument against data. Each call
// produces one token: array-form execution invokes exec.Command(name,
// args...) directly, so the rendered string is passed to the subprocess as
// a single argument — shell metacharacters never split or expand.
//
// Missing keys are an error (not the Go-template default "<no value>") so a
// typo in {{ .Cluster }} never silently emits the literal "<no value>" into
// a command line.
func substituteArg(arg string, data map[string]any) (string, error) {
	tmpl, err := template.New("arg").Option("missingkey=error").Parse(arg)
	if err != nil {
		return "", fmt.Errorf("shell: parse template %q: %w", arg, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("shell: render template %q: %w", arg, err)
	}
	return buf.String(), nil
}

// substituteArgs renders each entry in args via substituteArg, returning a
// fresh slice so callers do not alias the manifest's underlying storage.
func substituteArgs(args []string, data map[string]any) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		s, err := substituteArg(a, data)
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

// substituteEnv renders the values in env via substituteArg; keys pass
// through verbatim (the env-var name is a literal manifest key, not a
// template). A nil or empty input returns a non-nil empty map so callers
// can range over the result unconditionally.
func substituteEnv(env map[string]string, data map[string]any) (map[string]string, error) {
	out := make(map[string]string, len(env))
	for k, v := range env {
		s, err := substituteArg(v, data)
		if err != nil {
			return nil, fmt.Errorf("shell: env %s: %w", k, err)
		}
		out[k] = s
	}
	return out, nil
}
