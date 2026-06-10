// Package bootstrap is the shared start-of-process setup for Packwright's
// front-ends (TUI and GUI). Both launchers must materialize the home
// directory, open the operational log (packwright.log), wire that logger
// in as slog.Default so every package that calls slog.Default() picks it
// up, and open the usage log (usage.jsonl). The work is identical for
// each surface, so it lives here rather than being duplicated.
//
// Every step is best-effort: if the home directory cannot be resolved,
// config cannot be loaded, or either log file cannot be opened, Init
// records a warning to the standard slog default (stderr text) and
// continues. A front-end is expected to start successfully even when its
// log destinations are unavailable — losing log lines is preferable to
// refusing to launch.
package bootstrap

import (
	"log/slog"
	"strings"

	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/usage"
	"github.com/bannaarr01/packwright/log"
)

// Init performs the shared start-of-process setup and returns the logger
// every front-end should use thereafter. The returned *slog.Logger is
// the same instance as the (potentially reassigned) slog default, so
// callers can equivalently call slog.Default() from anywhere later.
//
// surface is a short label ("tui" or "gui") used as a prefix in warning
// messages so the source of any bootstrap failure is obvious in the log.
func Init(surface string) *slog.Logger {
	home, err := config.Home()
	if err != nil {
		slog.Warn(surface+": bootstrap: resolve home", slog.Any("err", err))
		return slog.Default()
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Warn(surface+": bootstrap: load config", slog.Any("err", err))
		cfg = &config.Config{LogLevel: "info"}
	}

	if err := log.Init(home, parseLevel(cfg.LogLevel), "json"); err != nil {
		slog.Warn(surface+": bootstrap: log init", slog.Any("err", err))
	} else {
		// Route every package that calls slog.Default() — awsx,
		// monitor runners, GUI app, etc. — through the rotated
		// packwright.log file.
		slog.SetDefault(log.Default)
	}

	if err := usage.Init(home); err != nil {
		slog.Warn(surface+": bootstrap: usage init", slog.Any("err", err))
	}

	return slog.Default()
}

// parseLevel maps config.LogLevel ("debug" / "info" / "warn" / "error")
// to its slog equivalent. Unknown or empty values default to Info — the
// safest middle ground when config is misconfigured.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
