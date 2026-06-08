package pack

// Decision is the outcome of a consent prompt: either the user trusts the
// pack at its current tree hash (Trusted) or the install is refused
// (Denied). The zero value is Denied so a forgotten override fails closed.
type Decision int

// Decision values. New entries should be appended; tests rely on Denied
// being the zero value.
const (
	// Denied means the user refused to install or update the pack at the
	// scanned surface and hash. Callers must treat the install as aborted.
	Denied Decision = iota
	// Trusted means the user explicitly accepted the scanned surface at
	// the current tree hash. Callers should record the hash and proceed.
	Trusted
)

// String renders d in a form suitable for logs and test failure messages.
// The values match the Go identifier names so a grep through audit logs
// for "Trusted" or "Denied" finds every relevant entry.
func (d Decision) String() string {
	switch d {
	case Trusted:
		return "Trusted"
	case Denied:
		return "Denied"
	default:
		return "Decision(?)"
	}
}

// Surface is the read-only, UI-agnostic description of every shell-execution
// site a pack declares. The TUI/GUI consent screen renders Surface; this
// package never formats it directly, so the same scan output flows
// unchanged into either front-end.
//
// A Surface with zero Commands describes a pack whose manifests are pure
// resource/form actions — nothing in it shells out. The consent screen
// still appears (per ADR-0025: the user must always opt in), but with an
// empty command list.
type Surface struct {
	// Commands lists every shell call discovered in the pack's manifests,
	// in stable order: by manifest path first, then by the order they
	// appear within the manifest. Identical pack content always produces
	// the same slice — Scan never reorders based on map iteration.
	Commands []Command
}

// Command is one shell-execution site declared by a pack manifest. The
// fields mirror the consent-screen mockup in ADR-0025 so the renderer
// can build that view without further transformation.
type Command struct {
	// Manifest is the pack-relative path of the manifest file this call
	// was extracted from (forward-slash form). Stable across operating
	// systems so the consent screen displays the same string everywhere.
	Manifest string

	// Slash is the manifest's slash command (e.g. "/restart-api"). Empty
	// when the source is a monitor panel or a composite step that does
	// not carry its own slash.
	Slash string

	// Source identifies where in the manifest the call was found:
	// "command", "composite-step", or "monitor-panel". Tests and the
	// consent screen group entries by Source.
	Source string

	// Argv is the resolved command tokens, copied verbatim from the
	// manifest. Template placeholders ("{{ .Project }}") are not
	// expanded — the consent screen shows what the author wrote, not
	// what runtime substitution will produce.
	Argv []string

	// Shell is the manifest's shell mode: "" for the default array form
	// (exec.Command) or "bash" for the opt-in bash -c form. ADR-0025
	// renders the latter with a ⚠ marker; this field is the source of
	// truth for that decision.
	Shell string
}

// Bash is "bash" — the only non-empty Shell value PR-06 recognises. Kept
// as a named constant so the consent renderer and any future audit log
// can branch on a stable identifier instead of a string literal.
const Bash = "bash"

// Source values populated on Command.Source. Stable identifiers that the
// consent renderer and tests assert against.
const (
	// SourceCommand identifies a kind: shell manifest's run.command.
	SourceCommand = "command"
	// SourceCompositeStep identifies a composite step whose body is an
	// inline shell call (composite steps that reference another slash
	// do not contribute a Command — their surface is owned by the
	// referenced manifest, which Scan visits independently).
	SourceCompositeStep = "composite-step"
	// SourceMonitorPanel identifies a shell/output panel inside a
	// kind: monitor manifest.
	SourceMonitorPanel = "monitor-panel"
)

// RequestConsent is the package-level hook the install / update flows
// invoke to obtain user consent. UI layers (TUI, GUI) overwrite this
// variable in their init() functions; the default returns Denied so a
// headless or test process never auto-trusts a pack.
//
// The function receives the scanned Surface and the hash recorded the
// last time the user trusted this pack (or "" for first install). UI
// layers diff the two to highlight new or changed shell calls.
//
// Calling site contract: the front-end's override must be safe to invoke
// from any goroutine, and must return after the user has made a choice
// — there is no cancellation channel. The follow-up consent-screen PR
// will introduce a context-aware variant if cancellation proves needed.
var RequestConsent func(s Surface, oldHash string) Decision = denyConsent

// denyConsent is the package's default RequestConsent: it ignores its
// inputs and returns Denied. It is exported as a function value (not a
// closure) so tests can swap RequestConsent for a custom implementation
// and restore the original via t.Cleanup without rebuilding the deny
// behaviour by hand.
func denyConsent(Surface, string) Decision { return Denied }
