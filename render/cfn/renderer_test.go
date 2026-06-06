package cfn_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bannaarr01/packwright/manifest"
	"github.com/bannaarr01/packwright/render/cfn"
)

func TestMarshalParameters_FieldOrderPreserved(t *testing.T) {
	fields := []manifest.Field{
		{ID: "Beta"},
		{ID: "Alpha"},
		{ID: "Gamma"},
	}
	inputs := map[string]any{
		"Alpha": "a",
		"Beta":  "b",
		"Gamma": "g",
	}
	got, err := cfn.MarshalParameters(fields, inputs)
	if err != nil {
		t.Fatalf("MarshalParameters: %v", err)
	}
	want := "{\n  \"Beta\": \"b\",\n  \"Alpha\": \"a\",\n  \"Gamma\": \"g\"\n}\n"
	if string(got) != want {
		t.Errorf("output mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestMarshalParameters_StringArraysFormattedMultiline(t *testing.T) {
	fields := []manifest.Field{{ID: "Ids"}}
	inputs := map[string]any{"Ids": []string{"a", "b"}}
	got, err := cfn.MarshalParameters(fields, inputs)
	if err != nil {
		t.Fatalf("MarshalParameters: %v", err)
	}
	want := "{\n  \"Ids\": [\n    \"a\",\n    \"b\"\n  ]\n}\n"
	if string(got) != want {
		t.Errorf("output mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestMarshalParameters_OmitsFieldsWithoutValue(t *testing.T) {
	fields := []manifest.Field{{ID: "A"}, {ID: "B"}}
	inputs := map[string]any{"A": "x"}
	got, err := cfn.MarshalParameters(fields, inputs)
	if err != nil {
		t.Fatalf("MarshalParameters: %v", err)
	}
	if strings.Contains(string(got), "\"B\"") {
		t.Errorf("output should omit B when it has no input: %s", got)
	}
}

func TestRenderer_WritesParametersFile(t *testing.T) {
	dir := t.TempDir()
	r := &cfn.Renderer{BaseDir: dir}

	m := &manifest.Manifest{
		Kind: manifest.KindResource,
		Template: &manifest.TemplateSpec{
			Path:           "template.yaml",
			ParametersFile: "out/parameters.json",
		},
		Form: []manifest.Field{{ID: "Key"}},
	}
	if err := r.Render(m, map[string]any{"Key": "value"}); err != nil {
		t.Fatalf("Render: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "out", "parameters.json"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	want := "{\n  \"Key\": \"value\"\n}\n"
	if string(got) != want {
		t.Errorf("file contents = %q, want %q", got, want)
	}
}

func TestRenderer_DeployStreamsLinesAndExitsCleanly(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, filepath.Join(dir, "echo.sh"), `#!/bin/sh
echo "line-1"
echo "line-2"
echo "to stderr" >&2
exit 0
`)
	r := &cfn.Renderer{BaseDir: dir}
	m := &manifest.Manifest{
		Kind: manifest.KindResource,
		Template: &manifest.TemplateSpec{
			Path:           "template.yaml",
			ParametersFile: "parameters.json",
		},
		Deploy: &manifest.DeploySpec{Driver: "script", Script: "echo.sh"},
	}
	lines, wait, err := r.Deploy(context.Background(), m, map[string]string{"FOO": "bar"})
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	var stdoutLines, stderrLines []string
	for ln := range lines {
		switch ln.Source {
		case cfn.StdoutLine:
			stdoutLines = append(stdoutLines, ln.Text)
		case cfn.StderrLine:
			stderrLines = append(stderrLines, ln.Text)
		}
	}
	if err := wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if got, want := stdoutLines, []string{"line-1", "line-2"}; !equalSlice(got, want) {
		t.Errorf("stdout lines = %v, want %v", got, want)
	}
	if got, want := stderrLines, []string{"to stderr"}; !equalSlice(got, want) {
		t.Errorf("stderr lines = %v, want %v", got, want)
	}
}

func TestRenderer_DeployHonoursContextCancel(t *testing.T) {
	dir := t.TempDir()
	writeScript(t, filepath.Join(dir, "slow.sh"), `#!/bin/sh
trap 'exit 1' TERM
sleep 30
`)
	r := &cfn.Renderer{BaseDir: dir, SigTermDelay: 100 * time.Millisecond}
	m := &manifest.Manifest{
		Kind: manifest.KindResource,
		Template: &manifest.TemplateSpec{
			Path:           "template.yaml",
			ParametersFile: "parameters.json",
		},
		Deploy: &manifest.DeploySpec{Driver: "script", Script: "slow.sh"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	lines, wait, err := r.Deploy(ctx, m, nil)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	// Cancel almost immediately; the trap should make the child exit non-zero.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	for range lines {
	} // drain
	err = wait()
	if err == nil {
		t.Error("expected non-nil wait error after cancel; got nil")
	}
}

func TestRenderer_DeployRejectsUnsupportedDriver(t *testing.T) {
	r := &cfn.Renderer{BaseDir: t.TempDir()}
	m := &manifest.Manifest{
		Kind:     manifest.KindResource,
		Deploy:   &manifest.DeploySpec{Driver: "sdk", Script: "x.sh"},
		Template: &manifest.TemplateSpec{Path: "t", ParametersFile: "p"},
	}
	if _, _, err := r.Deploy(context.Background(), m, nil); err == nil {
		t.Error("Deploy should reject sdk driver in MVP 1")
	}
}

func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
