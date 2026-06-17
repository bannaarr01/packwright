// Package consent implements the per-call write-action consent flow
// defined by ADR-0036. Write tools registered by MVP-5 PR-03 invoke
// Gate before executing; Gate enforces the documented posture
// (auto-approve → session-approve → modal), records every decision
// to <home>/ai/audit.jsonl, and never approves a call without a
// non-empty AI-stated reason.
//
// The package is intentionally UI-agnostic. Two package-level
// function variables describe the surfaces the TUI and GUI override
// from their own init():
//
//   - ShowModal renders the consent dialog and returns the user's
//     decision. The default, denyModal, refuses every request — a
//     build without a front-end fails closed (ADR-0036 §"Default modal").
//   - WarningBanner surfaces the persistent red banner mandated when
//     AutoApproveTools is non-empty. The default prints a single line
//     to stderr so headless contexts still see the warning.
//
// For non-interactive tests and CI runs that need a real consent
// answer, PromptStdinModal renders a compact text version of the
// dialog and reads a one-character decision from stdin.
package consent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Decision is the outcome of a Gate call. The zero value is Deny so
// any uninitialised Decision returned by a faulty modal still fails
// closed.
type Decision int

const (
	// Deny refuses the tool call. The AI is informed so it can adapt
	// its plan; no state changes are made.
	Deny Decision = iota
	// ApproveOnce permits this single call only. The next call of
	// the same tool re-opens the modal.
	ApproveOnce
	// ApproveSession permits this tool name for the rest of the
	// session — until app restart, ResetSession, or sessionIdleTimeout
	// elapses without another consult for this tool.
	ApproveSession
)

// String returns the canonical value used in audit.jsonl.
func (d Decision) String() string {
	switch d {
	case ApproveOnce:
		return "approve_once"
	case ApproveSession:
		return "approve_session"
	default:
		return "deny"
	}
}

// Request describes a write-tool invocation awaiting consent. The
// caller fills every field before invoking Gate. Reason is required
// per ADR-0036; UserReason is filled by the modal renderer when the
// user adds free-form text to their decision.
type Request struct {
	// Tool is the namespaced tool name, e.g. "cfn/update-stack".
	Tool string
	// Account is the AWS account ID the call targets.
	Account string
	// Profile is the AWS CLI profile resolving to Account.
	Profile string
	// Region is the target AWS region, or "" for region-less tools.
	Region string
	// Resource is a short identifier of the target object — stack
	// name, ARN, file path, etc.
	Resource string
	// Args is the raw serialized argument payload. The audit log
	// records sha256(Args); the modal renderer displays it verbatim
	// (or as a unified diff, depending on tool kind).
	Args []byte
	// Reason is the AI's stated justification. ADR-0036 makes this
	// REQUIRED: Gate returns Deny without prompting when Reason is
	// empty after trimming whitespace.
	Reason string
	// BlastHint is a short auto-generated blast-radius hint to show
	// in the modal. Empty when the caller cannot estimate one.
	BlastHint string
	// UserReason is the free-text reason the user typed into the
	// modal. Filled by the modal renderer, surfaced in the audit
	// record as `user_reason`.
	UserReason string
}

// sessionIdleTimeout is the per-tool sliding window of ADR-0036.
// A session approval that has not been consulted for one hour
// silently lapses.
const sessionIdleTimeout = time.Hour

// Now is the source of time. Tests override it to advance past the
// sessionIdleTimeout without sleeping.
var Now = time.Now

// sessionMu guards sessionApprovals.
var sessionMu sync.Mutex

// sessionApprovals records the last time each session-approved tool
// was consulted. A miss or an entry older than sessionIdleTimeout
// re-opens the modal.
var sessionApprovals = map[string]time.Time{}

// autoApprove holds the list of tool names that bypass the modal.
// The list is loaded once at startup via SetAutoApprove. An
// atomic.Pointer keeps Gate's fast path lock-free.
var autoApprove atomic.Pointer[[]string]

