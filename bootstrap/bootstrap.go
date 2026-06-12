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
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/bannaarr01/packwright/action/dispatch"
	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/update"
	"github.com/bannaarr01/packwright/internal/usage"
	"github.com/bannaarr01/packwright/log"
	"github.com/bannaarr01/packwright/meta"
)

// updateCheckTimeout bounds the background launch-time GitHub probe. The
// check is best-effort and lives off the launch critical path; this
// ceiling exists so a slow GitHub doesn't leave the goroutine alive
// indefinitely after the process has otherwise quit.
const updateCheckTimeout = 5 * time.Second

// checkForUpdate is the seam through which Init fires the update probe.
// Tests assign a no-op (or a synchronous stub) here so unit tests don't
// spin up goroutines that outlive the test binary.
var checkForUpdate = runUpdateCheck

// Init performs the shared start-of-process setup and returns the logger
// every front-end should use thereafter. The returned *slog.Logger is
// the same instance as the (potentially reassigned) slog default, so
// callers can equivalently call slog.Default() from anywhere later.
//
// surface is a short label ("tui" or "gui") used as a prefix in warning
// messages so the source of any bootstrap failure is obvious in the log.
// The same label is registered with action/dispatch as the default
// usage-event surface, so command invocations are tagged correctly even
// before per-call dispatch.WithSurface plumbing exists.
func Init(surface string) *slog.Logger {
	dispatch.SetDefaultSurface(usage.Surface(surface))

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

	// The update check is the only outbound HTTP call Packwright performs
	// on its own (ADR-0030). It runs after the log destinations are open
	// so the banner's stderr fallback isn't competing with raw log lines
	// on the same stream — by here, slog.Default writes to packwright.log.
	checkForUpdate(surface, cfg)

	return slog.Default()
}

// runUpdateCheck spawns a best-effort goroutine that consults the
// GitHub Releases API once per process startup. The 24h cache lives
// inside internal/update so a TUI restart loop does not hammer GitHub.
//
// Disabled installs (cfg.DisableUpdateCheck = true OR
// PACKWRIGHT_NO_UPDATE_CHECK=1) cause CheckOnce to short-circuit; this
// helper still spawns the goroutine but it exits immediately with no
// HTTP traffic. Keeping the spawn unconditional makes the behaviour the
// same whether opt-out is set via env or via config.
func runUpdateCheck(surface string, cfg *config.Config) {
	if cfg == nil {
		return
	}
	update.Disabled = cfg.DisableUpdateCheck
	channel := update.Channel(strings.TrimSpace(cfg.UpdateChannel))
	if !update.ValidChannel(string(channel)) {
		slog.Warn(surface+": bootstrap: invalid update_channel",
			slog.String("channel", string(channel)))
		channel = update.ChannelStable
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
		defer cancel()

		latest, err := update.CheckOnce(ctx, meta.Version, channel)
		if err != nil {
			// ADR-0030: failures are silent. Record at debug only so the
			// operational log keeps a breadcrumb without nagging the user.
			slog.Debug(surface+": bootstrap: update check",
				slog.Any("err", err))
			return
		}
		if latest == nil {
			return
		}
		update.Banner(latest)
	}()
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
