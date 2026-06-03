package log

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewStderrDefault verifies that an Options with no File field routes
// output to os.Stderr. We swap os.Stderr for a pipe *before* calling New so
// the logger captures the pipe end as its writer.
func TestNewStderrDefault(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	t.Cleanup(func() {
		os.Stderr = orig
		_ = r.Close()
	})

	lg := New(Options{Level: slog.LevelInfo, Format: "text"})
	lg.Info("hello", "k", "v")

	// Close the write end so io.ReadAll on the read end terminates.
	_ = w.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stderr pipe: %v", err)
	}
	if !strings.Contains(string(got), "hello") {
		t.Errorf("stderr output missing message: %q", got)
	}
	if !strings.Contains(string(got), "k=v") {
		t.Errorf("stderr output missing attribute: %q", got)
	}
}

// TestNewWritesFile verifies that file mode actually writes to disk through
// lumberjack and that attributes round-trip in text format.
func TestNewWritesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "packwright.log")
	lg := New(Options{Level: slog.LevelInfo, File: path, Format: "text"})
	lg.Info("hello", "k", "v")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), "hello") {
		t.Errorf("log file missing message: %q", data)
	}
	if !strings.Contains(string(data), "k=v") {
		t.Errorf("log file missing attribute: %q", data)
	}
}

// TestLevelFiltering confirms that records below Options.Level never reach
// the writer.
func TestLevelFiltering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "packwright.log")
	lg := New(Options{Level: slog.LevelWarn, File: path, Format: "text"})
	lg.Info("dropped")
	lg.Warn("kept")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	s := string(data)
	if strings.Contains(s, "dropped") {
		t.Errorf("info record leaked past level filter: %q", s)
	}
	if !strings.Contains(s, "kept") {
		t.Errorf("warn record missing from output: %q", s)
	}
}

// TestJSONFormat checks that "json" format produces a parseable JSON object
// per line with the expected msg and attribute fields. This is the contract
// we promise downstream tooling (jq, grep) per ADR-0018.
func TestJSONFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "packwright.log")
	lg := New(Options{Level: slog.LevelInfo, File: path, Format: "json"})
	lg.Info("hello", "k", "v")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	line := strings.TrimSpace(string(data))
	if line == "" {
		t.Fatalf("empty log file")
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("log line is not valid JSON: %v\n%s", err, line)
	}
	if got := rec["msg"]; got != "hello" {
		t.Errorf("msg = %v, want %q", got, "hello")
	}
	if got := rec["k"]; got != "v" {
		t.Errorf("k = %v, want %q", got, "v")
	}
	if _, ok := rec["level"]; !ok {
		t.Errorf("missing level field: %q", line)
	}
}

// TestInit verifies that Init creates <homeDir>/logs/packwright.log and that
// Default is rewired to write JSON records to it.
func TestInit(t *testing.T) {
	origDefault := Default
	t.Cleanup(func() { Default = origDefault })

	home := t.TempDir()
	if err := Init(home, slog.LevelInfo, "json"); err != nil {
		t.Fatalf("Init: %v", err)
	}

	Default.Info("init-test", "x", 1)

	path := filepath.Join(home, "logs", "packwright.log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	line := strings.TrimSpace(string(data))
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("log line is not valid JSON: %v\n%s", err, line)
	}
	if got := rec["msg"]; got != "init-test" {
		t.Errorf("msg = %v, want %q", got, "init-test")
	}
}

// TestInitMakesLogsDir verifies that Init creates the logs directory even
// when the home dir exists but logs/ does not.
func TestInitMakesLogsDir(t *testing.T) {
	origDefault := Default
	t.Cleanup(func() { Default = origDefault })

	home := t.TempDir()
	logsDir := filepath.Join(home, "logs")
	if _, err := os.Stat(logsDir); !os.IsNotExist(err) {
		t.Fatalf("logs dir already exists before Init: %v", err)
	}

	if err := Init(home, slog.LevelInfo, "text"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := os.Stat(logsDir); err != nil {
		t.Errorf("logs dir not created by Init: %v", err)
	}
}
