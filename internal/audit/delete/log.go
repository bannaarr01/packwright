package delete

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// LogFilename is the on-disk name of the deletion audit log.
const LogFilename = "deletions.jsonl"

// Outcome is the post-call status recorded for each row.
type Outcome string

// Outcome values written to deletions.jsonl. These are stable strings
// — organisations that audit Packwright deletions parse the file by
// these literals, so changes are breaking.
const (
	// OutcomeDeleted means the AWS Delete* call returned without error.
	OutcomeDeleted Outcome = "deleted"
	// OutcomeFailed means the AWS Delete* call returned an error;
	// the row remains live in AWS.
	OutcomeFailed Outcome = "failed"
	// OutcomeSkipped means the executor did not issue a Delete* call
	// at all (row unselected, blocked, or cancelled).
	OutcomeSkipped Outcome = "skipped"
)

// LogEntry is the on-disk JSONL schema. JSON tags are fixed by
// ADR-0043's audit contract; renaming a field is a breaking change.
type LogEntry struct {
	// Time is the RFC3339 UTC timestamp of the entry.
	Time string `json:"time"`
	// RowID is the tray row this entry refers to.
	RowID string `json:"row_id"`
	// Kind is the resource kind (ec2/volume, ...).
	Kind string `json:"kind"`
	// Identifier is the AWS handle of the resource.
	Identifier string `json:"identifier"`
	// Account, Region, Profile name the AWS surface the call hit.
	Account string `json:"account,omitempty"`
	Region  string `json:"region,omitempty"`
	Profile string `json:"profile,omitempty"`
	// ConsentHash is the sha256 of the Batch contents the user
	// confirmed; lets an auditor pin this entry to the exact set
	// of decisions and typed confirmation.
	ConsentHash string `json:"consent_hash"`
	// Outcome is one of OutcomeDeleted / OutcomeFailed /
	// OutcomeSkipped.
	Outcome Outcome `json:"outcome"`
	// Reason explains the outcome:
	//   deleted: empty
	//   failed:  the AWS error message
	//   skipped: "unselected" | "blocked" | "cancelled"
	Reason string `json:"reason,omitempty"`
}

// LogWriter appends LogEntry records to deletions.jsonl. The
// canonical implementation is *FileLog; tests use *WriterLog
// backed by a bytes.Buffer so they do not touch disk.
//
// Write must be safe to call concurrently. A failed write is
// surfaced to the executor, which logs but does not abort — a
// broken audit log is bad, but not as bad as failing to delete a
// resource the user already paid the consent cost for.
type LogWriter interface {
	Write(entry LogEntry) error
}

// FileLog appends to <home>/audit/deletions.jsonl. Constructed by
// OpenLog. Safe for concurrent use across goroutines.
type FileLog struct {
	mu sync.Mutex
	w  io.WriteCloser
}

// OpenLog opens (creating if missing) <homeDir>/audit/deletions.jsonl
// in append mode. The same audit/ subdir as the tray is used; if
// OpenTray has not been called yet the directory is created here.
func OpenLog(homeDir string) (*FileLog, error) {
	if homeDir == "" {
		return nil, errors.New("delete: OpenLog: homeDir is empty")
	}
	dir := filepath.Join(homeDir, Subdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("delete: create %q: %w", dir, err)
	}
	path := filepath.Join(dir, LogFilename)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("delete: open %q: %w", path, err)
	}
	return &FileLog{w: f}, nil
}

// Write appends entry as a single JSONL line.
func (l *FileLog) Write(entry LogEntry) error {
	if entry.Time == "" {
		entry.Time = time.Now().UTC().Format(time.RFC3339)
	}
	buf, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("delete: encode log entry: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.w.Write(append(buf, '\n')); err != nil {
		return fmt.Errorf("delete: write log entry: %w", err)
	}
	return nil
}

// Close releases the underlying file handle.
func (l *FileLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w == nil {
		return nil
	}
	err := l.w.Close()
	l.w = nil
	return err
}

// WriterLog wraps an io.Writer (e.g. a bytes.Buffer) so tests can
// capture log entries without touching disk.
type WriterLog struct {
	mu sync.Mutex
	W  io.Writer
}

// Write appends entry as a single JSONL line to the wrapped writer.
func (l *WriterLog) Write(entry LogEntry) error {
	if entry.Time == "" {
		entry.Time = time.Now().UTC().Format(time.RFC3339)
	}
	buf, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, err = l.W.Write(append(buf, '\n'))
	return err
}
