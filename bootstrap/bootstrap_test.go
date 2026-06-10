package bootstrap

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/bannaarr01/packwright/internal/usage"
	pwlog "github.com/bannaarr01/packwright/log"
)

// TestInitOpensBothLogs verifies that Init creates the operational log
// (packwright.log) and the usage log (usage.jsonl) under the resolved
// home, and rewires slog.Default to the operational logger.
func TestInitOpensBothLogs(t *testing.T) {
	home := t.TempDir()
	withHome(t, home)
	withSlogDefault(t)
	withUsageDefault(t)
	withLogDefault(t)

	logger := Init("test")
	if logger == nil {
		t.Fatal("Init returned nil logger")
	}
	if logger != slog.Default() {
		t.Errorf("returned logger does not match slog.Default()")
	}
	if logger != pwlog.Default {
		t.Errorf("slog.Default was not rewired to log.Default")
	}

	// Emit one record on each pipeline so the files exist on disk.
	slog.Info("op-log-hello")
	if err := usage.Record(usage.UsageEvent{
		Command: "/bootstrap-test",
		Kind:    usage.KindResource,
		Outcome: usage.OutcomeSuccess,
		Surface: usage.SurfaceTUI,
		Version: "test",
	}); err != nil {
		t.Fatalf("usage.Record: %v", err)
	}

	opLog := filepath.Join(home, "logs", "packwright.log")
	if _, err := os.Stat(opLog); err != nil {
		t.Errorf("packwright.log not created: %v", err)
	}
	usageLog := filepath.Join(home, "logs", usage.Filename)
	if _, err := os.Stat(usageLog); err != nil {
		t.Errorf("usage.jsonl not created: %v", err)
	}
}

// TestInitMissingHomeFallsBackToStderr asserts that when home resolution
// fails, Init returns the stdlib default rather than crashing. The
// fallback is essential for cases like sandboxed CI environments where
// no home directory is available.
func TestInitMissingHomeFallsBackToStderr(t *testing.T) {
	withSlogDefault(t)
	withUsageDefault(t)
	withLogDefault(t)
	// Force home resolution to fail by clearing every env var Home consults.
	t.Setenv("PACKWRIGHT_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", "")
	}

	logger := Init("test")
	if logger == nil {
		t.Fatal("Init returned nil logger when home unresolved")
	}
	// slog.Default must NOT have been swapped to pwlog.Default — log.Init
	// was never reached because home resolution failed first.
	if slog.Default() == pwlog.Default {
		t.Errorf("slog.Default was rewired despite home resolution failure")
	}
}

// TestParseLevel covers the four valid level strings and the fallback.
func TestParseLevel(t *testing.T) {
	cases := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"", slog.LevelInfo},
		{"   warn  ", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"nonsense", slog.LevelInfo},
	}
	for _, tc := range cases {
		if got := parseLevel(tc.in); got != tc.want {
			t.Errorf("parseLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// --- helpers ---

// withHome pins config.Home() to the given directory by setting
// PACKWRIGHT_HOME.
func withHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PACKWRIGHT_HOME", dir)
}

// withSlogDefault snapshots slog.Default and restores it after the test.
func withSlogDefault(t *testing.T) {
	t.Helper()
	orig := slog.Default()
	t.Cleanup(func() { slog.SetDefault(orig) })
}

// withLogDefault snapshots pwlog.Default and restores it after the test.
func withLogDefault(t *testing.T) {
	t.Helper()
	orig := pwlog.Default
	t.Cleanup(func() { pwlog.Default = orig })
}

// withUsageDefault snapshots usage.Default and restores it after the test.
func withUsageDefault(t *testing.T) {
	t.Helper()
	orig := usage.Default
	t.Cleanup(func() { usage.Default = orig })
}
