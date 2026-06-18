package manifest

// Draft metadata helpers (ADR-0047).
//
// A "draft" is a manifest that has been authored but not yet promoted for
// deploy. The state is encoded by a single "_"-prefixed root key — `_draft:
// true` — and is intentionally cheap: just a boolean. A second metadata key,
// `_copied_from`, records provenance when the manifest was forked via
// /copy-template; promotion keeps that key intact so a future "show me every
// fork of /alb" view drops out for free.
//
// These helpers are pure: they read and write fields on the Manifest struct
// without touching the filesystem. The /copy-template and /promote-template
// slash commands compose them with the atomic-write helpers in
// internal/scaffold/copy.go to land the changes on disk.

// IsDraft reports whether m is a draft manifest. A nil m is treated as not
// a draft so callers can fold the nil check into the same condition.
func IsDraft(m *Manifest) bool {
	if m == nil {
		return false
	}
	return m.Draft
}

// MarkDraft flips m into the draft state. It is idempotent: calling it on
// an already-draft manifest is a no-op. Nil m is silently ignored so
// upstream pipelines that route a possibly-nil pointer don't need a guard.
func MarkDraft(m *Manifest) {
	if m == nil {
		return
	}
	m.Draft = true
}

// Promote moves m out of the draft state. CopiedFrom (when set) is left
// alone — the provenance survives promotion so users keep an audit trail
// of where a deployed manifest originated. Idempotent on a non-draft.
func Promote(m *Manifest) {
	if m == nil {
		return
	}
	m.Draft = false
}

// CopiedFrom returns the `_copied_from` provenance string written by
// /copy-template, or "" when m was not produced by a copy. The format is
// the one CopyTemplate emits — `<source-slash> @ <source-path>` — but
// callers should treat the value as opaque.
func CopiedFrom(m *Manifest) string {
	if m == nil {
		return ""
	}
	return m.CopiedFrom
}
