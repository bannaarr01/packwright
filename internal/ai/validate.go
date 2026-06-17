package ai

import (
	"fmt"

	"github.com/bannaarr01/packwright/config"
	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// configKeyAutoApprove is the cfg.AI key holding the per-session auto-approve
// tool list (ADR-0036). Listing a write tool here makes the consent flow
// auto-grant it for the session — a deliberately dangerous escape hatch that
// [Validate] refuses to extend to any forbidden tool.
const configKeyAutoApprove = "auto_approve_tools"

// safetyBypassKeys are cfg.AI keys whose mere presence would, if honoured,
// weaken a hardcoded safety invariant (ADR-0033 read-by-default, ADR-0035
// forbidden-tool list, ADR-0036 consent, ADR-0037 outbound redactor, and the
// MVP-5 egress allowlist). None of these is a real schema key. Rather than
// silently ignore them — which would let a user believe they had disabled a
// guardrail they had not — [Validate] treats any of them as a hard error.
//
// This is the enforcement behind MVP-5 exit criterion 8: "the forbidden-tool
// list cannot be enabled even by editing config.yaml directly; the loader
// rejects the override."
var safetyBypassKeys = []string{
	"disable_consent",
	"allow_forbidden",
	"forbidden_tools",
	"forbidden",
	"auto_approve_all",
	"disable_redactor",
	"disable_redaction",
	"disable_egress",
	"allow_hosts",
	"allowed_hosts",
}

// Validate enforces the AI safety invariants that the YAML schema cannot
// express. It is the "loader rejects the override" gate (ADR-0035 / MVP-5
// exit criterion 8): a config.yaml that tries to disable consent, override the
// forbidden-tool list, turn off the outbound redactor or egress allowlist, or
// auto-approve a forbidden tool is rejected outright rather than honoured.
//
// A nil cfg or absent cfg.AI block is valid (AI simply unconfigured). Callers
// run Validate before constructing any AI subsystem; the chat engine refuses
// to start when it returns an error, so a poisoned config disables AI rather
// than quietly running with a weakened posture.
func Validate(cfg *config.Config) error {
	if cfg == nil || cfg.AI == nil {
		return nil
	}
	for _, k := range safetyBypassKeys {
		if _, ok := cfg.AI[k]; ok {
			return fmt.Errorf("ai: config key %q is not permitted — the safety posture "+
				"(per-call write consent, the forbidden-tool list, the outbound redactor, "+
				"and the egress allowlist) is hardcoded and cannot be overridden via config.yaml", k)
		}
	}
	for _, name := range autoApproveRaw(cfg) {
		if tools.IsForbidden(name) {
			return fmt.Errorf("ai: %s lists forbidden tool %q — the forbidden-tool list "+
				"cannot be enabled via config.yaml", configKeyAutoApprove, name)
		}
	}
	return nil
}

// AutoApproveTools returns the validated per-session auto-approve tool list
// from cfg.AI[auto_approve_tools]. It assumes [Validate] has already rejected
// any forbidden entry; as belt-and-braces it still drops forbidden names so a
// caller that skipped Validate cannot auto-approve a forbidden tool. The
// result feeds [consent.SetAutoApprove] when a session starts.
func AutoApproveTools(cfg *config.Config) []string {
	raw := autoApproveRaw(cfg)
	out := make([]string, 0, len(raw))
	for _, name := range raw {
		if name == "" || tools.IsForbidden(name) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// autoApproveRaw extracts cfg.AI[auto_approve_tools] as a string slice,
// tolerating the two shapes yaml.v3 produces: a []any of strings (the common
// case for an inline or block list) and a []string (rare, but cheap to honour).
// Non-string elements and other types yield no entries.
func autoApproveRaw(cfg *config.Config) []string {
	if cfg == nil || cfg.AI == nil {
		return nil
	}
	switch v := cfg.AI[configKeyAutoApprove].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
