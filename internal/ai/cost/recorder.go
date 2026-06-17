package cost

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

// Filename is the on-disk name of the AI usage log. It mirrors the
// MVP-4 usage package (see internal/usage) intentionally — same naming,
// same JSONL contract — but the file lives under <home>/ai/ rather
// than <home>/logs/ so the operational log redactor (which scans
// <home>/logs/*) never crosses paths with it. ADR-0039 §"Always-visible
// cost meter" pins the path.
const Filename = "usage.jsonl"

// subdir is the home-relative directory that holds AI-specific state.
// Exposed as a constant so tests and the bug-report flow can find the
// file without re-deriving the path.
const Subdir = "ai"

// Rotation policy: small file, few backups, no gzip — mirrors the
// existing usage logger. Per-turn rows are tiny (~150 bytes) so a 5 MB
// file holds tens of thousands of turns.
const (
	rotateMaxMB      = 5
	rotateMaxBackups = 3
)

// UsageRecord is the on-disk schema for one row of usage.jsonl. The
// struct is the sanitization boundary: only these eight fields can
// ever reach the file because the slog handler is wired to emit
// exactly these attributes. Adding a field here is a schema change;
// adding a field elsewhere is a no-op.
//
// All monetary values are USD per ADR-0039; the currency is fixed at
// the AI panel and the pricing layer rather than per-row.
type UsageRecord struct {
	// Timestamp is when the turn completed. A zero value is replaced
	// with time.Now().UTC() by [Recorder.Record].
	Timestamp time.Time
	// SessionID is the conversation/session this turn belongs to —
	// the same id the chat panel uses to namespace persistence.
	SessionID string
	// RequestID is the per-turn id used by the EventBus.
	RequestID string
	// Provider and Model identify the LLM that handled this turn.
	Provider string
	Model    string
	// TokensIn / TokensOut are the actual counts reported by the
	// provider on completion.
	TokensIn  int
	TokensOut int
	// USD is the dollar cost of this turn: tokens_in × input_per_1k +
	// tokens_out × output_per_1k.
	USD float64
}

// Recorder writes [UsageRecord] values as one JSON object per line.
// Concurrent calls to Record are safe — both slog handlers and
// lumberjack writers are internally synchronized.
type Recorder struct {
	handler slog.Handler
}

// NewRecorder builds a Recorder over w. The handler emits only the
// documented fields: slog's built-in level and msg keys are dropped,
// and the time key is renamed to "timestamp" to match the on-disk
// schema documented in ADR-0039 / Subdir.
func NewRecorder(w io.Writer) *Recorder {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
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

// Record appends rec as a single JSONL line. Returns the handler's
// error, if any.
func (r *Recorder) Record(rec UsageRecord) error {
	ts := rec.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	// Empty message + LevelInfo are both stripped by ReplaceAttr above.
	sr := slog.NewRecord(ts, slog.LevelInfo, "", 0)
	sr.AddAttrs(
		slog.String("session_id", rec.SessionID),
		slog.String("request_id", rec.RequestID),
		slog.String("provider", rec.Provider),
		slog.String("model", rec.Model),
		slog.Int("tokens_in", rec.TokensIn),
		slog.Int("tokens_out", rec.TokensOut),
		slog.Float64("usd", rec.USD),
	)
	return r.handler.Handle(context.Background(), sr)
}

// defaultMu guards reassignment of defaultRec by InitRecorder.
var defaultMu sync.RWMutex

// defaultRec is the package-level recorder swapped in by InitRecorder.
// Until InitRecorder runs it discards every event, so any code path
// that races with startup — tests, init blocks, the early /ai setup
// wizard — is safe. It is unexported to force callers through
// [RecordUsage], which reads it under defaultMu and avoids the data
// race a direct read would have against an InitRecorder call.
var defaultRec = NewRecorder(io.Discard)

// InitRecorder opens <homeDir>/ai/usage.jsonl through a lumberjack
// writer and installs the resulting Recorder as the package default.
// The ai/ subdirectory is created if it does not already exist.
// Rotation matches the MVP-4 usage logger: 5 MB, 3 backups, no
// compression.
//
// InitRecorder takes the resolved home directory rather than calling
// config.Home so the dependency graph stays one-directional (config →
// cost, never the reverse) — same shape as usage.Init in MVP-4.
func InitRecorder(homeDir string) error {
	dir := filepath.Join(homeDir, Subdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cost: create ai dir %q: %w", dir, err)
	}
	lj := &lumberjack.Logger{
		Filename:   filepath.Join(dir, Filename),
		MaxSize:    rotateMaxMB,
		MaxBackups: rotateMaxBackups,
		Compress:   false,
	}
	defaultMu.Lock()
	defaultRec = NewRecorder(lj)
	defaultMu.Unlock()
	return nil
}

// RecordUsage forwards rec to the package-level default recorder.
// Use this entry point after [InitRecorder] has wired the home
// directory; calls before that quietly discard.
func RecordUsage(rec UsageRecord) error {
	defaultMu.RLock()
	r := defaultRec
	defaultMu.RUnlock()
	return r.Record(rec)
}
