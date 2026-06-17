package ai

import (
	"context"
	"fmt"
	"strings"

	"github.com/bannaarr01/packwright/config"
)

// Provider names recognised by the wizard. ADR-0034 calls out exactly these
// four; the wizard keeps the list short so users get a curated menu rather
// than a free-text box that turns into a typo farm.
const (
	ProviderAnthropic        = "anthropic"
	ProviderOpenAI           = "openai"
	ProviderBedrockAnthropic = "bedrock-anthropic"
	ProviderOllama           = "ollama"
)

// supportedProviders is the wizard's provider menu in display order.
// Anthropic is first because ADR-0034 declares it the default.
var supportedProviders = []string{
	ProviderAnthropic,
	ProviderOpenAI,
	ProviderBedrockAnthropic,
	ProviderOllama,
}

// defaultModelFor returns the suggested model for a provider — used as the
// pre-filled default in the model prompt. The user can override; we do not
// validate that the typed-in string is a real model name (the provider
// package in PR-02 will surface a clearer error on the first call than we
// could here).
func defaultModelFor(provider string) string {
	switch provider {
	case ProviderAnthropic, ProviderBedrockAnthropic:
		return "claude-opus-4-8"
	case ProviderOpenAI:
		return "gpt-4o"
	case ProviderOllama:
		return "llama3"
	default:
		return ""
	}
}

// Setup runs the /ai setup wizard. Per the PR-01 contract it collects only
// the provider and model — never the API key, which lives in the OS keychain
// owned by PR-06 (ADR-0038). The wizard persists provider and model to
// config.yaml and exits with a clear "install PR-06 to finish" handoff.
//
// The wizard does NOT flip cfg.AI["enabled"] to true. Setting enabled=true
// without a stored key would advertise a feature that cannot run — the
// keychain step in PR-06 is what actually transitions a user to the
// enabled state. Until then, /ai remains the wizard entry point (Run
// dispatches back here on every invocation while disabled).
//
// ctx is currently unused — the wizard is interactive and runs in the
// caller's foreground — but is accepted so PR-02+ can plumb through
// cancellation without changing the signature.
func Setup(ctx context.Context, cfg *config.Config, p Prompter) error {
	_ = ctx
	if cfg == nil {
		return fmt.Errorf("ai: Setup: cfg is required")
	}
	if p == nil {
		return fmt.Errorf("ai: Setup: prompter is required")
	}

	current := LoadSettings(cfg)

	provider, err := p.Select("Choose your LLM provider", supportedProviders)
	if err != nil {
		return fmt.Errorf("ai: Setup: provider: %w", err)
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		// Fall back to the user's current pick (if any) rather than
		// fail; an empty submit from a Select implementation is
		// equivalent to "keep what we had".
		provider = current.Provider
	}
	if provider == "" {
		return fmt.Errorf("ai: Setup: provider selection is required")
	}

	model, err := p.Input("Model id", defaultModelFor(provider))
	if err != nil {
		return fmt.Errorf("ai: Setup: model: %w", err)
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = defaultModelFor(provider)
	}
	if model == "" {
		return fmt.Errorf("ai: Setup: model id is required")
	}

	if cfg.AI == nil {
		cfg.AI = map[string]any{}
	}
	cfg.AI[configKeyProvider] = provider
	cfg.AI[configKeyModel] = model
	// Intentionally do not set configKeyEnabled here; see Setup doc.
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("ai: Setup: save config: %w", err)
	}

	p.Info(fmt.Sprintf(
		"Provider %q and model %q saved. AI remains disabled until your API key is stored.\nNext step: install PR-06 (OS-keychain key storage) to finish enabling AI.",
		provider, model,
	))
	return nil
}
