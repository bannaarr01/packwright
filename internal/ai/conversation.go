package ai

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// sessionsSubdir is the directory beneath the Packwright home where session
// JSONL files live: <home>/ai/sessions/<session-id>.jsonl. The "ai/" parent
// is created on first append; config.Home() does not list it as a standard
// subdirectory because it is owned by this package.
const sessionsSubdir = "ai/sessions"

// sessionFileExt is the on-disk suffix for a session file.
const sessionFileExt = ".jsonl"

// Turn is one entry in an AI conversation. PR-01 keeps the shape minimal —
// just the fields any plausible turn needs (when, who, what) plus an
// extensible Meta bag — so PR-02 can attach tool calls, token usage, or
// provider-specific deltas to existing sessions without rewriting the file
// format. Existing files remain forward-compatible: unknown Meta keys
// round-trip through json.Unmarshal verbatim.
type Turn struct {
	// Timestamp is when the turn was produced. AppendTurn defaults a zero
	// value to time.Now().UTC() so callers can leave it unset.
	Timestamp time.Time `json:"timestamp"`
	// Role is the actor: "user", "assistant", "system", or "tool".
	// PR-01 does not constrain the set; PR-02 will tighten it once the
	// provider abstraction lands.
	Role string `json:"role"`
	// Content is the human-readable text of the turn. Tool calls and
	// structured payloads go in Meta until PR-02 defines their schema.
	Content string `json:"content"`
	// Meta is an extensible bag for provider-specific or PR-02+
	// additions. The "omitempty" tag keeps PR-01 files clean (no empty
	// "meta": {} on every line).
	Meta map[string]any `json:"meta,omitempty"`
}

// appendMu serializes concurrent AppendTurn calls for the same session. The
// kernel guarantees O_APPEND writes <= PIPE_BUF are atomic on POSIX, but our
// JSON lines can exceed that bound; serialising in-process keeps the file
// strictly JSONL-parseable under concurrent producers.
var appendMu sync.Mutex

// NewSessionID returns a fresh session id of the form
// "20260617T123456-1a2b3c4d". Sortable by creation time, with a short
// crypto/rand suffix to avoid collisions when two sessions start in the same
// second. The crypto/rand dependency is satisfied by every supported
// platform, so callers can treat the error as fatal.
func NewSessionID() (string, error) {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("ai: NewSessionID: read random: %w", err)
	}
	ts := time.Now().UTC().Format("20060102T150405")
	return ts + "-" + hex.EncodeToString(buf[:]), nil
}

// AppendTurn writes turn as a single JSONL line to
// <homeDir>/ai/sessions/<sessionID>.jsonl, creating the directory tree on
// first use. Concurrent calls are safe — see appendMu.
//
// homeDir is taken as a parameter (rather than calling config.Home()
// internally) so tests can drive the function with t.TempDir() and so the
// package never carries hidden global state. The same pattern is used by
// internal/usage.Init.
func AppendTurn(homeDir, sessionID string, turn Turn) error {
	path, err := sessionPath(homeDir, sessionID)
	if err != nil {
		return err
	}
	if turn.Timestamp.IsZero() {
		turn.Timestamp = time.Now().UTC()
	}
	line, err := json.Marshal(turn)
	if err != nil {
		return fmt.Errorf("ai: AppendTurn: marshal: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("ai: AppendTurn: mkdir %q: %w", filepath.Dir(path), err)
	}

	appendMu.Lock()
	defer appendMu.Unlock()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("ai: AppendTurn: open %q: %w", path, err)
	}
	defer f.Close()

	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("ai: AppendTurn: write %q: %w", path, err)
	}
	return nil
}

// LoadSession reads every turn in the session file in append order. A
// missing session file is not an error — it returns nil, nil — so callers
// can probe for "is there a conversation here?" without a stat first.
//
// Malformed lines (truncated, mid-write, manually edited) fail the load with
// a wrapped error rather than being skipped silently; a partial conversation
// is worse than a clear error in this context.
func LoadSession(homeDir, sessionID string) ([]Turn, error) {
	path, err := sessionPath(homeDir, sessionID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("ai: LoadSession: open %q: %w", path, err)
	}
	defer f.Close()

	var turns []Turn
	scanner := bufio.NewScanner(f)
	// Allow larger than the default 64 KiB line in case a single turn
	// embeds a long log excerpt. 1 MiB is plenty for human-readable
	// conversation; tool-call payloads in PR-02 will encode through Meta
	// and stay well under this.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	line := 0
	for scanner.Scan() {
		line++
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var t Turn
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("ai: LoadSession: %q line %d: %w", path, line, err)
		}
		turns = append(turns, t)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ai: LoadSession: scan %q: %w", path, err)
	}
	return turns, nil
}

// sessionPath resolves and validates the on-disk path for a session id.
// Session ids are user-supplied (they come from NewSessionID today, but the
// /ai dispatcher in a later PR may resume an id from the URL or argv), so
// reject anything that could escape the sessions directory.
func sessionPath(homeDir, sessionID string) (string, error) {
	if homeDir == "" {
		return "", fmt.Errorf("ai: sessionPath: homeDir is required")
	}
	if err := validateSessionID(sessionID); err != nil {
		return "", err
	}
	return filepath.Join(homeDir, sessionsSubdir, sessionID+sessionFileExt), nil
}

// validateSessionID rejects empty ids and anything that contains a path
// separator or "..". This is belt-and-braces — filepath.Join would already
// confine a relative path to the sessions subtree on POSIX, but a Windows
// backslash would not, and "../../etc/passwd" should never even reach
// filepath.Join.
func validateSessionID(id string) error {
	if id == "" {
		return fmt.Errorf("ai: session id is required")
	}
	if strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("ai: session id %q contains a path separator", id)
	}
	if id == "." || id == ".." || strings.Contains(id, "..") {
		return fmt.Errorf("ai: session id %q is not a valid file name", id)
	}
	return nil
}
