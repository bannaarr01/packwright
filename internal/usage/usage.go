// Package usage implements Packwright's local-only usage log per ADR-0031.
//
// The package writes one JSON object per line to
// <homeDir>/logs/usage.jsonl through a lumberjack-rotated writer (5 MB,
// 3 backups, no compression — these files are tiny). It is deliberately
// separate from the operational log (the log package):
//
//   - Different file, different rotation policy.
//   - Different slog handler — no redactor wraps this stream. The
//     operational redactor scans bytes for AWS-key / JWT / secret-field
//     shapes; running that over a structured usage record could mangle
//     legitimate values, and the usage schema is already free of secrets
//     by construction.
//
// Sanitization is type-level: UsageEvent is a fixed struct whose seven
// fields are the only ones the slog handler ever sees. Callers cannot
// smuggle extra attributes through the API. The handler additionally
// drops slog's built-in `level` / `msg` keys and renames `time` to
// `timestamp` so the emitted JSON matches the documented schema exactly.
//
// No outbound calls. This package does not import net/http — there is
// no network code path of any kind. usage_test.go pins this guarantee
// with an integration test that replaces http.DefaultTransport with a
// panic-on-use sentinel and asserts the sentinel is never touched.
package usage

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Filename is the on-disk name of the usage log. Exported so tests and
// the bug-report flow (ADR-0031 §"Voluntary attach-on-bug-report") can
// locate the file without duplicating the constant.
const Filename = "usage.jsonl"

// Rotation policy from ADR-0031: small file, few backups, no gzip.
const (
	rotateMaxMB      = 5
	rotateMaxBackups = 3
)

// Kind is the runner family a slash command dispatches to. The values
// mirror the four runner kinds wired up in MVP-1/2/3 (resource, shell,
// monitor, composite).
type Kind string

const (
	KindResource  Kind = "resource"
	KindShell     Kind = "shell"
	KindMonitor   Kind = "monitor"
	KindComposite Kind = "composite"
)

// Outcome is the terminal state of a command invocation.
type Outcome string

const (
	OutcomeSuccess   Outcome = "success"
	OutcomeFailed    Outcome = "failed"
	OutcomeCancelled Outcome = "cancelled"
)

// Surface identifies which front-end the user was driving when the
// command ran.
type Surface string

const (
	SurfaceTUI Surface = "tui"
	SurfaceGUI Surface = "gui"
)

// UsageEvent is the complete schema written to usage.jsonl. The struct
// is the sanitization boundary — slog only emits attributes derived
// from these fields, so unexpected keys cannot reach disk.
type UsageEvent struct {
	// Timestamp is when the command completed. A zero value is replaced
	// with time.Now().UTC() inside Record so callers can leave it unset
	// for simple invocations.
	Timestamp time.Time
	// Command is the slash-command name (e.g. "/deploy-stack"). No
	// arguments, no form values — see ADR-0031 for the exclusion list.
	Command string
	// Kind is the runner family. See the Kind* constants.
	Kind Kind
	// Duration is wall-clock time spent in the runner. Emitted as
	// duration_ms.
	Duration time.Duration
	// Outcome is success / failed / cancelled.
	Outcome Outcome
	// Surface is tui / gui.
	Surface Surface
	// Version is the Packwright build version (cmd.version).
	Version string
}

// Recorder writes UsageEvents through a slog JSONHandler. Concurrent
// calls to Record are safe — slog handlers and lumberjack writers are
// both internally synchronized.
type Recorder struct {
	handler slog.Handler
}

// NewRecorder builds a Recorder that writes JSON-encoded UsageEvents to
// w. The handler is configured to emit only the seven documented fields:
// the standard slog `level` and `msg` keys are dropped, and `time` is
// renamed to `timestamp` to match ADR-0031.
func NewRecorder(w io.Writer) *Recorder {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Only mutate top-level keys; nested groups (we have none
			// today, but be defensive) pass through unchanged.
			if len(groups) > 0 {
				return a
			}
			switch a.Key {
			case slog.LevelKey, slog.MessageKey:
				return slog.Attr{}
			case slog.TimeKey:
				return slog.Attr{Key: "timestamp", Value: a.Value}
			}
			return a
		},
	})
	return &Recorder{handler: h}
}

// Record writes ev as a single JSONL line. A zero Timestamp is filled
// with the current UTC time so callers do not have to stamp every
// event. Returns the handler's error, if any.
func (r *Recorder) Record(ev UsageEvent) error {
	ts := ev.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	// Empty message — slog.JSONHandler with the ReplaceAttr above will
	// drop the msg key entirely. We pass LevelInfo for the same reason
	// (the key is dropped before serialization).
	rec := slog.NewRecord(ts, slog.LevelInfo, "", 0)
	rec.AddAttrs(
		slog.String("command", ev.Command),
		slog.String("kind", string(ev.Kind)),
		slog.Int64("duration_ms", ev.Duration.Milliseconds()),
		slog.String("outcome", string(ev.Outcome)),
		slog.String("surface", string(ev.Surface)),
		slog.String("version", ev.Version),
	)
	return r.handler.Handle(context.Background(), rec)
}

// defaultMu guards reassignment of Default by Init.
var defaultMu sync.RWMutex

// Default is the package-level recorder. Prior to Init it discards
// every event so callers can safely invoke Record from package init
// code, unit tests, or any path that runs before main wires up the
// home directory.
var Default = NewRecorder(io.Discard)

// Init opens <homeDir>/logs/usage.jsonl through a lumberjack writer and
// installs the resulting Recorder as Default. The logs directory is
// created if it does not already exist; rotation matches ADR-0031
// (5 MB, 3 backups, no compression).
//
// Init does not import the config package — it takes the resolved home
// directory as a parameter — for the same reason log.Init does: keep
// the dependency graph one-directional (config → usage, never the
// reverse).
func Init(homeDir string) error {
	logsDir := filepath.Join(homeDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return fmt.Errorf("usage: create logs dir %q: %w", logsDir, err)
	}
	lj := &lumberjack.Logger{
		Filename:   filepath.Join(logsDir, Filename),
		MaxSize:    rotateMaxMB,
		MaxBackups: rotateMaxBackups,
		Compress:   false,
	}
	defaultMu.Lock()
	Default = NewRecorder(lj)
	defaultMu.Unlock()
	return nil
}

// Record forwards to Default.Record. This is the entry point callers
// use after Init has wired the home directory.
func Record(ev UsageEvent) error {
	defaultMu.RLock()
	r := Default
	defaultMu.RUnlock()
	return r.Record(ev)
}
