package cfn

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/bannaarr01/packwright/manifest"
)

// LineSource identifies which stream a captured Line came from. The renderer
// tags each line so the engine can render them differently (e.g. stderr in a
// warning style).
type LineSource string

// Recognised line sources.
const (
	StdoutLine LineSource = "stdout"
	StderrLine LineSource = "stderr"
)

// Line is one captured stdout/stderr line from the deploy subprocess, with the
// timestamp at which the renderer observed it.
type Line struct {
	Time   time.Time
	Source LineSource
	Text   string
}

// DefaultSigTermDelay is how long Deploy waits between sending SIGTERM and
// escalating to SIGKILL when ctx is cancelled. ADR-0008's "richer safe-cancel"
// (CancelUpdateStack / DeleteStack) lands in MVP-2 PR-05.
const DefaultSigTermDelay = 5 * time.Second

// Renderer writes parameters.json and drives the script-based deploy.
//
// All paths in the manifest are interpreted relative to BaseDir. ExecCommand
// is the seam for tests to substitute a fake exec.Cmd; production callers
// leave it nil and the renderer uses exec.Command.
type Renderer struct {
	BaseDir      string
	ExecCommand  func(name string, args ...string) *exec.Cmd
	SigTermDelay time.Duration
}

// Render writes the manifest's parameters file, populated from inputs, to the
// path declared by the manifest's TemplateSpec (resolved against BaseDir).
//
// The file is byte-deterministic: keys appear in manifest-field order, values
// are encoded with stable formatting, and the file ends with a trailing
// newline. That deterministic shape is what lets a hand-edited parameters.json
// and a Render() output compare byte-for-byte.
func (r *Renderer) Render(m *manifest.Manifest, inputs map[string]any) error {
	if m == nil {
		return errors.New("cfn: manifest is nil")
	}
	if m.Template == nil {
		return errors.New("cfn: manifest has no template spec")
	}
	if m.Template.ParametersFile == "" {
		return errors.New("cfn: manifest template.parameters_file is empty")
	}

	data, err := MarshalParameters(m.Form, inputs)
	if err != nil {
		return fmt.Errorf("cfn: marshal parameters: %w", err)
	}

	path := r.resolve(m.Template.ParametersFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("cfn: create parameters dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("cfn: write parameters: %w", err)
	}
	return nil
}

// Deploy spawns the manifest's deploy.script with the supplied env and returns
// a channel of captured stdout/stderr lines plus a Wait function that returns
// the final exit error (nil on a clean zero-exit).
//
// The renderer also adds TEMPLATE_PATH and PARAMETERS_PATH to env, resolved
// against BaseDir, so the script can locate its inputs without re-implementing
// path resolution.
//
// Cancellation: when ctx is cancelled the renderer sends SIGTERM to the
// subprocess, then SIGKILL after SigTermDelay if the process hasn't exited.
// The Lines channel is closed once the process and its output pumps are done.
func (r *Renderer) Deploy(
	ctx context.Context,
	m *manifest.Manifest,
	env map[string]string,
) (<-chan Line, func() error, error) {
	if m == nil || m.Deploy == nil || m.Deploy.Script == "" {
		return nil, nil, errors.New("cfn: manifest has no deploy.script")
	}
	if m.Deploy.Driver != "" && m.Deploy.Driver != "script" {
		return nil, nil, fmt.Errorf("cfn: unsupported deploy driver %q (only script is implemented in MVP 1)", m.Deploy.Driver)
	}

	scriptPath := r.resolve(m.Deploy.Script)
	templatePath := ""
	parametersPath := ""
	if m.Template != nil {
		templatePath = r.resolve(m.Template.Path)
		parametersPath = r.resolve(m.Template.ParametersFile)
	}

	cmd := r.exec(scriptPath)
	cmd.Env = buildEnv(env, templatePath, parametersPath)
	cmd.Dir = r.BaseDir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("cfn: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("cfn: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("cfn: start deploy script: %w", err)
	}

	lines := make(chan Line)
	var pumps sync.WaitGroup
	pumps.Add(2)
	go r.pump(ctx, stdout, StdoutLine, lines, &pumps)
	go r.pump(ctx, stderr, StderrLine, lines, &pumps)

	// Signal handler: SIGTERM on ctx.Done, escalating to SIGKILL after the
	// grace period. The handler exits cleanly once the process exits.
	signalDone := make(chan struct{})
	go r.handleCancel(ctx, cmd, signalDone)

	waitErr := make(chan error, 1)
	go func() {
		pumps.Wait()
		close(lines)
		err := cmd.Wait()
		close(signalDone) // releases the signal goroutine
		waitErr <- err
	}()

	wait := func() error {
		return <-waitErr
	}
	return lines, wait, nil
}

func (r *Renderer) resolve(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(r.BaseDir, p)
}

func (r *Renderer) exec(name string, args ...string) *exec.Cmd {
	if r.ExecCommand != nil {
		return r.ExecCommand(name, args...)
	}
	return exec.Command(name, args...)
}

// pump scans rc one line at a time and forwards each line on out, tagged with
// src. It exits when rc reaches EOF, ctx is cancelled, or the consumer of out
// stops reading (which the cancellation arm covers — without it, a wedged
// consumer would back up the OS pipe and lock the subprocess).
func (r *Renderer) pump(
	ctx context.Context,
	rc io.ReadCloser,
	src LineSource,
	out chan<- Line,
	wg *sync.WaitGroup,
) {
	defer wg.Done()
	defer rc.Close()
	scan := bufio.NewScanner(rc)
	scan.Buffer(make([]byte, 64*1024), 1024*1024)
	for scan.Scan() {
		select {
		case out <- Line{Time: time.Now(), Source: src, Text: scan.Text()}:
		case <-ctx.Done():
			return
		}
	}
}

// handleCancel sends SIGTERM (then SIGKILL after SigTermDelay) when ctx is
// cancelled. It exits when done is closed, which Deploy does after the
// process has exited cleanly.
func (r *Renderer) handleCancel(ctx context.Context, cmd *exec.Cmd, done <-chan struct{}) {
	select {
	case <-done:
		return
	case <-ctx.Done():
	}

	delay := r.SigTermDelay
	if delay <= 0 {
		delay = DefaultSigTermDelay
	}

	if cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}

	select {
	case <-done:
		return
	case <-time.After(delay):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
}

// buildEnv merges the manifest-derived env with TEMPLATE_PATH / PARAMETERS_PATH
// and the host environment. Manifest entries come last so they win on any
// name collision against the host env, which is the principle of least
// surprise: the manifest is authoritative.
func buildEnv(env map[string]string, templatePath, parametersPath string) []string {
	merged := append([]string(nil), os.Environ()...)
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys) // child processes don't care, tests do
	for _, k := range keys {
		merged = append(merged, k+"="+env[k])
	}
	if templatePath != "" {
		merged = append(merged, "TEMPLATE_PATH="+templatePath)
	}
	if parametersPath != "" {
		merged = append(merged, "PARAMETERS_PATH="+parametersPath)
	}
	return merged
}

