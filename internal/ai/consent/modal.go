package consent

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// ModalFunc renders the per-call consent dialog for a write tool and
// returns the user's choice. The TUI and GUI override the
// package-level ShowModal from their init() functions; tests assign
// PromptStdinModal (or a hand-rolled stub) so non-interactive runs
// can still drive the gate.
type ModalFunc func(req Request) Decision

// ShowModal is invoked by Gate when the request is neither auto-
// approved nor session-approved. The default — denyModal — refuses
// every call. ADR-0036 requires "no state changes without an
// explicit yes"; with no UI loaded, no yes is reachable, so the
// safe default is to deny.
var ShowModal ModalFunc = denyModal

// denyModal is the bedrock default. It is hard-coded to Deny so a
// build without a TUI/GUI front-end cannot accidentally approve any
// write call.
func denyModal(_ Request) Decision { return Deny }

// PromptIn is the input stream PromptStdinModal reads from. Tests
// reassign it to a strings.Reader; production code never touches it.
var PromptIn io.Reader = os.Stdin

// PromptOut is where PromptStdinModal renders the dialog. The
// default is stderr, matching the rest of Packwright's "informational
// output goes to stderr" convention.
var PromptOut io.Writer = os.Stderr

// PromptStdinModal renders a compact text version of the ADR-0036
// modal to PromptOut and reads a single line from PromptIn:
//
//	y, o, once, 1   → ApproveOnce
//	s, a, session, 2 → ApproveSession
//	anything else, EOF → Deny
//
// It is the documented headless fallback. Tests that need to drive
// Gate end-to-end without a TUI swap ShowModal = PromptStdinModal
// and feed bytes through PromptIn.
func PromptStdinModal(req Request) Decision {
	renderPrompt(PromptOut, req)
	r := bufio.NewReader(PromptIn)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return Deny
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "o", "once", "1":
		return ApproveOnce
	case "s", "a", "session", "2":
		return ApproveSession
	default:
		return Deny
	}
}

// renderPrompt writes a text rendering of the ADR-0036 modal to w.
// The shape mirrors the ASCII art in the ADR so users who have seen
// the dialog in the TUI recognise the headless fallback.
func renderPrompt(w io.Writer, req Request) {
	fmt.Fprintln(w, "──────────── AI requests permission to ────────────")
	fmt.Fprintf(w, "  Tool      %s\n", req.Tool)
	if req.Account != "" || req.Profile != "" {
		fmt.Fprintf(w, "  Account   %s  (profile: %s)\n", req.Account, req.Profile)
	}
	if req.Region != "" {
		fmt.Fprintf(w, "  Region    %s\n", req.Region)
	}
	if req.Resource != "" {
		fmt.Fprintf(w, "  Resource  %s\n", req.Resource)
	}
	if len(req.Args) > 0 {
		fmt.Fprintln(w, "  ─ Args ───────────────────────────────────────────")
		fmt.Fprintln(w, indent(string(req.Args), "    "))
	}
	fmt.Fprintln(w, "  ─ AI reason ──────────────────────────────────────")
	fmt.Fprintln(w, indent(req.Reason, "    "))
	if req.BlastHint != "" {
		fmt.Fprintln(w, "  ─ Blast radius ───────────────────────────────────")
		fmt.Fprintln(w, indent(req.BlastHint, "    "))
	}
	fmt.Fprintln(w, "[y] approve once   [s] approve session   [n] deny ?")
}

// indent prefixes every line of s with prefix. Empty lines are
// prefixed too — when the diff or args payload contains a blank
// line, keeping the indentation makes the rendered block obviously
// part of one logical region.
func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// WarningBannerFunc renders the persistent banner mandated by
// ADR-0036 when AutoApproveTools is non-empty. The TUI and GUI
// overrides paint it red in the AI panel; the default writes a
// single line to stderr so headless contexts (CI, scripts, --version
// probes) still see the warning.
//
// The contract mirrors update.BannerFunc from MVP-4 PR-04: a single
// package-level function variable that front-ends override from
// their init(), with a stderr-line default for headless builds.
type WarningBannerFunc func(autoApprovedTools []string)

// WarningBanner is invoked by SetAutoApprove when its sanitised list
// has at least one entry. Tests reassign it to capture the call.
var WarningBanner WarningBannerFunc = defaultWarningBanner

// BannerOut is the writer defaultWarningBanner writes to. Tests
// redirect it to a buffer.
var BannerOut io.Writer = os.Stderr

func defaultWarningBanner(tools []string) {
	if len(tools) == 0 {
		return
	}
	fmt.Fprintf(BannerOut, "WARNING: AI auto-approve enabled for: %s\n",
		strings.Join(tools, ", "))
}
