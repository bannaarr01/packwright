// Package log is Packwright's logging package. It is a thin wrapper around
// log/slog that adds two affordances:
//
//   - A package-level Default *slog.Logger that is usable before any explicit
//     initialization, so callers can write log.Default.Info(...) from package
//     init code or unit tests without first calling Init.
//   - File-mode output backed by gopkg.in/natefinch/lumberjack.v2, which gives
//     us size-based rotation, retention, and gzip compression — the policy
//     established by ADR-0018.
//
// This package is deliberately decoupled from the config package. Init takes the
// resolved home directory as a parameter rather than importing the config
// package, which keeps the dependency graph one-directional (config → log,
// never log → config) and avoids an import cycle once the config package
// starts logging.
//
// MVP-1 baseline only: there is no redactor here yet. The redactor handler
// that scrubs AWS keys, JWTs, and secret-tagged form values from records lands
// in MVP-2 PR-06; see handler.go for the wrap point.
package log

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

// defaultMaxMB is the rotation threshold used when Options.MaxMB is zero.
// Matches the value mandated by ADR-0018.
const defaultMaxMB = 10

// Options configures a logger built by New.
type Options struct {
	// Level is the minimum level to emit. Records below Level are dropped.
	Level slog.Level
	// File is the path to write logs to. The empty string routes output to
	// os.Stderr instead, which is the right default for short-lived
	// processes and tests.
	File string
	// Format selects the slog handler. "json" yields slog.NewJSONHandler;
	// anything else (including the empty string) yields slog.NewTextHandler.
	Format string
	// MaxMB is the rotation size in megabytes for File-mode logs. A zero or
	// negative value falls back to defaultMaxMB (10). Ignored when File is
	// empty, since stderr is not rotated.
	MaxMB int
}

// New returns a *slog.Logger built from opts. When opts.File is empty the
// logger writes to os.Stderr; otherwise it writes to opts.File via a rotating
// lumberjack.Logger (5 backups, gzip-compressed, rotating at opts.MaxMB or
// defaultMaxMB).
//
// The returned logger captures os.Stderr by value at call time when File is
// empty, so swapping os.Stderr after New returns has no effect on this logger.
func New(opts Options) *slog.Logger {
	var w io.Writer
	if opts.File == "" {
		w = os.Stderr
	} else {
		maxMB := opts.MaxMB
		if maxMB <= 0 {
			maxMB = defaultMaxMB
		}
		w = &lumberjack.Logger{
			Filename:   opts.File,
			MaxSize:    maxMB,
			MaxBackups: 5,
			Compress:   true,
		}
	}
	return slog.New(newHandler(w, opts.Level, opts.Format))
}

// Default is the package-level logger. It is initialized eagerly to a
// stderr-text logger at info level so any code path — including init
// functions and tests — can call log.Default.Info(...) before main has had a
// chance to call Init.
//
// Init reassigns Default; treat it as a normal variable read, not as a
// constant.
var Default = New(Options{Level: slog.LevelInfo, Format: "text"})

// Init reconfigures Default to write structured logs to
// <homeDir>/logs/packwright.log at the given level and format ("json" or
// "text"). It creates the logs directory if it does not already exist.
//
// Init does not import the config package. The caller — typically main, after
// loading config — passes the resolved home directory in. This keeps the
// dependency direction one-way (config depends on log, never the reverse) and
// preserves the cycle break called out in feature/mvp1/plan/03-logging.md.
func Init(homeDir string, level slog.Level, format string) error {
	logsDir := filepath.Join(homeDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return fmt.Errorf("create logs dir %q: %w", logsDir, err)
	}
	Default = New(Options{
		Level:  level,
		File:   filepath.Join(logsDir, "packwright.log"),
		Format: format,
	})
	return nil
}