// MarshalParameters serialises inputs to the parameters.json shape the deploy
// scripts in this repo consume: a JSON object whose keys appear in
// manifest-field order and whose values are stringified scalars or arrays of
// strings.
//
// Fields that have no entry in inputs are omitted. Fields not declared in the
// schema are ignored — the engine validates inputs against the schema before
// rendering, so untracked keys here would be a programming error in the
// engine, not a manifest concern.
func MarshalParameters(fields []manifest.Field, inputs map[string]any) ([]byte, error) {
	type pair struct {
		key string
		val json.RawMessage
	}
	pairs := make([]pair, 0, len(fields))
	for _, f := range fields {
		v, ok := inputs[f.ID]
		if !ok {
			continue
		}
		raw, err := encodeValue(v)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", f.ID, err)
		}
		pairs = append(pairs, pair{f.ID, raw})
	}

	var buf bytes.Buffer
	buf.WriteString("{\n")
	for i, p := range pairs {
		keyJSON, err := json.Marshal(p.key)
		if err != nil {
			return nil, fmt.Errorf("encode key %q: %w", p.key, err)
		}
		buf.WriteString("  ")
		buf.Write(keyJSON)
		buf.WriteString(": ")
		buf.Write(p.val)
		if i < len(pairs)-1 {
			buf.WriteString(",")
		}
		buf.WriteString("\n")
	}
	buf.WriteString("}\n")
	return buf.Bytes(), nil
}

// encodeValue returns the JSON representation of a single input value:
// strings as JSON strings, []string as JSON arrays of strings, scalar numbers
// and bools stringified (matching the existing deploy.sh convention of
// quoting every primitive). Unknown types fall back to json.Marshal.
func encodeValue(v any) (json.RawMessage, error) {
	switch x := v.(type) {
	case string:
		return json.Marshal(x)
	case []string:
		return marshalStringArray(x)
	case []any:
		strs := make([]string, len(x))
		for i, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("array element %d is %T, want string", i, e)
			}
			strs[i] = s
		}
		return marshalStringArray(strs)
	case int:
		return json.Marshal(fmt.Sprintf("%d", x))
	case int64:
		return json.Marshal(fmt.Sprintf("%d", x))
	case bool:
		if x {
			return json.RawMessage(`"true"`), nil
		}
		return json.RawMessage(`"false"`), nil
	case nil:
		return nil, errors.New("value is nil")
	default:
		return json.Marshal(v)
	}
}

// marshalStringArray emits a multi-line array of strings, indented two spaces
// deeper than the enclosing object key — matching the hand-edited shape from
// featureDetails §4.4.
func marshalStringArray(xs []string) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("[\n")
	for i, s := range xs {
		bs, err := json.Marshal(s)
		if err != nil {
			return nil, err
		}
		b.WriteString("    ")
		b.Write(bs)
		if i < len(xs)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("  ]")
	return b.Bytes(), nil
}
