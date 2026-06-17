package chat

import (
	"context"
	"encoding/json"

	"github.com/bannaarr01/packwright/internal/ai/consent"
	"github.com/bannaarr01/packwright/internal/ai/tools"
)

// consentGate adapts the package-level [consent.Gate] (ADR-0036) to the
// [tools.Gate] interface the tool registry consults before running a write
// tool. This adapter is the integration seam between PR-03 (tool catalogue,
// which ships a deny-everything default gate) and PR-04 (the consent flow):
// the chat engine installs it on the per-call context via [tools.WithGate] so
// every write-tool invocation surfaces the consent modal exactly once, while
// read tools — which never reach the gate — run without a prompt.
//
// The account / profile / region are captured once at session start from the
// active AWS client so the consent modal can show the operator which account a
// mutation would land in.
type consentGate struct {
	account string
	profile string
	region  string
}

// Allow maps a tools.ConsentRequest to a consent.Request, runs it through the
// ADR-0036 decision flow, and maps the reply back to a tools.Decision.
//
// Per ADR-0036 the AI must state a reason for every write; the registry passes
// the LLM's verbatim arg map, from which we read args["reason"]. consent.Gate
// denies outright when the reason is empty, so a model that requests a
// mutation without justifying it is refused before any modal is shown.
func (g consentGate) Allow(ctx context.Context, req tools.ConsentRequest) (tools.Decision, error) {
	reason, _ := req.Args["reason"].(string)
	raw, _ := json.Marshal(req.Args)

	decision := consent.Gate(ctx, consent.Request{
		Tool:     req.Tool,
		Account:  g.account,
		Profile:  g.profile,
		Region:   g.region,
		Resource: resourceHint(req.Args),
		Args:     raw,
		Reason:   reason,
	})

	switch decision {
	case consent.ApproveOnce:
		return tools.DecisionApproveOnce, nil
	case consent.ApproveSession:
		return tools.DecisionApproveSession, nil
	default:
		return tools.DecisionDeny, tools.ErrConsentDenied
	}
}

// resourceHint pulls a human-meaningful target out of a tool's arg map for the
// consent modal's "this will affect …" line. It checks the keys write tools
// conventionally use (stack name, service, ARN, path) and returns the first
// non-empty string it finds; an empty result simply leaves the modal's
// resource line blank rather than guessing.
func resourceHint(args map[string]any) string {
	for _, k := range []string{"stack_name", "stack", "service", "arn", "resource", "path", "name"} {
		if v, ok := args[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