// sessionID is the audit-log session identifier. It rotates on
// ResetSession (i.e. /ai end) so consumers can group records.
var sessionID atomic.Pointer[string]

func init() {
	empty := []string{}
	autoApprove.Store(&empty)
	id := newSessionID()
	sessionID.Store(&id)
}

// SessionID returns the current audit-log session identifier.
func SessionID() string {
	if p := sessionID.Load(); p != nil {
		return *p
	}
	return ""
}

// SetAutoApprove records the tool names that should bypass the
// consent modal. Whitespace is trimmed and duplicates dropped before
// storage. When the resulting list is non-empty, WarningBanner is
// invoked synchronously so the front-end can paint its red banner
// before any subsequent Gate call.
//
// Call once at startup after loading config.yaml. Passing nil or an
// empty slice clears the list and is a no-op for the banner.
func SetAutoApprove(tools []string) {
	out := make([]string, 0, len(tools))
	seen := map[string]struct{}{}
	for _, t := range tools {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	autoApprove.Store(&out)
	if len(out) > 0 {
		WarningBanner(out)
	}
}

// AutoApproved reports whether tool is on the auto-approve list.
func AutoApproved(tool string) bool {
	list := autoApprove.Load()
	if list == nil {
		return false
	}
	for _, t := range *list {
		if t == tool {
			return true
		}
	}
	return false
}

// ResetSession discards every session-level approval and rotates
// SessionID so subsequent audit lines carry a fresh identifier.
// /ai end and graceful shutdown both call this.
func ResetSession() {
	sessionMu.Lock()
	sessionApprovals = map[string]time.Time{}
	sessionMu.Unlock()
	id := newSessionID()
	sessionID.Store(&id)
}

// Gate is the canonical entry point used by tools.write/* before
// executing a write tool. It implements the ADR-0036 posture:
//
//  1. Reason empty (after trim) → Deny without prompting.
//  2. Tool on auto-approve list → ApproveOnce + warn-level log.
//  3. Tool has a live session approval → ApproveSession (refreshed).
//  4. Otherwise → ShowModal; remember session if the user opted in.
//
// Every outcome appends one record to audit.jsonl.
func Gate(ctx context.Context, req Request) Decision {
	if strings.TrimSpace(req.Reason) == "" {
		recordAudit(req, Deny)
		return Deny
	}
	if AutoApproved(req.Tool) {
		slog.Warn("ai.consent: auto-approved write tool",
			slog.String("tool", req.Tool),
			slog.String("account", req.Account),
			slog.String("region", req.Region),
			slog.String("resource", req.Resource),
			slog.String("args", string(req.Args)),
		)
		recordAudit(req, ApproveOnce)
		return ApproveOnce
	}
	if consumeSession(req.Tool) {
		recordAudit(req, ApproveSession)
		return ApproveSession
	}
	d := ShowModal(req)
	if d == ApproveSession {
		rememberSession(req.Tool)
	}
	recordAudit(req, d)
	return d
}

// consumeSession reports whether tool has a non-expired session
// approval. A hit refreshes the lastUsed timestamp so the approval
// stays alive as long as the user keeps using the tool.
func consumeSession(tool string) bool {
	sessionMu.Lock()
	defer sessionMu.Unlock()
	last, ok := sessionApprovals[tool]
	if !ok {
		return false
	}
	if Now().Sub(last) >= sessionIdleTimeout {
		delete(sessionApprovals, tool)
		return false
	}
	sessionApprovals[tool] = Now()
	return true
}

// rememberSession records that the user approved tool for the rest
// of the session.
func rememberSession(tool string) {
	sessionMu.Lock()
	sessionApprovals[tool] = Now()
	sessionMu.Unlock()
}

// newSessionID returns 16 random bytes hex-encoded. ADR-0036's
// example uses a ULID-looking value; we use hex to avoid pulling
// in a ULID dependency since the audit consumers only need
// uniqueness within a process lifetime.
func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is catastrophic on supported platforms,
		// but the AI runtime continuing to deny with a stable
		// placeholder is better than panicking out of an init().
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b[:])
}
