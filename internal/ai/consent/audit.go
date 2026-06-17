package consent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Filename is the on-disk name of the consent audit log. Exported
// so the bug-report attach flow and the user can locate the file
// without duplicating the constant.
const Filename = "audit.jsonl"

// Subdir is the directory inside the Packwright home that holds AI
// artefacts. audit.jsonl lives at <home>/<Subdir>/<Filename>,
// matching the path documented in ADR-0036.
const Subdir = "ai"

// auditRecord is the on-disk JSONL schema. JSON tags are exact —
// the schema is a public contract consumed by organisations that
// audit AI activity, so renaming a field is a breaking change.
type auditRecord struct {
	Time       string `json:"time"`
	SessionID  string `json:"session_id"`
	Tool       string `json:"tool"`
	ArgsHash   string `json:"args_hash"`
	Decision   string `json:"decision"`
	UserReason string `json:"user_reason,omitempty"`
}

// auditMu serialises writer reassignment and record encoding. A
// JSONL sink needs serial writes anyway; holding the lock around
// the encode is simpler than threading a sync.Mutex through the
// hot path.
var auditMu sync.Mutex

// auditWriter is the active sink. Pre-InitAudit it is io.Discard so
// callers that emit decisions before bootstrap (tests, early init)
// silently no-op rather than crashing.
var auditWriter io.Writer = io.Discard

// InitAudit opens <homeDir>/ai/audit.jsonl in append-only mode and
// routes every subsequent Gate decision there. Repeated InitAudit
// calls are safe — the previous file handle is closed first.
func InitAudit(homeDir string) error {
	dir := filepath.Join(homeDir, Subdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("consent: create %q: %w", dir, err)
	}
	path := filepath.Join(dir, Filename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("consent: open %q: %w", path, err)
	}
	auditMu.Lock()
	if closer, ok := auditWriter.(io.Closer); ok && auditWriter != io.Discard {
		_ = closer.Close()
	}
	auditWriter = f
	auditMu.Unlock()
	return nil
}

// SetAuditWriter installs w as the audit sink. Tests use it to
// redirect to a bytes.Buffer; production code should call InitAudit
// instead.
func SetAuditWriter(w io.Writer) {
	auditMu.Lock()
	if closer, ok := auditWriter.(io.Closer); ok && auditWriter != io.Discard {
		_ = closer.Close()
	}
	auditWriter = w
	auditMu.Unlock()
}

// recordAudit appends one record describing decision to the active
// audit sink. Failures are logged at warn level but never surfaced
// to the caller — a write tool's decision is the authoritative
// outcome, and a broken audit log must not prevent a denied call
// from being denied or an approved one from being approved.
func recordAudit(req Request, decision Decision) {
	rec := auditRecord{
		Time:       Now().UTC().Format(time.RFC3339),
		SessionID:  SessionID(),
		Tool:       req.Tool,
		ArgsHash:   "sha256:" + argsHash(req.Args),
		Decision:   decision.String(),
		UserReason: req.UserReason,
	}
	auditMu.Lock()
	err := json.NewEncoder(auditWriter).Encode(rec)
	auditMu.Unlock()
	if err != nil {
		slog.Warn("ai.consent: audit write failed",
			slog.String("tool", req.Tool),
			slog.Any("err", err),
		)
	}
}

// argsHash returns the hex sha256 of args. An empty payload hashes
// to a stable, well-known constant — audit lines for "no args"
// calls can therefore be filtered by hash without inspecting the
// payload.
func argsHash(args []byte) string {
	h := sha256.Sum256(args)
	return hex.EncodeToString(h[:])
}
