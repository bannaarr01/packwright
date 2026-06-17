package tools

import (
	"context"
	"log/slog"
	"path"
	"strings"

	pwlog "github.com/bannaarr01/packwright/log"
)

// forbiddenExact lists tool names that may never be registered or called,
// no matter what permission tier they claim. Per ADR-0035 the list is
// hardcoded — it cannot be expanded via config, manifest, or runtime flag.
//
// Each entry is the canonical lowercase form with '/' separators. The
// lookup normalises the queried name to that form (lowercased, ':'→'/')
// so a tool registered as "iam:CreateUser", "IAM/CreateUser", or
// "iam/CreateUser" is rejected by the same rule.
var forbiddenExact = map[string]struct{}{
	// IAM credential / permission creation. Blast radius too high; even a
	// well-intentioned AI doing this is unacceptable.
	"iam/createuser":      {},
	"iam/createaccesskey": {},
	"iam/attachpolicy":    {},
	"iam/putrolepolicy":   {},
	"iam/createrole":      {},

	// S3 data-loss surface. Humans must drive deletions intentionally.
	"s3/deleteobject":        {},
	"s3/deletebucket":        {},
	"s3/deleteobjectversion": {},
}

// forbiddenGlob lists pattern-based forbidden names. Each pattern is
// matched against the normalised query string with path.Match (glob
// semantics: '*' matches any run of characters except '/'). The patterns
// cover ADR-0035's wildcard entries: every secretsmanager *Secret op, and
// every kms Schedule* / Disable* op.
//
// Additional patterns guard the "self-modifying safety posture" class
// from ADR-0035: anything ending in "/disable-consent" or matching the
// auto-approve-forever shape is forbidden regardless of which namespace
// it appears under.
var forbiddenGlob = []string{
	// SecretsManager: any tool whose action name contains "secret" under
	// the secretsmanager namespace (create, delete, restore, update, ...).
	"secretsmanager/*secret",
	"secretsmanager/*secret*",

	// KMS key material destruction surface. Disable* and Schedule*
	// (e.g. ScheduleKeyDeletion) are out of bounds for the AI.
	"kms/schedule*",
	"kms/disable*",

	// Self-modifying safety posture. The AI cannot disable or auto-approve
	// its own consent flow.
	"*/disable-consent",
	"*/auto-approve*",
}

// IsForbidden reports whether name matches the hardcoded forbidden list.
// The comparison is case-insensitive and treats ':' and '/' as the same
// separator, so a tool registered under either AWS-SDK ("iam:CreateUser")
// or slash ("iam/CreateUser") notation is rejected by the same rule.
func IsForbidden(name string) bool {
	q := normaliseName(name)
	if _, ok := forbiddenExact[q]; ok {
		return true
	}
	for _, pat := range forbiddenGlob {
		// path.Match is the standard glob matcher; '*' does not cross '/'.
		// We control both pattern and query so the only error path
		// (malformed pattern) is unreachable at runtime — but guard
		// against it anyway by treating a match error as "no match".
		if ok, _ := path.Match(pat, q); ok {
			return true
		}
	}
	return false
}

// normaliseName collapses ':' / '/' separators and lowercases the input so
// the forbidden-list lookup is independent of the calling convention the
// LLM or registrant happened to use.
func normaliseName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	return strings.ReplaceAll(s, ":", "/")
}

// logForbiddenAttempt records a structured security event when a forbidden
// tool is invoked through the registry. The event is emitted at warn level
// so it surfaces in the operational log and any downstream alerting; the
// args map is not logged verbatim to avoid leaking secrets, only its keys
// and the requested tool name.
//
// PR-04's audit log will record a second, finer-grained entry; this log
// line is the *operational* trail — useful when triaging "what did the AI
// just try to do?" without opening the audit file.
func logForbiddenAttempt(ctx context.Context, name string, args map[string]any) {
	pwlog.Default.LogAttrs(ctx, slog.LevelWarn,
		"ai tool: forbidden call refused",
		slog.String("event", "ai_tool_forbidden_call"),
		slog.String("tool", name),
		slog.Any("arg_keys", argKeys(args)),
	)
}

// argKeys returns the sorted keys of args so security events record what
// the AI tried to pass without leaking the values (which may include
// secrets, ARNs, etc. that ADR-0037's redactor will mask later).
func argKeys(args map[string]any) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, 0, len(args))
	for k := range args {
		out = append(out, k)
	}
	// Sort in place so log output is deterministic across runs.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
