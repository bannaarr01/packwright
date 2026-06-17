// Package ai is the foundation for Packwright's opt-in AI assistant
// (ADR-0033). PR-01 ships only the central enable-gate, the /ai slash-command
// entry point, and a conversation-persistence skeleton; provider, tool
// catalogue, consent, redactor, keychain, and cost-meter logic ships in
// PR-02–PR-07 of MVP-5.
//
// The package observes ADR-0033's off-by-default contract: when [Enabled]
// returns false, none of the AI subsystems initialize and no outbound HTTP
// transport addressing an LLM provider host is constructed. The contract is
// pinned in ai_test.go by an integration test that replaces
// http.DefaultTransport with a panic-on-use sentinel and exercises every
// public entry point with AI disabled, plus a static check that no file in
// this package imports net/http.
package ai

import (
	"context"
	"fmt"

	"github.com/bannaarr01/packwright/config"
)

// SlashCommand is the slash-command name that opens the AI panel. Exposed so
// future palette wiring can register the entry point without hard-coding the
// literal in two places.
const SlashCommand = "/ai"

// configKeyEnabled, configKeyProvider, and configKeyModel are the keys this
// package reads from and writes to cfg.AI (which the config package keeps as
// map[string]any so the AI schema can evolve PR-by-PR without breaking
// existing config.yaml files — see config.Config doc).
const (
	configKeyEnabled  = "enabled"
	configKeyProvider = "provider"
	configKeyModel    = "model"
)

// Settings is the typed view of cfg.AI used internally by this package.
// Callers should prefer the [Enabled] helper for the disabled-gate check; the
// struct is exposed so PR-02+ can read the wizard's persisted provider/model
// choice without re-implementing the map-to-struct cast.
type Settings struct {
	// Enabled mirrors cfg.AI["enabled"]; the central gate documented by
	// ADR-0033. Defaults to false when the key is missing or not a bool.
	Enabled bool
	// Provider is the LLM provider name the user picked in /ai setup
	// (e.g. "anthropic", "openai", "bedrock-anthropic", "ollama"). Empty
	// until the wizard has been run.
	Provider string
	// Model is the provider-specific model id (e.g. "claude-opus-4-8").
	// Empty until the wizard has been run.
	Model string
}

// LoadSettings extracts the typed AI settings from cfg. A nil cfg or a nil
// cfg.AI map both yield the zero Settings — equivalent to "AI never
// configured, disabled by default".
func LoadSettings(cfg *config.Config) Settings {
	var s Settings
	if cfg == nil || cfg.AI == nil {
		return s
	}
	if v, ok := cfg.AI[configKeyEnabled].(bool); ok {
		s.Enabled = v
	}
	if v, ok := cfg.AI[configKeyProvider].(string); ok {
		s.Provider = v
	}
	if v, ok := cfg.AI[configKeyModel].(string); ok {
		s.Model = v
	}
	return s
}

// Enabled is the central AI gate (ADR-0033). It reports whether the user has
// explicitly opted in. When false, every AI subsystem in PR-02+ must short-
// circuit before constructing an HTTP transport or invoking a provider.
//
// The integration test in ai_test.go pins this guarantee for PR-01: with
// cfg.AI["enabled"] == false (or absent), no outbound HTTP request is made by
// any exported function in this package.
func Enabled(cfg *config.Config) bool {
	return LoadSettings(cfg).Enabled
}

// Prompter is the UI-agnostic surface the /ai setup wizard talks to. Both
// front-ends (TUI and GUI) provide their own implementation; tests inject a
// scripted fake. Keeping the wizard's flow in this package — rather than
// duplicating it into tui/ and gui/ — means PR-06's keychain step can extend
// the same dialogue without touching the front-end packages.
type Prompter interface {
	// Select asks the user to pick one of options. Implementations should
	// return an error if the user aborts (e.g. presses Esc); the wizard
	// treats that as a non-fatal cancel and bubbles the error up.
	Select(label string, options []string) (string, error)
	// Input collects free-form text. defaultValue is the placeholder shown
	// when the user submits an empty line; implementations may return it
	// verbatim or prompt again — the wizard only requires that an empty
	// return value means "user accepted the default".
	Input(label, defaultValue string) (string, error)
	// Info displays a non-blocking message to the user. Used by the wizard
	// to surface the "next step: install PR-06" handoff without requiring
	// an acknowledgement that complicates GUI flows.
	Info(msg string)
}

// Run is the entry point a slash-command dispatcher should call for /ai. It
// observes the [Enabled] gate: when AI is disabled the call falls through to
// the setup wizard (DOD: "/ai opens the setup wizard when AI is disabled");
// when AI is enabled the call is a placeholder until PR-02 ships the chat UI.
//
// Run never constructs an HTTP transport — the chat panel is what eventually
// will, and it lives in a downstream PR. The integration test exercises Run
// with AI disabled to prove the no-outbound-HTTP property for the /ai
// dispatch path itself.
func Run(ctx context.Context, cfg *config.Config, p Prompter) error {
	if p == nil {
		return fmt.Errorf("ai: Run: prompter is required")
	}
	if !Enabled(cfg) {
		return Setup(ctx, cfg, p)
	}
	// Enabled-path placeholder: the chat panel lands in PR-02. Surfacing a
	// clear info message keeps /ai responsive (rather than silently
	// no-opping) once a user has completed PR-06's keychain step and
	// flipped enabled=true.
	p.Info("AI chat panel is not yet wired up in this build; expected in MVP-5 PR-02.")
	return nil
}
